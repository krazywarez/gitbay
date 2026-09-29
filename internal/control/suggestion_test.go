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
