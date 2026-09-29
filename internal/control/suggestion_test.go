package control

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// suggestFixture is a queueFixture whose feature branch carries a
// multi-line file, lib.txt, for suggestions to anchor in.
func newSuggestFixture(t *testing.T, set func(*store.RepoSettings)) *queueFixture {
	t.Helper()
	f := newQueueFixture(t, set)
	f.write("lib.txt", "one\ntwo\nthree\nfour\nfive\n")
	f.write("dos.txt", "a\r\nb\r\nc\r\n")
	f.write("tail.txt", "x\nlast")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "lib")
	f.moveHead()
	return f
}

// moveHead pushes the fixture's feature branch to the bare repository
// and points the merge request at it, as post-receive would.
func (f *queueFixture) moveHead() {
	f.t.Helper()
	f.git(f.src, "push", "-q", "--force", f.dir, "feature")
	f.headSHA = strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.dir, "update-ref", mrHeadRef(1), f.headSHA)
	if err := f.st.UpdateMRHead(f.mr().ID, f.headSHA, f.targetSH, false); err != nil {
		f.t.Fatal(err)
	}
}

// suggest opens a thread on lib.txt start-end with a suggestion block
// holding lines, returning the thread id.
func (f *queueFixture) suggest(u store.User, path string, start, end int, lines ...string) string {
	f.t.Helper()
	body := "try this\n```suggestion\n" + strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	body += "```\n"
	var out, errOut strings.Builder
	c := &Ctx{User: u, Scope: "full", Store: f.st, Stdout: &out, Stderr: &errOut, JSON: true,
		Stdin: strings.NewReader(body)}
	c.Cfg.Server.Root = f.root
	c.Cfg.Limits.WriteRate = -1
	argv := []string{"mr", "diff-comment", f.repo.Path(), "1", "--path", path,
		"--start-line", strconv.Itoa(start), "--line", strconv.Itoa(end), "--file", "-"}
	if code := Dispatch(c, argv); code != protocol.ExitOK {
		f.t.Fatalf("diff-comment: exit %d, %s", code, errOut.String())
	}
	var env struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out.String()), &env); err != nil {
		f.t.Fatal(err)
	}
	return strconv.Itoa(int(env.Data.Thread))
}

type threadJSON struct {
	ID         int64          `json:"id"`
	StartLine  int64          `json:"start_line"`
	Line       int64          `json:"line"`
	Resolved   string         `json:"resolved_by"`
	Suggestion *SuggestionOut `json:"suggestion"`
}

// unlimited lifts the per-account write limit, which these tests would
// otherwise spend for every test in the package that writes as uid 1.
func unlimited(c *Ctx) { c.Cfg.Limits.WriteRate = -1 }

func (f *queueFixture) mustWrite(u store.User, argv ...string) string {
	f.t.Helper()
	code, out, errOut := f.runWith(u, unlimited, argv...)
	if code != protocol.ExitOK {
		f.t.Fatalf("%v: exit %d, %s", argv, code, errOut)
	}
	return out
}

func (f *queueFixture) threads(u store.User) []threadJSON {
	f.t.Helper()
	out := f.mustRun(u, "mr", "threads", f.repo.Path(), "1", "--json")
	var env struct {
		Data []threadJSON `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		f.t.Fatalf("threads JSON: %v\n%s", err, out)
	}
	return env.Data
}

func (f *queueFixture) suggestion(u store.User, thread string) *SuggestionOut {
	f.t.Helper()
	for _, th := range f.threads(u) {
		if strconv.Itoa(int(th.ID)) == thread {
			return th.Suggestion
		}
	}
	f.t.Fatalf("no thread %s", thread)
	return nil
}

// A suggestion over a range reads back from mr threads as structure:
// the anchor, the commit and blob it was made against, the lines it
// replaces and the replacement.
func TestSuggestionInThreads(t *testing.T) {
	f := newSuggestFixture(t, nil)
	id := f.suggest(f.alice, "lib.txt", 2, 3, "TWO", "THREE", "extra")
	s := f.suggestion(f.alice, id)
	if s == nil {
		t.Fatal("thread carries no suggestion")
	}
	blob := strings.TrimSpace(f.git(f.dir, "rev-parse", f.headSHA+":lib.txt"))
	want := SuggestionOut{Path: "lib.txt", StartLine: 2, EndLine: 3, Commit: f.headSHA, Blob: blob,
		Original: "two\nthree\n", Replacement: "TWO\nTHREE\nextra\n", Apply: "server"}
	if *s != want {
		t.Fatalf("suggestion = %+v\nwant %+v", *s, want)
	}
	text := f.mustRun(f.alice, "mr", "threads", f.repo.Path(), "1")
	for _, w := range []string{"lib.txt:2-3", "- two", "+ THREE", "mr apply-suggestion alice/app 1 " + id} {
		if !strings.Contains(text, w) {
			t.Errorf("threads text lacks %q:\n%s", w, text)
		}
	}
	if strings.Contains(text, "```suggestion") {
		t.Errorf("threads text repeats the raw block:\n%s", text)
	}
}

// The suggestion goes stale when the lines it replaces change, and not
// when the file changes elsewhere.
func TestSuggestionOutdated(t *testing.T) {
	f := newSuggestFixture(t, nil)
	id := f.suggest(f.alice, "lib.txt", 2, 2, "TWO")
	f.write("lib.txt", "one\ntwo\nthree\nfour\nFIVE\n")
	f.git(f.src, "commit", "-q", "-am", "elsewhere")
	f.moveHead()
	if s := f.suggestion(f.alice, id); s.Outdated {
		t.Fatalf("a change below the range outdated the suggestion: %+v", s)
	}
	f.write("lib.txt", "zero\none\ntwo\nthree\nfour\nFIVE\n")
	f.git(f.src, "commit", "-q", "-am", "shift")
	f.moveHead()
	if s := f.suggestion(f.alice, id); !s.Outdated || s.Reason != reasonChanged {
		t.Fatalf("suggestion after its lines moved = %+v, want outdated", s)
	}
	f.git(f.src, "rm", "-q", "lib.txt")
	f.git(f.src, "commit", "-q", "-m", "gone")
	f.moveHead()
	if s := f.suggestion(f.alice, id); !s.Outdated || s.Reason != reasonGone {
		t.Fatalf("suggestion on a deleted file = %+v, want outdated", s)
	}
}

// On a repository requiring signed commits the server does not commit a
// suggestion, and the thread says the CLI applies it locally.
func TestSuggestionSignedIsLocal(t *testing.T) {
	f := newSuggestFixture(t, func(s *store.RepoSettings) { s.RequireSignedCommits = true })
	id := f.suggest(f.alice, "lib.txt", 1, 1, "ONE")
	if s := f.suggestion(f.alice, id); s.Apply != "local" {
		t.Fatalf("apply = %q, want local", s.Apply)
	}
}

// What diff-comment refuses before storing a suggestion it could never
// apply.
func TestSuggestionRefusals(t *testing.T) {
	f := newSuggestFixture(t, nil)
	id := f.suggest(f.alice, "lib.txt", 1, 1, "ONE")
	run := func(body string, extra ...string) (int, string) {
		var out, errOut strings.Builder
		c := &Ctx{User: f.alice, Scope: "full", Store: f.st, Stdout: &out, Stderr: &errOut,
			Stdin: strings.NewReader(body)}
		c.Cfg.Server.Root = f.root
	c.Cfg.Limits.WriteRate = -1
		return Dispatch(c, append([]string{"mr", "diff-comment", f.repo.Path(), "1", "--file", "-"}, extra...)), errOut.String()
	}
	block := "```suggestion\nx\n```\n"
	cases := []struct {
		name string
		body string
		args []string
		want string
	}{
		{"old side", block, []string{"--path", "lib.txt", "--line", "1", "--old"}, "drop --old"},
		{"reply", block, []string{"--reply", id}, "its own thread"},
		{"past the end", block, []string{"--path", "lib.txt", "--start-line", "5", "--line", "6"}, "no lines 5-6"},
		{"unclosed", "```suggestion\nx\n", []string{"--path", "lib.txt", "--line", "1"}, "not closed"},
		{"start after line", "plain", []string{"--path", "lib.txt", "--start-line", "3", "--line", "2"}, "no greater than --line"},
	}
	for _, c := range cases {
		code, errOut := run(c.body, c.args...)
		if code != protocol.ExitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: exit %d %q, want usage with %q", c.name, code, errOut, c.want)
		}
	}
}

// verified gives u a verified primary address, which a commit needs.
func (f *queueFixture) verified(u store.User) {
	f.t.Helper()
	if err := f.st.AddEmail(u.ID, u.Username+"@example.test", "admin", true); err != nil {
		f.t.Fatal(err)
	}
}

// applyOK applies thread as u and returns the new commit, checking what
// every successful apply must have done: one commit on the old head by
// u, naming the merge request and thread, the merge request moved to
// it, and the thread resolved.
func (f *queueFixture) applyOK(u store.User, thread string) string {
	f.t.Helper()
	old := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/feature"))
	out := f.mustWrite(u, "mr", "apply-suggestion", f.repo.Path(), "1", thread, "--json")
	var env struct {
		Data struct {
			SHA string `json:"sha"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	sha := env.Data.SHA
	if tip := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/feature")); tip != sha || sha == "" {
		f.t.Fatalf("feature = %s, apply reported %q", tip, sha)
	}
	if parent := strings.TrimSpace(f.git(f.dir, "rev-parse", sha+"^")); parent != old {
		f.t.Fatalf("parent = %s, want the old head %s", parent, old)
	}
	meta := f.git(f.dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%B", sha)
	who := u.Username + " <" + u.Username + "@example.test>"
	if !strings.HasPrefix(meta, who+"|"+who+"|") || !strings.Contains(meta, "Thread "+thread+" on alice/app!1") {
		f.t.Fatalf("commit = %q", meta)
	}
	if mr := f.mr(); mr.HeadSHA != sha {
		f.t.Fatalf("MR head = %s, want %s", mr.HeadSHA, sha)
	}
	if head := strings.TrimSpace(f.git(f.dir, "rev-parse", mrHeadRef(1))); head != sha {
		f.t.Fatalf("MR head ref = %s, want %s", head, sha)
	}
	for _, th := range f.threads(u) {
		if strconv.Itoa(int(th.ID)) == thread && th.Resolved != u.Username {
			f.t.Fatalf("thread %s resolved by %q, want %s", thread, th.Resolved, u.Username)
		}
	}
	return sha
}

func (f *queueFixture) file(sha, path string) string {
	f.t.Helper()
	return f.git(f.dir, "show", sha+":"+path)
}

// Each shape of range: several lines to more, deletion, the last line of
// a file with no final newline, and a CRLF file.
func TestApplySuggestion(t *testing.T) {
	f := newSuggestFixture(t, nil)
	f.verified(f.alice)
	cases := []struct {
		path       string
		start, end int
		lines      []string
		want       string
	}{
		{"lib.txt", 2, 3, []string{"TWO", "THREE", "3.5"}, "one\nTWO\nTHREE\n3.5\nfour\nfive\n"},
		{"lib.txt", 5, 6, nil, "one\nTWO\nTHREE\n3.5\n"},
		{"tail.txt", 2, 2, []string{"LAST", "more"}, "x\nLAST\nmore"},
		{"dos.txt", 2, 2, []string{"B", "B2"}, "a\r\nB\r\nB2\r\nc\r\n"},
	}
	for _, c := range cases {
		id := f.suggest(f.alice, c.path, c.start, c.end, c.lines...)
		sha := f.applyOK(f.alice, id)
		if got := f.file(sha, c.path); got != c.want {
			t.Errorf("%s %d-%d: file = %q, want %q", c.path, c.start, c.end, got, c.want)
		}
	}
}

// A suggestion whose lines changed, or whose file is gone, is refused;
// so is one on a repository requiring signed commits, with the command
// that applies it locally.
func TestApplySuggestionRefusals(t *testing.T) {
	f := newSuggestFixture(t, nil)
	f.verified(f.alice)
	stale := f.suggest(f.alice, "lib.txt", 1, 1, "ONE")
	gone := f.suggest(f.alice, "tail.txt", 1, 1, "X")
	plain := f.suggest(f.alice, "lib.txt", 2, 2, "two")
	f.write("lib.txt", "uno\ntwo\nthree\nfour\nfive\n")
	f.git(f.src, "rm", "-q", "tail.txt")
	f.git(f.src, "commit", "-q", "-am", "moved on")
	f.moveHead()
	cases := []struct {
		thread string
		code   int
		want   string
	}{
		{stale, protocol.ExitUsage, "outdated: " + reasonChanged},
		{gone, protocol.ExitUsage, reasonGone},
		{plain, protocol.ExitUsage, "changes nothing"},
		{"999", protocol.ExitNotFound, "no thread 999"},
	}
	for _, c := range cases {
		code, _, errOut := f.runWith(f.alice, unlimited, "mr", "apply-suggestion", f.repo.Path(), "1", c.thread)
		if code != c.code || !strings.Contains(errOut, c.want) {
			t.Errorf("thread %s: exit %d %q, want %d with %q", c.thread, code, errOut, c.code, c.want)
		}
	}

	if _, err := f.st.UpdateRepoSettings(f.repo.ID, func(s *store.RepoSettings) { s.RequireSignedCommits = true }); err != nil {
		t.Fatal(err)
	}
	ok := f.suggest(f.alice, "lib.txt", 2, 2, "TWO")
	code, _, errOut := f.runWith(f.alice, unlimited, "mr", "apply-suggestion", f.repo.Path(), "1", ok)
	if code != protocol.ExitDenied || !strings.Contains(errOut, "gitbay mr apply-suggestion alice/app 1 "+ok) {
		t.Fatalf("signed repo: exit %d %q", code, errOut)
	}
}

// The update is held to the pre-receive ref policy a push is: a source
// branch that is protected under require-mr refuses it.
func TestApplySuggestionHonoursRefPolicy(t *testing.T) {
	f := newSuggestFixture(t, func(s *store.RepoSettings) {
		s.ProtectedBranches = []string{"feature"}
		s.RequireMR = true
	})
	f.verified(f.alice)
	id := f.suggest(f.alice, "lib.txt", 1, 1, "ONE")
	before := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/feature"))
	code, _, errOut := f.runWith(f.alice, unlimited, "mr", "apply-suggestion", f.repo.Path(), "1", id)
	if code != protocol.ExitDenied || !strings.Contains(errOut, "merge requests only") {
		t.Fatalf("exit %d %q, want the require-mr refusal", code, errOut)
	}
	if after := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/feature")); after != before {
		t.Fatal("refused apply moved the branch")
	}
}

// Only the source branch's writers apply: a reader cannot. A thread in
// an unsubmitted review is not applied, and to anyone but its author it
// does not exist.
func TestApplySuggestionNeedsWrite(t *testing.T) {
	f := newSuggestFixture(t, nil)
	carol := f.user("carol", "read")
	f.verified(carol)
	id := f.suggest(f.alice, "lib.txt", 1, 1, "ONE")
	code, _, errOut := f.runWith(carol, unlimited, "mr", "apply-suggestion", f.repo.Path(), "1", id)
	if code != protocol.ExitDenied || !strings.Contains(errOut, "only its writers") {
		t.Fatalf("reader: exit %d %q", code, errOut)
	}

	var out, stderr strings.Builder
	c := &Ctx{User: f.alice, Scope: "full", Store: f.st, Stdout: &out, Stderr: &stderr, JSON: true,
		Stdin: strings.NewReader("```suggestion\nONE\n```\n")}
	c.Cfg.Server.Root = f.root
	c.Cfg.Limits.WriteRate = -1
	if code := Dispatch(c, []string{"mr", "diff-comment", f.repo.Path(), "1", "--path", "lib.txt", "--line", "1", "--pending", "--file", "-"}); code != protocol.ExitOK {
		t.Fatalf("pending diff-comment: %s", stderr.String())
	}
	var env struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out.String()), &env)
	pending := strconv.Itoa(int(env.Data.Thread))
	if code, _, errOut := f.runWith(f.alice, unlimited, "mr", "apply-suggestion", f.repo.Path(), "1", pending); code != protocol.ExitUsage || !strings.Contains(errOut, "unsubmitted") {
		t.Errorf("own pending thread: exit %d %q", code, errOut)
	}
	if code, _, _ := f.runWith(carol, unlimited, "mr", "apply-suggestion", f.repo.Path(), "1", pending); code != protocol.ExitNotFound {
		t.Errorf("someone else's pending thread: exit %d, want not found", code)
	}
}

// Applying is a push by the applier, so a queued merge sees it: a
// writer's apply keeps the queue, and resolving the thread it came from
// lets the queued merge land on the new head.
func TestApplySuggestionReachesQueuedMerge(t *testing.T) {
	f := newSuggestFixture(t, func(s *store.RepoSettings) { s.RequireResolved = true })
	f.verified(f.alice)
	id := f.suggest(f.alice, "lib.txt", 1, 1, "ONE")
	f.mustWrite(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.wantQueued("threads resolved")
	sha := f.applyOK(f.alice, id)
	f.wantMergedBy("alice")
	if main := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/main")); main != sha {
		t.Fatalf("main = %s, want the applied commit %s", main, sha)
	}
}

// On a merge request from a fork the source branch is the fork's, so its
// writers apply and the target's do not. The fork writer's apply is a
// push by someone who cannot merge into the target, which dequeues a
// merge queued there.
func TestApplySuggestionFork(t *testing.T) {
	f := newSuggestFixture(t, nil)
	bobID, err := f.st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := f.st.UserByID(bobID)
	f.verified(f.alice)
	f.verified(bob)
	forkID, err := f.st.CreateRepo("user", bobID, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	fork, _ := f.st.RepoByID(forkID)
	forkDir := RepoDir(f.root, fork.OwnerName, fork.Name)
	f.git(f.root, "clone", "-q", "--bare", f.src, forkDir)
	f.git(f.dir, "update-ref", mrHeadRef(2), f.headSHA)
	if _, err := f.st.CreateMR(f.repo.ID, bobID, forkID, "feature", "main", "forked", "", f.headSHA, "md", false); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	c := &Ctx{User: f.alice, Scope: "full", Store: f.st, Stdout: &out, Stderr: &errOut, JSON: true,
		Stdin: strings.NewReader("```suggestion\nONE\n```\n")}
	c.Cfg.Server.Root = f.root
	c.Cfg.Limits.WriteRate = -1
	if code := Dispatch(c, []string{"mr", "diff-comment", f.repo.Path(), "2", "--path", "lib.txt", "--line", "1", "--file", "-"}); code != protocol.ExitOK {
		t.Fatalf("diff-comment: %s", errOut.String())
	}
	var env struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out.String()), &env)
	thread := strconv.Itoa(int(env.Data.Thread))

	code, _, stderr := f.runWith(f.alice, unlimited, "mr", "apply-suggestion", f.repo.Path(), "2", thread)
	if code != protocol.ExitDenied || !strings.Contains(stderr, "bob/app:feature") {
		t.Fatalf("target owner on a fork's branch: exit %d %q", code, stderr)
	}

	if _, err := f.st.UpdateRepoSettings(f.repo.ID, func(s *store.RepoSettings) { s.RequireApprovals = 1 }); err != nil {
		t.Fatal(err)
	}
	f.mustWrite(f.alice, "mr", "merge", f.repo.Path(), "2", "--when-ready", "--strategy", "merge")
	f.mustWrite(bob, "mr", "apply-suggestion", f.repo.Path(), "2", thread)
	tip := strings.TrimSpace(f.git(forkDir, "rev-parse", "refs/heads/feature"))
	if got := f.git(forkDir, "show", tip+":lib.txt"); !strings.HasPrefix(got, "ONE\ntwo\n") {
		t.Fatalf("fork's lib.txt = %q", got)
	}
	mr, _ := f.st.MRByNumber(f.repo.ID, 2)
	if mr.HeadSHA != tip {
		t.Fatalf("MR head = %s, want the fork's new tip %s", mr.HeadSHA, tip)
	}
	if mr.QueuedAt != "" || mr.State != "open" {
		t.Fatalf("queued merge after the fork writer's apply: state %s queued %q", mr.State, mr.QueuedAt)
	}
	cs, _ := f.st.ListMRComments(mr.ID)
	said := false
	for _, c := range cs {
		said = said || (c.Kind == "system" && strings.Contains(c.Body, "bob pushed and cannot merge"))
	}
	if !said {
		t.Fatalf("timeline does not say why the merge was dequeued: %+v", cs)
	}
}
