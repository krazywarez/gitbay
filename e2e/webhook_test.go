package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookReceiver captures webhook deliveries and can be told to fail.
type hookReceiver struct {
	addr     string
	mu       sync.Mutex
	got      []capturedHook
	failNext int // respond 500 to this many requests
}

type capturedHook struct {
	event     string
	delivery  string
	signature string
	body      []byte
}

func startHookReceiver(t *testing.T) *hookReceiver {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	h := &hookReceiver{addr: ln.Addr().String()}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.failNext > 0 {
			h.failNext--
			w.WriteHeader(500)
			return
		}
		h.got = append(h.got, capturedHook{
			event:     r.Header.Get("X-Gitbay-Event"),
			delivery:  r.Header.Get("X-Gitbay-Delivery"),
			signature: r.Header.Get("X-Gitbay-Signature-256"),
			body:      body,
		})
		w.WriteHeader(204)
	}))
	return h
}

func (h *hookReceiver) waitN(t *testing.T, n int) []capturedHook {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.got) >= n {
			out := append([]capturedHook(nil), h.got...)
			h.mu.Unlock()
			return out
		}
		h.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("only %d deliveries arrived, want %d", len(h.got), n)
	return nil
}

func TestWebhooks(t *testing.T) {
	inst := startInstanceWith(t, "[webhooks]\nallow_local = true\n")
	// Restart the daemon with a fast retry base for the failure tests.
	inst.proc.Process.Kill()
	inst.proc.Wait()
	inst.proc = exec.Command(inst.gitbayd, "--config", inst.config, "serve")
	inst.proc.Env = append(os.Environ(), "GITBAY_WEBHOOK_RETRY_BASE=500ms")
	inst.proc.Stderr = os.Stderr
	if err := inst.proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.proc.Process.Kill(); inst.proc.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inst.port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not restart")
		}
		time.Sleep(50 * time.Millisecond)
	}

	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/proj"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	recv := startHookReceiver(t)
	hookURL := "http://" + recv.addr + "/hook"
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"webhook", "add", "alice/proj", hookURL, "--secret", "s3cret"); code != 0 {
		t.Fatalf("webhook add: %s", errOut)
	}

	// An issue event arrives, signed and shaped.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/proj", "--title", "'hook me'"); code != 0 {
		t.Fatal("issue create failed")
	}
	got := recv.waitN(t, 1)
	h := got[0]
	if h.event != "issue.created" || h.delivery == "" {
		t.Fatalf("delivery headers: %+v", h)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(h.body)
	if h.signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("HMAC mismatch: %s", h.signature)
	}
	var p struct {
		Event string `json:"event"`
		Repo  string `json:"repo"`
		Actor string `json:"actor"`
		Data  struct {
			Number int `json:"number"`
		} `json:"data"`
	}
	if err := json.Unmarshal(h.body, &p); err != nil {
		t.Fatalf("payload: %v\n%s", err, h.body)
	}
	if p.Repo != "alice/proj" || p.Actor != "alice" || p.Data.Number != 1 {
		t.Fatalf("payload fields: %+v", p)
	}

	// Push events flow through the hook chain.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/proj"), "w")
	dir := work + "/w"
	os.WriteFile(dir+"/f.txt", []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "push event")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	got = recv.waitN(t, 2)
	push := got[1]
	if push.event != "push" || !strings.Contains(string(push.body), `"ref":"refs/heads/main"`) {
		t.Fatalf("push event: %s %s", push.event, push.body)
	}

	// Event filters: a hook subscribed to mr.created ignores issues.
	recv2 := startHookReceiver(t)
	if _, _, code := inst.ssh(t, aliceKey, "",
		"webhook", "add", "alice/proj", "http://"+recv2.addr+"/", "--events", "mr.created"); code != 0 {
		t.Fatal("filtered webhook add failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/proj", "--title", "'no hook'"); code != 0 {
		t.Fatal("issue 2 failed")
	}
	got = recv.waitN(t, 3) // unfiltered hook sees it
	if got[2].event != "issue.created" {
		t.Fatalf("third delivery: %s", got[2].event)
	}
	time.Sleep(500 * time.Millisecond)
	recv2.mu.Lock()
	if len(recv2.got) != 0 {
		t.Fatalf("filtered hook received %d deliveries", len(recv2.got))
	}
	recv2.mu.Unlock()

	// Retries: fail twice, then succeed; attempts recorded.
	recv.mu.Lock()
	recv.failNext = 2
	recv.mu.Unlock()
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "close", "alice/proj", "1"); code != 0 {
		t.Fatal("close failed")
	}
	got = recv.waitN(t, 4)
	if got[3].event != "issue.closed" {
		t.Fatalf("retried event: %s", got[3].event)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "webhook", "deliveries", "alice/proj", "--json")
	if !strings.Contains(out, `"attempts":3`) {
		t.Fatalf("retry attempts not recorded:\n%s", out)
	}

	// Dead-letter after max attempts, then manual redelivery revives it.
	recv.mu.Lock()
	recv.failNext = 99
	recv.mu.Unlock()
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "reopen", "alice/proj", "1"); code != 0 {
		t.Fatal("reopen failed")
	}
	var deadID string
	deadlineDL := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadlineDL) {
		out, _, _ = inst.ssh(t, aliceKey, "", "webhook", "deliveries", "alice/proj", "--json")
		var envl struct {
			Data []struct {
				ID     int64  `json:"id"`
				Event  string `json:"event"`
				Status string `json:"status"`
			} `json:"data"`
		}
		json.Unmarshal([]byte(out), &envl)
		for _, d := range envl.Data {
			if d.Event == "issue.open" && d.Status == "failed" {
				deadID = fmt.Sprint(d.ID)
			}
		}
		if deadID != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if deadID == "" {
		t.Fatalf("delivery never dead-lettered:\n%s", out)
	}
	recv.mu.Lock()
	recv.failNext = 0
	prev := len(recv.got)
	recv.mu.Unlock()
	if _, errOut, code := inst.ssh(t, aliceKey, "", "webhook", "redeliver", "alice/proj", deadID); code != 0 {
		t.Fatalf("redeliver: %s", errOut)
	}
	recv.waitN(t, prev+1)

	// SSRF: on a default instance (allow_local off), local targets are
	// rejected at add time.
	inst2 := startInstance(t)
	k2 := inst2.newKey(t, "a2")
	inst2.admin(t, "admin", "user", "create", "a2", "--key", k2+".pub")
	if _, _, code := inst2.ssh(t, k2, "", "repo", "create", "a2/r"); code != 0 {
		t.Fatal("repo create failed")
	}
	_, errOut, code := inst2.ssh(t, k2, "", "webhook", "add", "a2/r", "http://127.0.0.1:9/x")
	if code != 2 || !strings.Contains(errOut, "SSRF") {
		t.Fatalf("local webhook target accepted: exit %d, %s", code, errOut)
	}
	if _, _, code := inst2.ssh(t, k2, "", "webhook", "add", "a2/r", "ftp://example.com/x"); code != 2 {
		t.Fatal("non-http scheme accepted")
	}
}
