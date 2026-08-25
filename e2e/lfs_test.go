package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("listener did not come back")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLFS(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs client not installed")
	}
	inst := startInstance(t)
	// LFS hands clients absolute hrefs built from site_url; point it at
	// the live HTTP listener so the real git-lfs client can follow them.
	inst.proc.Process.Kill()
	inst.proc.Wait()
	raw, err := os.ReadFile(inst.config)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`site_url = "https://gitbay.test"`),
		[]byte(fmt.Sprintf(`site_url = "http://127.0.0.1:%d"`, inst.httpPort)), 1)
	os.WriteFile(inst.config, raw, 0o600)
	inst.proc = exec.Command(inst.gitbayd, "--config", inst.config, "serve")
	inst.proc.Stderr = os.Stderr
	if err := inst.proc.Start(); err != nil {
		t.Fatal(err)
	}
	waitForPort(t, inst.port)

	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/big"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/big"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "lfs", "install", "--local")
	mustGit(t, dir, env, "lfs", "track", "*.bin")
	payload := make([]byte, 1<<20)
	rand.Read(payload)
	os.WriteFile(filepath.Join(dir, "data.bin"), payload, 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "big file")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// The object landed in content-addressed storage, not in git.
	oid := sha256.Sum256(payload)
	oidHex := hex.EncodeToString(oid[:])
	stored := filepath.Join(inst.root, "lfs", oidHex[:2], oidHex[2:4], oidHex)
	if fi, err := os.Stat(stored); err != nil || fi.Size() != int64(len(payload)) {
		t.Fatalf("object not in lfs store: %v", err)
	}

	// A fresh SSH clone round-trips the content through the smudge filter.
	work2 := t.TempDir()
	mustGit(t, work2, env, "clone", inst.sshURL("alice/big"), "w")
	dir2 := filepath.Join(work2, "w")
	mustGit(t, dir2, env, "lfs", "install", "--local")
	mustGit(t, dir2, env, "lfs", "pull", "origin")
	got, err := os.ReadFile(filepath.Join(dir2, "data.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("ssh round-trip: %v, %d bytes", err, len(got))
	}

	// Anonymous HTTPS: public repos serve LFS downloads with no credentials.
	httpURL := fmt.Sprintf("http://127.0.0.1:%d/alice/big.git", inst.httpPort)
	work3 := t.TempDir()
	mustGit(t, work3, env, "clone", httpURL, "w")
	dir3 := filepath.Join(work3, "w")
	mustGit(t, dir3, env, "lfs", "install", "--local")
	mustGit(t, dir3, env, "lfs", "pull", "origin")
	if got, err := os.ReadFile(filepath.Join(dir3, "data.bin")); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("anonymous http round-trip: %v, %d bytes", err, len(got))
	}

	// Anonymous upload is refused; so is anything on a private repo.
	batch := func(repo, op, auth string) int {
		body := fmt.Sprintf(`{"operation":%q,"transfers":["basic"],"objects":[{"oid":%q,"size":4}]}`, op, oidHex)
		req, _ := http.NewRequest("POST",
			fmt.Sprintf("http://127.0.0.1:%d/alice/%s.git/info/lfs/objects/batch", inst.httpPort, repo),
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := batch("big", "upload", ""); code != 403 {
		t.Fatalf("anonymous upload: %d", code)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/vault", "--private"); code != 0 {
		t.Fatal("private repo create failed")
	}
	if code := batch("vault", "download", ""); code != 404 {
		t.Fatalf("anonymous private batch: %d", code)
	}

	// Access rules over SSH: a stranger's authenticate on a private repo
	// reads as nonexistence; upload needs write.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, bobKey, "", "git-lfs-authenticate", "alice/vault", "download"); code != 3 || !strings.Contains(errOut, "not found") {
		t.Fatalf("stranger authenticate: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "git-lfs-authenticate", "alice/big", "upload"); code != 4 || !strings.Contains(errOut, "denied") {
		t.Fatalf("read-only upload authenticate: exit %d, %s", code, errOut)
	}

	// A corrupt upload is refused and stores nothing: mint an upload token
	// via authenticate, then PUT a body that does not match the oid.
	out, _, code := inst.ssh(t, aliceKey, "", "git-lfs-authenticate", "alice/big", "upload")
	if code != 0 {
		t.Fatalf("authenticate: %s", out)
	}
	var grant struct {
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal([]byte(out), &grant); err != nil {
		t.Fatalf("authenticate JSON: %v\n%s", err, out)
	}
	fakeOID := strings.Repeat("ab", 32)
	req, _ := http.NewRequest("PUT",
		fmt.Sprintf("http://127.0.0.1:%d/alice/big.git/info/lfs/objects/%s", inst.httpPort, fakeOID),
		strings.NewReader("not the content"))
	req.Header.Set("Authorization", grant.Header["Authorization"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("corrupt upload: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(inst.root, "lfs", "ab", "ab", fakeOID)); err == nil {
		t.Fatal("corrupt object was stored")
	}
}
