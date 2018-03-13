package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"client/http"
)

var HOSTNAME = flag.String("hostname", "http://162.217.248.187", "Address of the server")
var USER = flag.String("user", "", "Username")
var PASSWORD = flag.String("password", "", "Password")
var GPU = flag.Int("gpu", 0, "ID of the OpenCL device to use (-1 for no GPU)")

func getExtraParams() map[string]string {
	return map[string]string{
		"user":     *USER,
		"password": *PASSWORD,
		"version":  "2",
	}
}

func uploadGame(httpClient *http.Client, path string, pgn string, nextGame client.NextGameResponse) error {
	extraParams := getExtraParams()
	extraParams["training_id"] = strconv.Itoa(int(nextGame.TrainingId))
	extraParams["network_id"] = strconv.Itoa(int(nextGame.NetworkId))
	extraParams["pgn"] = pgn
	request, err := client.BuildUploadRequest(*HOSTNAME+"/upload_game", extraParams, "file", path)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	body := &bytes.Buffer{}
	_, err = body.ReadFrom(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	fmt.Println(resp.StatusCode)
	fmt.Println(resp.Header)
	fmt.Println(body)

	return nil
}

type CmdWrapper struct {
	Cmd   *exec.Cmd
	Pgn   string
	Input io.WriteCloser
}

func (c *CmdWrapper) openInput() {
	var err error
	c.Input, err = c.Cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
}

func (c *CmdWrapper) launch(networkPath string, args []string) {
	var gpu_id string = ""
	if *GPU != -1 {
		gpu_id = fmt.Sprintf("--gpu=%v", *GPU)
	}
	weights := fmt.Sprintf("--weights=%s", networkPath)
	dir, _ := os.Getwd()
	c.Cmd = exec.Command(path.Join(dir, "lczero"), weights, gpu_id, "--randomize", "--noise", "-t1", "--quiet")
	c.Cmd.Args = append(c.Cmd.Args, args...)

	stdout, err := c.Cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}

	stderr, err := c.Cmd.StderrPipe()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		stdoutScanner := bufio.NewScanner(stdout)
		reading_pgn := false
		for stdoutScanner.Scan() {
			line := stdoutScanner.Text()
			fmt.Printf("%s\n", line)
			if line == "PGN" {
				reading_pgn = true
			} else if line == "END" {
				reading_pgn = false
			} else if reading_pgn {
				c.Pgn += line + "\n"
			}
		}
	}()

	go func() {
		stderrScanner := bufio.NewScanner(stderr)
		for stderrScanner.Scan() {
			fmt.Printf("%s\n", stderrScanner.Text())
		}
	}()

	err = c.Cmd.Start()
	if err != nil {
		log.Fatal(err)
	}
}

func playMatch(baselinePath string, candidatePath string, params []string, flip bool) {
	baseline := CmdWrapper{}
	baseline.launch(baselinePath, params)
	baseline.openInput()
	defer baseline.Input.Close()

	candidate := CmdWrapper{}
	candidate.launch(candidatePath, params)
	candidate.openInput()
	defer candidate.Input.Close()

	p1 := &baseline
	p2 := &candidate

	if flip {
		p2, p1 = p1, p2
	}

	// Play a game using UCI
	is_white := true
	for {
		var p *CmdWrapper
		if is_white {
			p = p1
		} else {
			p = p2
		}
		p.Input.WriteString()
	}
}

func train(networkPath string, params []string) (string, string) {
	// pid is intended for use in multi-threaded training
	pid := os.Getpid()

	dir, _ := os.Getwd()
	train_dir := path.Join(dir, fmt.Sprintf("data-%v", pid))
	if _, err := os.Stat(train_dir); err == nil {
		files, err := ioutil.ReadDir(train_dir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Cleanup training files:\n")
		for _, f := range files {
			fmt.Printf("%s/%s\n", train_dir, f.Name())
		}
		err = os.RemoveAll(train_dir)
		if err != nil {
			log.Fatal(err)
		}
	}

	num_games := 1
	train_cmd := fmt.Sprintf("--start=train %v %v", pid, num_games)
	params = append(params, train_cmd)

	c := CmdWrapper{}
	c.launch(networkPath, params)

	err := c.Cmd.Wait()
	if err != nil {
		log.Fatal(err)
	}

	return path.Join(train_dir, "training.0.gz"), c.Pgn
}

func getNetwork(httpClient *http.Client, sha string, clearOld bool) (string, error) {
	// Sha already exists?
	path := filepath.Join("networks", sha)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	if clearOld {
		// Clean out any old networks
		os.RemoveAll("networks")
	}
	os.MkdirAll("networks", os.ModePerm)

	// Otherwise, let's download it
	err := client.DownloadNetwork(httpClient, *HOSTNAME, path, sha)
	if err != nil {
		return "", err
	}
	return path, nil
}

func nextGame(httpClient *http.Client) error {
	nextGame, err := client.NextGame(httpClient, *HOSTNAME, getExtraParams())
	if err != nil {
		return err
	}
	if nextGame.Type == "match" {
		networkPath, err := getNetwork(httpClient, nextGame.Sha, false)
		if err != nil {
			return err
		}
		candidatePath, err := getNetwork(httpClient, nextGame.CandidateSha, false)
		if err != nil {
			return err
		}
		playMatch(networkPath, candidatePath, nextGame.Params, nextGame.Flip)
		return nil
	} else if nextGame.Type == "train" {
		networkPath, err := getNetwork(httpClient, nextGame.Sha, true)
		if err != nil {
			return err
		}
		trainFile, pgn := train(networkPath, nextGame.Params)
		uploadGame(httpClient, trainFile, pgn, nextGame)
		return nil
	}

	return errors.New("Unknown game type: " + nextGame.Type)
}

func main() {
	flag.Parse()
	if len(*USER) == 0 {
		log.Fatal("You must specify a username")
	}
	if len(*PASSWORD) == 0 {
		log.Fatal("You must specify a non-empty password")
	}

	httpClient := &http.Client{}
	for {
		err := nextGame(httpClient)
		if err != nil {
			log.Print(err)
			log.Print("Sleeping for 30 seconds...")
			time.Sleep(30 * time.Second)
			continue
		}
	}
}
