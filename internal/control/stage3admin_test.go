package control

import (
	"regexp"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

var diskBytes = regexp.MustCompile(`\t\d{4,}\t|\d+\.\d KiB`)

// adminFixture is the work fixture with alice as an instance admin, a
// second account, and a closed merge request to prune.
func adminFixture(t *testing.T) (*store.Store, store.Repo, store.User, string) {
	t.Helper()
	st, repo, u, root, _ := workFixture(t)
	if err := st.SetUserAdmin(u.ID, true); err != nil {
		t.Fatal(err)
	}
	u.IsAdmin = true
	dispatchIn(t, st, u, root, "", "admin", "user", "create", "bob")
	dispatchIn(t, st, u, root, "", "mr", "close", repo.Path(), "1")
	return st, repo, u, root
}

func TestAdminPlainPinned(t *testing.T) {
	st, repo, u, root := adminFixture(t)
	for name, argv := range map[string][]string{
		"admin-user-list":          {"admin", "user", "list"},
		"admin-user-show":          {"admin", "user", "show", "alice"},
		"admin-runners":            {"admin", "runners"},
		"admin-repo-list":          {"admin", "repo", "list"},
		"admin-stats":              {"admin", "stats"},
		"admin-mail-inbound-check": {"admin", "mail", "inbound", "check"},
	} {
		got := dispatchIn(t, st, u, root, "", argv...)
		// A repository's size on disk moves by a few bytes between runs.
		got = diskBytes.ReplaceAllString(got, "<size>")
		pinPlain(t, name, got)
	}
	pinPlain(t, "admin-mr-prune", dispatchIn(t, st, u, root, "", "admin", "mr", "prune", repo.Path(), "1", "--yes"))
}

func TestAdminScreens(t *testing.T) {
	st, repo, u, root := adminFixture(t)
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"admin", "user", "list"}, []string{"Accounts (3)\n", "alice", "bob", "admin"}},
		{[]string{"admin", "user", "show", "alice"}, []string{"User:", "alice", "active", "gitbay admin user demote alice"}},
		{[]string{"admin", "runners"}, []string{"Queue:", "0 pending"}},
		{[]string{"admin", "repo", "list"}, []string{"Repositories (1)\n", "alice/app"}},
		{[]string{"admin", "stats"}, []string{"Users:", "2", "Repos:", "Disk:"}},
		{[]string{"admin", "mail", "inbound", "check"}, []string{"Inbound:  off\n"}},
		{[]string{"admin", "mr", "prune", repo.Path(), "1", "--yes"}, []string{"Pruned (1)\n", "!1", "already gone"}},
	} {
		out := atTerminalIn(t, st, u, root, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		if strings.Contains(tc.argv[1], "mail") || tc.argv[1] == "runners" {
			continue
		}
		checkLegend(t, out)
	}
}
