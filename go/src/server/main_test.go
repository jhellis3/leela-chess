package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"server/db"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"client/http"
)

type StoreSuite struct {
	suite.Suite

	router *gin.Engine
	w      *httptest.ResponseRecorder
}

func (s *StoreSuite) SetupSuite() {
	db.Init(false)

	s.router = setupRouter()
}

func (s *StoreSuite) SetupTest() {
	err := db.GetDB().DropTable(
		&db.User{},
		&db.TrainingRun{},
		&db.Network{},
		&db.Match{},
		&db.MatchGame{},
		&db.TrainingGame{},
	).Error
	if err != nil {
		log.Fatal(err)
	}
	db.SetupDB()

	network := db.Network{Sha: "abcd", Path: "/tmp/network"}
	if err := db.GetDB().Create(&network).Error; err != nil {
		log.Fatal(err)
	}

	training_run := db.TrainingRun{Description: "Testing", BestNetwork: network, Active: true}
	if err := db.GetDB().Create(&training_run).Error; err != nil {
		log.Fatal(err)
	}

	user := db.User{Username: "defaut", Password: "1234"}
	if err := db.GetDB().Create(&user).Error; err != nil {
		log.Fatal(err)
	}

	s.w = httptest.NewRecorder()
}

func (s *StoreSuite) TearDownSuite() {
	db.Close()
}

// This is the actual "test" as seen by Go, which runs the tests defined below
func TestStoreSuite(t *testing.T) {
	s := new(StoreSuite)
	suite.Run(t, s)
}

func postParams(params map[string]string) *strings.Reader {
	data := url.Values{}
	for key, val := range params {
		data.Set(key, val)
	}
	return strings.NewReader(data.Encode())
}

func initMatch(matchDone bool) {
	candidate_network := db.Network{Sha: "efgh", Path: "/tmp/network2"}
	if err := db.GetDB().Create(&candidate_network).Error; err != nil {
		log.Fatal(err)
	}

	match := db.Match{
		TrainingRunID: 1,
		Parameters:    `["--visits 10"]`,
		CandidateID:   candidate_network.ID,
		CurrentBestID: 1,
		Done:          matchDone,
	}
	if err := db.GetDB().Create(&match).Error; err != nil {
		log.Fatal(err)
	}
}

// For backwards compatibility in short term.
func (s *StoreSuite) TestNextGameNoUser() {
	req, _ := http.NewRequest("POST", "/next_game", nil)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())
	assert.JSONEqf(s.T(), `{"params":"","type":"train","trainingId":1,"networkId":1,"sha":"abcd"}`, s.w.Body.String(), "Body incorrect")
}

// Make sure old users don't get match games
func (s *StoreSuite) TestNextGameNoUserMatch() {
	initMatch(false)

	req, _ := http.NewRequest("POST", "/next_game", nil)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())
	assert.JSONEqf(s.T(), `{"params":"","type":"train","trainingId":1,"networkId":1,"sha":"abcd"}`, s.w.Body.String(), "Body incorrect")
}

func (s *StoreSuite) TestNextGameUserNoMatch() {
	req, _ := http.NewRequest("POST", "/next_game", postParams(map[string]string{"user": "default", "password": "1234", "version": "2"}))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())
	assert.JSONEqf(s.T(), `{"params":"","type":"train","trainingId":1,"networkId":1,"sha":"abcd"}`, s.w.Body.String(), "Body incorrect")
}

func (s *StoreSuite) TestNextGameUserMatch() {
	initMatch(false)

	req, _ := http.NewRequest("POST", "/next_game", postParams(map[string]string{"user": "default", "password": "1234", "version": "2"}))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())
	assert.JSONEqf(s.T(), `{"params":"[\"--visits 10\"]","type":"match","matchGameId":1,"sha":"abcd","candidateSha":"efgh"}`, s.w.Body.String(), "Body incorrect")
}

func (s *StoreSuite) TestNextGameUserMatchDone() {
	initMatch(true)

	req, _ := http.NewRequest("POST", "/next_game", postParams(map[string]string{"user": "default", "password": "1234", "version": "2"}))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	// Shouldn't get a match back
	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())
	assert.JSONEqf(s.T(), `{"params":"","type":"train","trainingId":1,"networkId":1,"sha":"abcd"}`, s.w.Body.String(), "Body incorrect")
}

func (s *StoreSuite) TestUploadGameNewUser() {
	extraParams := map[string]string{
		"user":        "foo",
		"password":    "asdf",
		"training_id": "1",
		"network_id":  "1",
		"version":     "1",
	}
	tmpfile, _ := ioutil.TempFile("", "example")
	defer os.Remove(tmpfile.Name())
	req, err := client.BuildUploadRequest("/upload_game", extraParams, "file", tmpfile.Name())
	if err != nil {
		log.Fatal(err)
	}
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())

	// Check we create the new user
	user := db.User{}
	err = db.GetDB().Where("username = ?", "foo").First(&user).Error
	if err != nil {
		log.Fatal(err)
	}

	// Check we update the game count properly
	network := db.Network{}
	err = db.GetDB().Where("id = ?", 1).First(&network).Error
	if err != nil {
		log.Fatal(err)
	}
	assert.Equal(s.T(), 1, network.GamesPlayed)
}

func uploadTestNetwork(s *StoreSuite, contentString string, networkId int) {
	s.w = httptest.NewRecorder()
	content := []byte(contentString)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(content)
	zw.Close()

	extraParams := map[string]string{
		"training_id": "1",
		"layers":      "6",
		"filters":     "64",
	}
	tmpfile, _ := ioutil.TempFile("", "example")
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write(buf.Bytes()); err != nil {
		log.Fatal(err)
	}
	req, err := client.BuildUploadRequest("/upload_network", extraParams, "file", tmpfile.Name())
	if err != nil {
		log.Fatal(err)
	}
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())

	// Trying to upload the same network should now fail
	s.w = httptest.NewRecorder()
	req, err = client.BuildUploadRequest("/upload_network", extraParams, "file", tmpfile.Name())
	if err != nil {
		log.Fatal(err)
	}
	s.router.ServeHTTP(s.w, req)
	assert.Equal(s.T(), 400, s.w.Code, s.w.Body.String())

	// Now we should be able to query for this network
	s.w = httptest.NewRecorder()
	sha := sha256.Sum256(content)
	req, _ = http.NewRequest("POST", "/next_game", nil)
	s.router.ServeHTTP(s.w, req)
	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())
	assert.JSONEqf(s.T(), fmt.Sprintf(`{"params":"", "type":"train","trainingId":1,"networkId":%d,"sha":"%x"}`, networkId, sha), s.w.Body.String(), "Body incorrect")

	// And let's download it now.
	s.w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", fmt.Sprintf("/get_network?sha=%x", sha), nil)
	s.router.ServeHTTP(s.w, req)
	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())

	// Should match the contents
	zr, err := gzip.NewReader(s.w.Body)
	if err != nil {
		log.Fatal(err)
	}
	buf.Reset()
	if _, err := io.Copy(&buf, zr); err != nil {
		log.Fatal(err)
	}
	assert.Equal(s.T(), contentString, buf.String(), "Contents don't match")
}

func (s *StoreSuite) TestUploadNetwork() {
	uploadTestNetwork(s, "this_is_a_network", 2)
	uploadTestNetwork(s, "network2", 3)
}

func (s *StoreSuite) TestPostMatchResult() {
	initMatch(false)

	// Initialize the MatchGame
	req, _ := http.NewRequest("POST", "/next_game", postParams(map[string]string{"user": "default", "password": "1234", "version": "2"}))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())

	// Now, post a result from the match
	req, _ = http.NewRequest("POST", "/match_result", postParams(map[string]string{
		"user":          "default",
		"password":      "1234",
		"version":       "2",
		"match_game_id": "1",
		"result":        "-1",
		"pgn":           "asdf",
	}))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s.router.ServeHTTP(s.w, req)

	assert.Equal(s.T(), 200, s.w.Code, s.w.Body.String())

	// Check that the match is present now.
	match_game := db.MatchGame{}
	err := db.GetDB().Where("id = ?", 1).First(&match_game).Error
	if err != nil {
		log.Fatal(err)
	}

	assert.Equal(s.T(), -1, match_game.Result)
	assert.Equal(s.T(), "asdf", match_game.Pgn)
	assert.Equal(s.T(), true, match_game.Done)
}
