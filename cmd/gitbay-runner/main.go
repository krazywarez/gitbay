// gitbay-runner executes CI builds queued by a gitbay server. It polls over
// SSH — the same authenticated channel everything else uses — claims one
// build at a time, clones the repo, runs each step with `sh -c`, streams the
// combined output back, and reports success or failure.
//
// The account behind the runner's key must be an instance admin: a runner
// executes arbitrary repo code, so handing out jobs is the operator's call.
// v1 runs steps directly on the host under this process's user; run it as a
// dedicated unprivileged user.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/buildinfo"
)

type job struct {
	ID      int64             `json:"id"`
	Repo    string            `json:"repo"`
	Number  int64             `json:"number"`
	Job     string            `json:"job"`
	SHA     string            `json:"sha"`
	Ref     string            `json:"ref"`
	Steps   []string          `json:"steps"`
	Secrets map[string]string `json:"secrets"`
}

type runner struct {
	remote    string // ssh destination, e.g. git@gitbay.org
	sshOpts   []string
	cloneBase string // e.g. ssh://git@gitbay.org
	workdir   string
	timeout   time.Duration
	// repos limits which repositories this runner claims builds for. Empty
	// means any, which is what a runner on the server itself wants; a runner
	// somewhere that should not execute every repository's steps names them.
	repos []string
}

func main() {
	var (
		remote    = flag.String("remote", "git@gitbay.org", "ssh destination of the gitbay server")
		sshOpts   = flag.String("ssh-opts", "", "extra ssh options, space-separated (also used for git clone)")
		cloneBase = flag.String("clone-base", "", "clone URL prefix (default ssh://<remote>)")
		workdir   = flag.String("workdir", filepath.Join(os.TempDir(), "gitbay-runner"), "build workspace root")
		poll      = flag.Duration("poll", 5*time.Second, "idle poll interval")
		timeout   = flag.Duration("timeout", 30*time.Minute, "per-build time limit")
		repos     = flag.String("repos", "", "only claim builds for these repositories, comma-separated owner/name (default: any)")
		once      = flag.Bool("once", false, "process at most one build, then exit")
		version   = flag.Bool("version", false, "print the commit this binary was built from, then exit")
	)
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.String())
		return
	}
	// The runner links internal/store, so it goes stale on changes that never
	// touch cmd/gitbay-runner. Say which commit is running.
	log.Printf("gitbay-runner %s", buildinfo.String())
	r := &runner{
		remote:    *remote,
		cloneBase: *cloneBase,
		workdir:   *workdir,
		timeout:   *timeout,
	}
	if *sshOpts != "" {
		r.sshOpts = strings.Fields(*sshOpts)
	}
	for _, name := range strings.Split(*repos, ",") {
		if name = strings.TrimSpace(name); name != "" {
			r.repos = append(r.repos, name)
		}
	}
	if r.cloneBase == "" {
		r.cloneBase = "ssh://" + *remote
	}
	if err := os.MkdirAll(r.workdir, 0o755); err != nil {
		log.Fatal(err)
	}
	for {
		ran, err := r.step()
		if err != nil {
			log.Printf("runner: %v", err)
		}
		if *once {
			return
		}
		if !ran {
			time.Sleep(*poll)
		}
	}
}

// step claims and executes at most one build. ran reports whether there was
// one, so the caller knows when to idle.
func (r *runner) step() (bool, error) {
	out, err := r.ssh(nil, append([]string{"runner", "next"}, append(r.repos, "--json")...)...)
	if err != nil {
		return false, fmt.Errorf("claiming build: %w (%s)", err, out)
	}
	var env struct {
		Data job `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return false, fmt.Errorf("parsing job: %w", err)
	}
	if env.Data.ID == 0 {
		return false, nil
	}
	j := env.Data
	log.Printf("build %d: %s %s @ %.10s", j.ID, j.Repo, j.Job, j.SHA)
	status := "failure"
	if r.run(j) {
		status = "success"
	}
	if out, err := r.ssh(nil, "runner", "done", fmt.Sprint(j.ID), status); err != nil {
		return true, fmt.Errorf("reporting build %d: %w (%s)", j.ID, err, out)
	}
	log.Printf("build %d: %s", j.ID, status)
	return true, nil
}

// logSink forwards a build's output to the server and swallows any error
// doing so. os/exec surfaces a write failure on a step's stdout through
// cmd.Wait(), so a sink that can fail is a sink that can fail the build it
// was only recording — a restart or a dropped session used to turn a green
// suite red, with the explaining line written to the same dead pipe. Losing
// log lines is the acceptable failure here; losing the build is not.
type logSink struct {
	mu sync.Mutex
	w  io.Writer // nil once a write has failed
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		if _, err := s.w.Write(p); err != nil {
			s.w = nil
		}
	}
	return len(p), nil
}

// broken reports whether the stream was lost, so a build can say its log is
// incomplete rather than appear to have simply stopped.
func (s *logSink) broken() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w == nil
}

// run clones, checks out, and executes the steps, streaming output to the
// server. Returns whether every step succeeded.
func (r *runner) run(j job) bool {
	dir := filepath.Join(r.workdir, fmt.Sprintf("build-%d", j.ID))
	defer os.RemoveAll(dir)

	// One long-lived `runner log` session receives the whole stream.
	logCmd := exec.Command("ssh", append(r.sshOpts, r.remote, "runner", "log", fmt.Sprint(j.ID))...)
	pipe, err := logCmd.StdinPipe()
	if err != nil {
		log.Printf("build %d: log pipe: %v", j.ID, err)
		return false
	}
	sink := &logSink{w: pipe}
	logCmd.Stdout, logCmd.Stderr = io.Discard, io.Discard
	if err := logCmd.Start(); err != nil {
		log.Printf("build %d: log stream: %v", j.ID, err)
		return false
	}
	defer func() {
		if sink.broken() {
			log.Printf("build %d: log stream lost; stored log is incomplete", j.ID)
		}
		pipe.Close()
		logCmd.Wait()
	}()

	gitSSH := strings.TrimSpace("ssh " + strings.Join(r.sshOpts, " "))
	cloneURL := r.cloneBase + "/" + j.Repo + ".git"
	fmt.Fprintf(sink, "$ git clone %s (%.10s)\n", cloneURL, j.SHA)
	for _, args := range [][]string{
		{"clone", "-q", cloneURL, dir},
		{"-C", dir, "checkout", "-q", j.SHA},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+gitSSH, "GIT_TERMINAL_PROMPT=0")
		cmd.Stdout, cmd.Stderr = sink, sink
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(sink, "git %s: %v\n", args[0], err)
			return false
		}
	}

	deadline := time.Now().Add(r.timeout)
	for _, step := range j.Steps {
		fmt.Fprintf(sink, "$ %s\n", step)
		cmd := exec.Command("sh", "-c", step)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GITBAY_REPO="+j.Repo, "GITBAY_SHA="+j.SHA, "GITBAY_REF="+j.Ref, "GITBAY_JOB="+j.Job, "CI=true")
		for name, value := range j.Secrets {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
		cmd.Stdout, cmd.Stderr = sink, sink
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(sink, "start: %v\n", err)
			return false
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				fmt.Fprintf(sink, "step failed: %v\n", err)
				return false
			}
		case <-time.After(time.Until(deadline)):
			cmd.Process.Kill()
			fmt.Fprintf(sink, "build timed out after %s\n", r.timeout)
			return false
		}
	}
	return true
}

// ssh runs one control command against the server and returns stdout.
func (r *runner) ssh(stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command("ssh", append(append(r.sshOpts, r.remote), args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String() + errOut.String(), err
	}
	return out.String(), nil
}
