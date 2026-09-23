package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// streamReader collects what r delivers, so a test can wait for text to
// arrive while the writer is still going.
type streamReader struct {
	ch  chan []byte
	buf strings.Builder
}

func newStreamReader(r io.Reader) *streamReader {
	s := &streamReader{ch: make(chan []byte, 16)}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			if n > 0 {
				s.ch <- append([]byte(nil), b[:n]...)
			}
			if err != nil {
				close(s.ch)
				return
			}
		}
	}()
	return s
}

func (s *streamReader) waitFor(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(s.buf.String(), want) {
		select {
		case b, ok := <-s.ch:
			if !ok {
				t.Fatalf("stream ended before %q:\n%s", want, s.buf.String())
			}
			s.buf.Write(b)
		case <-deadline:
			t.Fatalf("no %q after 20s:\n%s", want, s.buf.String())
		}
	}
	return s.buf.String()
}

// A running build is followed over ssh and on its page: output the runner
// sends arrives while the build runs, and both end with the outcome.
func TestBuildLogFollow(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Claim build 1 by hand, so the test decides when output arrives.
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 {
		t.Fatalf("runner next: %s", errOut)
	}
	var claim struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &claim); err != nil || claim.Data.ID == 0 {
		t.Fatalf("runner next output %q: %v", out, err)
	}
	id := fmt.Sprint(claim.Data.ID)

	cmd := inst.sshCmd(aliceKey, "build", "log", "alice/app", "1", "--follow")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// If the test fails before the final cmd.Wait below, kill the follow
	// instead of leaving it running. Once that Wait has run,
	// cmd.ProcessState is set and this is a no-op.
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	follow := newStreamReader(stdout)

	page, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/app/builds/1", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	web := newStreamReader(page.Body)
	web.waitFor(t, "Live: the log streams here")

	// A static render while the build runs returns at once.
	static := &http.Client{Timeout: 10 * time.Second}
	resp, err := static.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/app/builds/1?follow=0", inst.httpPort))
	if err != nil {
		t.Fatalf("?follow=0 did not return: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "Live:") {
		t.Fatalf("?follow=0 rendered the live page:\n%s", body)
	}

	if _, errOut, code := inst.ssh(t, runnerKey, "hello from the runner <b>\n", "runner", "log", id); code != 0 {
		t.Fatalf("runner log: %s", errOut)
	}
	follow.waitFor(t, "hello from the runner <b>\n")
	web.waitFor(t, "hello from the runner &lt;b&gt;")

	if _, errOut, code := inst.ssh(t, runnerKey, "", "runner", "done", id, "success"); code != 0 {
		t.Fatalf("runner done: %s", errOut)
	}
	web.waitFor(t, `<p class="notice" role="status">build finished: success</p>`)
	web.waitFor(t, "</html>")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("follow exited: %v\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "build 1 success" {
		t.Errorf("follow stderr %q", got)
	}
}
