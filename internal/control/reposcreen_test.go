package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestRepoShowPlainPinned(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	c, out, errOut := mrTestCtx(st, store.User{ID: uid, Username: "alice"})
	if code := Dispatch(c, []string{"repo", "show", repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "repo-show", out.String())
}

func TestRepoShowScreen(t *testing.T) {
	c := screenCtx(100, false)
	d := repoShowOut{Path: "krz/gitbay", Description: "A CLI-first git forge.", Visibility: "public", DefaultBranch: "main",
		Topics: []string{"cli", "forge"}}
	g := repoGlance{clone: "ssh://git@gitbay.org/krz/gitbay.git", release: "v1.41.0, 2h ago", checks: "✓ 2/2 on main", mrsN: 1, issuesN: 3}
	mrs := []store.MR{{Number: 552, Title: "wire $PAGER", UpdatedAt: "2026-10-01T11:40:00Z"}}
	issues := []store.Issue{{Number: 12, Title: "Android app", UpdatedAt: "2026-08-21T10:00:00Z"}}
	s := repoShowScreen(c, d, g, mrs, issues, []CommitOut{{"a136534ff9", "changelog: v1.41.0"}})
	if n := sectionCounts(s); n["Open merge requests"] != 1 || n["Open issues"] != 3 || n["Recent commits"] != 1 {
		t.Errorf("sections = %v", n)
	}
	out := renderString(c, s)
	for _, want := range []string{
		"Head:     main  ✓ 2/2\n",
		"A CLI-first git forge.\n",
		"+2 more  gitbay issue list krz/gitbay\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	checkActions(t, s)
}

func TestRepoShowScreenMirrorErrorOnly(t *testing.T) {
	c := screenCtx(100, false)
	d := repoShowOut{Path: "krz/tap", Visibility: "public", DefaultBranch: "main", Mirrors: []repoMirrorOut{
		{Direction: "push", URL: "https://github.com/x/tap.git"},
		{Direction: "push", URL: "https://github.com/x/tap2.git", LastError: "exit status 1"},
	}}
	out := renderString(c, repoShowScreen(c, d, repoGlance{}, nil, nil, nil))
	if strings.Count(out, "Mirror:") != 1 || !strings.Contains(out, "tap2.git: exit status 1") {
		t.Errorf("mirror lines:\n%s", out)
	}
}
