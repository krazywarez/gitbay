package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// orgScreensFixture is alice with a repository carrying a label and a
// milestone, and an org with a label, a milestone, a repository and a
// team that holds a grant on it.
func orgScreensFixture(t *testing.T) (*store.Store, store.Repo, store.User) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	u := store.User{ID: uid, Username: "alice"}
	p := repo.Path()
	dispatchAs(t, st, u, "", "label", "set", p, "bug", "--color", "d73a4a")
	dispatchAs(t, st, u, "", "milestone", "create", p, "v1", "--due", "2030-01-01")
	dispatchAs(t, st, u, "", "org", "create", "acme")
	dispatchAs(t, st, u, "", "org", "label", "set", "acme", "triage", "--color", "0e8a16")
	dispatchAs(t, st, u, "", "org", "milestone", "create", "acme", "Q4")
	dispatchAs(t, st, u, "", "repo", "create", "acme/site")
	dispatchAs(t, st, u, "", "org", "team", "create", "acme", "core")
	dispatchAs(t, st, u, "", "org", "team", "add", "acme", "core", "alice")
	dispatchAs(t, st, u, "", "org", "team", "grant", "acme", "core", "acme/site", "write")
	return st, repo, u
}

func TestOrgPlainPinned(t *testing.T) {
	st, repo, u := orgScreensFixture(t)
	p := repo.Path()
	for name, argv := range map[string][]string{
		"org-list":           {"org", "list"},
		"org-show":           {"org", "show", "acme"},
		"org-members-list":   {"org", "members", "list", "acme"},
		"org-label-list":     {"org", "label", "list", "acme"},
		"label-list":         {"label", "list", p},
		"milestone-list":     {"milestone", "list", p},
		"org-milestone-list": {"org", "milestone", "list", "acme"},
		"org-team-list":      {"org", "team", "list", "acme"},
		"org-team-show":      {"org", "team", "show", "acme", "core"},
	} {
		pinPlain(t, name, dispatchAs(t, st, u, "", argv...))
	}
}

func TestOrgScreens(t *testing.T) {
	st, repo, u := orgScreensFixture(t)
	p := repo.Path()
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"org", "list"}, []string{"Organizations (1)\nacme  admin\n"}},
		{[]string{"org", "show", "acme"}, []string{"Org:  acme\n", "Members (1)\nalice  admin\n"}},
		{[]string{"org", "members", "list", "acme"}, []string{"Members (1)\n"}},
		{[]string{"org", "label", "list", "acme"}, []string{"Labels (1)\n", "triage", "0 issues · 0 MRs"}},
		{[]string{"label", "list", p}, []string{"Labels (1)\n", "bug", "0 issues · 0 MRs"}},
		{[]string{"milestone", "list", p}, []string{"Milestones (1)\n", "v1", "0/0 closed"}},
		{[]string{"org", "milestone", "list", "acme"}, []string{"Milestones (1)\n", "Q4"}},
		{[]string{"org", "team", "list", "acme"}, []string{"Teams (1)\ncore\n"}},
		{[]string{"org", "team", "show", "acme", "core"}, []string{"Team:     acme/core\n", "Members:  alice\n", "Grants (1)\nacme/site  write\n"}},
	} {
		out := atTerminal(t, st, u, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		checkLegend(t, out)
	}
}
