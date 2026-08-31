package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	merged := inst.mrShow(t, aliceKey, "alice/lib", "1")
	if merged.State != "merged" {
		t.Fatalf("MR state after web merge: %s", merged.State)
	}
	// Who merged it and when, so the page can stop saying alice wants to.
	if merged.MergedBy != "alice" || merged.MergedAt == "" {
		t.Fatalf("merge not attributed: %+v", merged)
	}
	if merged.Reviews[0].CreatedAt == "" {
		t.Fatalf("review carries no timestamp: %+v", merged.Reviews[0])
	}
	_, body = browserGet(t, alice, mrURL)
	if strings.Contains(body, "wants to merge") {
		t.Fatalf("merged MR still wants to merge:\n%s", body)
	}
	if !strings.Contains(body, ">alice</a> merged") {
		t.Fatalf("merged MR does not name the merger:\n%s", body)
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

// TestMRWebCreate opens a merge request from the browser and checks the
// form survives a refusal with the draft intact.
func TestMRWebCreate(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "topic")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "topic work")
	mustGit(t, dir, env, "push", "-q", "origin", "topic")

	alice := inst.login(t, aliceKey)
	base := inst.base() + "/alice/lib"

	// The list links to the form, and the form offers the pushed branches.
	if _, body := browserGet(t, alice, base+"/mrs"); !strings.Contains(body, "/alice/lib/mrs/new") {
		t.Fatalf("no create link on the list:\n%s", body)
	}
	_, form := browserGet(t, alice, base+"/mrs/new")
	for _, want := range []string{`name="source"`, `value="topic"`, `value="main"`} {
		if !strings.Contains(form, want) {
			t.Fatalf("form missing %q:\n%s", want, form)
		}
	}

	// A refusal keeps the draft: the branch does not exist.
	_, retry := browserPost(t, alice, base+"/mrs/new", url.Values{
		"source": {"nope"}, "target": {"main"}, "title": {"my title"}, "body": {"my body"}})
	if !strings.Contains(retry, `class="error"`) || !strings.Contains(retry, "my title") ||
		!strings.Contains(retry, "my body") {
		t.Fatalf("refusal lost the draft:\n%s", retry)
	}

	// A real one lands on the merge request it created.
	status, created := browserPost(t, alice, base+"/mrs/new", url.Values{
		"source": {"topic"}, "target": {"main"}, "title": {"topic into main"}, "body": {"please review"}})
	if status != 200 || !strings.Contains(created, "topic into main") {
		t.Fatalf("create failed: %d\n%s", status, created)
	}
	show := inst.mrShow(t, aliceKey, "alice/lib", "1")
	if show.State != "open" || show.Source != "topic" {
		t.Fatalf("created MR wrong: %+v", show)
	}
}

// TestMRWebDiffThreads opens a review thread on a diff line and replies to
// it from the browser. The CLI's view of the threads afterwards is what
// proves the page dispatched mr diff-comment rather than writing its own
// rows.
func TestMRWebDiffThreads(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n}\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add greeting")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/lib",
		"--source", "feat", "--target", "main", "--title", "'greeting'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	mrURL := inst.base() + "/alice/lib/mrs/1"
	alice := inst.login(t, aliceKey)

	// The gutter carries the handle, and following it renders the form
	// anchored to that line. There is no JavaScript, so the anchor has to
	// survive a round trip in the query.
	_, body := browserGet(t, alice, mrURL+"?view=diff")
	if !strings.Contains(body, "cpath=main.go&amp;cline=6&amp;cside=new") {
		t.Fatalf("no comment handle in the diff gutter:\n%s", body)
	}
	_, body = browserGet(t, alice, mrURL+"?view=diff&cpath=main.go&cline=6&cside=new")
	if !strings.Contains(body, `id="compose"`) || !strings.Contains(body, `name="line" value="6"`) {
		t.Fatalf("compose form not rendered:\n%s", body)
	}

	if status, _ := browserPost(t, alice, mrURL+"/diff-comment", url.Values{
		"path": {"main.go"}, "line": {"6"}, "side": {"new"},
		"body": {"use log instead of fmt"}}); status != 200 {
		t.Fatalf("open thread: %d", status)
	}
	threads := inst.mrThreads(t, aliceKey, "alice/lib", "1")
	if len(threads) != 1 || threads[0].Path != "main.go" || threads[0].Line != 6 {
		t.Fatalf("thread not anchored: %+v", threads)
	}
	if len(threads[0].Comments) != 1 || threads[0].Comments[0].Body != "use log instead of fmt" {
		t.Fatalf("comment body not stored: %+v", threads[0].Comments)
	}

	// The rendered thread offers reply and resolve to its author.
	_, body = browserGet(t, alice, mrURL+"?view=diff")
	if !strings.Contains(body, `name="reply" value="`+strconv.FormatInt(threads[0].ID, 10)+`"`) {
		t.Fatalf("no reply form on the thread:\n%s", body)
	}
	if !strings.Contains(body, `value="resolve"`) {
		t.Fatalf("no resolve control on the thread:\n%s", body)
	}

	if status, _ := browserPost(t, alice, mrURL+"/diff-comment", url.Values{
		"reply": {strconv.FormatInt(threads[0].ID, 10)}, "body": {"agreed, switching"}}); status != 200 {
		t.Fatalf("reply: %d", status)
	}
	threads = inst.mrThreads(t, aliceKey, "alice/lib", "1")
	if len(threads) != 1 || len(threads[0].Comments) != 2 ||
		threads[0].Comments[1].Body != "agreed, switching" {
		t.Fatalf("reply not on the thread: %+v", threads)
	}

	// An empty body is refused, and says so on the page it returns to.
	_, body = browserPost(t, alice, mrURL+"/diff-comment", url.Values{
		"reply": {strconv.FormatInt(threads[0].ID, 10)}, "body": {"  "}})
	if !strings.Contains(body, "empty comment") {
		t.Fatalf("empty reply not refused:\n%s", body)
	}
}

// mrThread is one review thread as mr threads --json reports it.
type mrThread struct {
	ID       int64  `json:"id"`
	Path     string `json:"path"`
	Side     string `json:"side"`
	Line     int64  `json:"line"`
	Comments []struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	} `json:"comments"`
}

// mrThreads reads the review threads on a merge request over SSH.
func (i *instance) mrThreads(t *testing.T, key, repo, n string) []mrThread {
	t.Helper()
	out, errOut, code := i.ssh(t, key, "", "mr", "threads", repo, n, "--json")
	if code != 0 {
		t.Fatalf("mr threads: %s", errOut)
	}
	var env struct {
		Data []mrThread `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("mr threads json: %v", err)
	}
	return env.Data
}
