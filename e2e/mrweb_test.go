package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// login returns a browser holding a session for the given key's account.
func (i *instance) login(t *testing.T, key string) *http.Client {
	t.Helper()
	out, errOut, code := i.ssh(t, key, "", "web", "login", "--json")
	if code != 0 {
		t.Fatalf("web login: %s", errOut)
	}
	var env struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	c := newBrowser(t)
	path := env.Data.URL[strings.Index(env.Data.URL, "/login"):]
	if status, _ := browserGet(t, c, i.base()+path); status != 200 {
		t.Fatalf("login landed: %d", status)
	}
	return c
}

// TestMRWebReviewLoop drives review, thread resolution, and merge from the
// browser. Every action runs the same control command the CLI runs, so the
// test also proves the merge gates apply to web merges.
func TestMRWebReviewLoop(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/lib", "bob", "write"); code != 0 {
		t.Fatalf("grant: %s", errOut)
	}
	// Unresolved review threads block merges, so the gate is observable.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-resolved", "alice/lib", "on"); code != 0 {
		t.Fatalf("require-resolved: %s", errOut)
	}

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("v1\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Bob proposes a change and leaves a review thread on it.
	bobEnv := inst.gitEnv(bobKey)
	bobWork := t.TempDir()
	mustGit(t, bobWork, bobEnv, "clone", inst.sshURL("alice/lib"), "w")
	bobDir := filepath.Join(bobWork, "w")
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "feature", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "feature.txt"), []byte("bob's work\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "add feature")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "feature")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "feature", "--target", "main", "--title", "'add feature'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "diff-comment", "alice/lib", "1",
		"--path", "feature.txt", "--line", "1", "--message", "'is this right?'"); code != 0 {
		t.Fatalf("diff-comment: %s", errOut)
	}

	mrURL := inst.base() + "/alice/lib/mrs/1"
	alice := inst.login(t, aliceKey)

	// The controls are on the page, and carry the thread to resolve.
	_, body := browserGet(t, alice, mrURL)
	for _, want := range []string{`value="approve"`, `action="/alice/lib/mrs/1/merge"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("MR page missing %q", want)
		}
	}
	// Review threads live on the diff view, where their lines are.
	_, diffBody := browserGet(t, alice, mrURL+"?view=diff")
	m := regexp.MustCompile(`name="thread" value="(\d+)"`).FindStringSubmatch(diffBody)
	if m == nil {
		t.Fatalf("no thread control on the diff view:\n%s", diffBody)
	}
	threadID := m[1]

	// Approve from the browser; the CLI sees the review.
	if status, _ := browserPost(t, alice, mrURL+"/review", url.Values{"verdict": {"approve"}}); status != 200 {
		t.Fatalf("review post: %d", status)
	}
	show := inst.mrShow(t, aliceKey, "alice/lib", "1")
	if len(show.Reviews) != 1 || show.Reviews[0].Reviewer != "alice" || show.Reviews[0].Verdict != "approve" {
		t.Fatalf("review not recorded: %+v", show.Reviews)
	}

	// Merging is refused while the thread is open, and the page says why.
	_, body = browserPost(t, alice, mrURL+"/merge", url.Values{"strategy": {"auto"}})
	if !strings.Contains(body, "unresolved") {
		t.Fatalf("merge gate not surfaced:\n%s", body)
	}
	if st := inst.mrShow(t, aliceKey, "alice/lib", "1").State; st != "open" {
		t.Fatalf("blocked merge changed state to %s", st)
	}

	// Resolve the thread, then merge.
	if status, _ := browserPost(t, alice, mrURL+"/thread",
		url.Values{"thread": {threadID}, "action": {"resolve"}}); status != 200 {
		t.Fatalf("resolve post: %d", status)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "mr", "threads", "alice/lib", "1")
	if !strings.Contains(out, "resolved") {
		t.Fatalf("thread not resolved:\n%s", out)
	}
	if status, _ := browserPost(t, alice, mrURL+"/merge", url.Values{"strategy": {"auto"}}); status != 200 {
		t.Fatalf("merge post: %d", status)
	}
	if st := inst.mrShow(t, aliceKey, "alice/lib", "1").State; st != "merged" {
		t.Fatalf("MR state after web merge: %s", st)
	}
	mustGit(t, dir, env, "pull", "-q", "origin", "main")
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); err != nil {
		t.Fatal("merged content missing from main")
	}

	// Readers get no controls, and a forged POST is refused by the command.
	_, anon := browserGet(t, newBrowser(t), mrURL)
	if strings.Contains(anon, `value="approve"`) {
		t.Fatal("anonymous visitor sees review controls")
	}
	carol := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carol+".pub")
	if _, errOut, code := inst.ssh(t, carol, "", "repo", "create", "carol/own"); code != 0 {
		t.Fatalf("carol repo: %s", errOut)
	}
	_, denied := browserPost(t, inst.login(t, carol), mrURL+"/close", url.Values{})
	if !strings.Contains(denied, `class="error"`) || !strings.Contains(denied, "write access") {
		t.Fatalf("reader was not refused:\n%s", denied)
	}
}
