package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// browser is an HTTP client with a cookie jar, standing in for a logged-in
// user's browser.
func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func (i *instance) base() string { return fmt.Sprintf("http://127.0.0.1:%d", i.httpPort) }

func browserGet(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func browserPost(t *testing.T, c *http.Client, u string, form url.Values) (int, string) {
	t.Helper()
	resp, err := c.PostForm(u, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestWebAccounts(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")

	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// A repo with one file to edit.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/site"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/site"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("original\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// SSH-minted login URL.
	out, errOut, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatalf("web login: %s", errOut)
	}
	var env2 struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env2); err != nil {
		t.Fatalf("web login JSON: %v\n%s", err, out)
	}
	// The URL carries the configured site host; rewrite to the test port.
	loginPath := env2.Data.URL[strings.Index(env2.Data.URL, "/login"):]

	browser := newBrowser(t)
	status, body := browserGet(t, browser, inst.base()+loginPath)
	if status != 200 || !strings.Contains(body, `logged in as <a href="/alice">alice</a>`) {
		t.Fatalf("login redirect landed wrong: %d\n%s", status, body)
	}

	// The token is single-use.
	fresh := newBrowser(t)
	_, body = browserGet(t, fresh, inst.base()+loginPath)
	if !strings.Contains(body, "invalid, expired, or already used") {
		t.Fatalf("token reuse not refused:\n%s", body)
	}

	// Create a repo through the web.
	status, _ = browserPost(t, browser, inst.base()+"/new",
		url.Values{"name": {"webborn"}, "visibility": {"private"}})
	if status != 200 {
		t.Fatalf("web repo create: %d", status)
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "repo", "show", "alice/webborn"); code != 0 {
		t.Fatalf("web-created repo missing over ssh: %s", out)
	}

	// Logged-in viewer sees their private repo; anonymous still gets 404.
	if status, _ = browserGet(t, browser, inst.base()+"/alice/webborn"); status != 200 {
		t.Fatalf("owner blocked from private repo page: %d", status)
	}
	if status, _ := inst.get(t, "/alice/webborn"); status != 404 {
		t.Fatalf("anonymous sees private repo: %d", status)
	}

	// File edit: form loads with current content, POST commits.
	status, body = browserGet(t, browser, inst.base()+"/alice/site/edit/main/notes.txt")
	if status != 200 || !strings.Contains(body, "original") {
		t.Fatalf("edit form: %d\n%s", status, body)
	}
	status, _ = browserPost(t, browser, inst.base()+"/alice/site/edit/main/notes.txt",
		url.Values{"content": {"edited from the web\n"}, "message": {"web edit"}})
	if status != 200 {
		t.Fatalf("edit submit: %d", status)
	}

	// The edit is a real commit: authored with the verified email, and it
	// displays as unsigned — the honest outcome for a server-side commit.
	logOut, _, code := inst.ssh(t, aliceKey, "", "repo", "log", "alice/site", "--limit", "1", "--json")
	if code != 0 {
		t.Fatal("repo log failed")
	}
	var logEnv struct {
		Data []struct {
			Subject     string `json:"subject"`
			AuthorEmail string `json:"author_email"`
			Signature   struct {
				State string `json:"state"`
			} `json:"signature"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(logOut), &logEnv); err != nil || len(logEnv.Data) == 0 {
		t.Fatalf("log JSON: %v\n%s", err, logOut)
	}
	tip := logEnv.Data[0]
	if tip.Subject != "web edit" || tip.AuthorEmail != "alice@example.test" || tip.Signature.State != "unsigned" {
		t.Fatalf("web edit commit wrong: %+v", tip)
	}
	if status, body = browserGet(t, browser, inst.base()+"/alice/site/raw/main/notes.txt"); !strings.Contains(body, "edited from the web") {
		t.Fatalf("edited content not served: %d %q", status, body)
	}

	// A require-signed repo refuses web edits instead of violating itself.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-signed", "alice/site", "on"); code != 0 {
		t.Fatal("require-signed failed")
	}
	_, body = browserPost(t, browser, inst.base()+"/alice/site/edit/main/notes.txt",
		url.Values{"content": {"x"}, "message": {"x"}})
	if !strings.Contains(body, "requires signed commits") {
		t.Fatalf("require-signed web edit not refused:\n%s", body)
	}

	// Issue participation through the web.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/site", "--title", "'from ssh'"); code != 0 {
		t.Fatal("issue create failed")
	}
	status, _ = browserPost(t, browser, inst.base()+"/alice/site/issues/1/comment",
		url.Values{"body": {"web comment"}})
	if status != 200 {
		t.Fatalf("web comment: %d", status)
	}
	showOut, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/site", "1")
	if !strings.Contains(showOut, "web comment") {
		t.Fatalf("web comment missing over ssh:\n%s", showOut)
	}

	// Cross-origin POSTs are refused.
	req, _ := http.NewRequest("POST", inst.base()+"/alice/site/issues/1/comment",
		strings.NewReader("body=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin POST: %d, want 403", resp.StatusCode)
	}

	// Logout kills the session.
	if status, _ = browserPost(t, browser, inst.base()+"/logout", url.Values{}); status != 200 {
		t.Fatalf("logout: %d", status)
	}
	if status, _ = browserGet(t, browser, inst.base()+"/alice/webborn"); status != 404 {
		t.Fatalf("session survived logout: %d", status)
	}
}

// TestViewOnlyHasNoLoginOnTheWire is the M8 negative: in view_only mode the
// login route does not exist and web login over ssh is refused.
func TestViewOnlyHasNoLoginOnTheWire(t *testing.T) {
	inst := startInstance(t) // default: view_only
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if status, _ := inst.get(t, "/login"); status != 404 {
		t.Fatalf("view_only /login = %d, want 404", status)
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "web", "login")
	if code != 4 || !strings.Contains(errOut, "view-only") {
		t.Fatalf("web login in view_only: exit %d, %s", code, errOut)
	}
}
