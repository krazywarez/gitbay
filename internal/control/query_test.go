package control

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

type queryEnv struct {
	t     *testing.T
	st    *store.Store
	users map[string]store.User
}

func newQueryEnv(t *testing.T) queryEnv {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	e := queryEnv{t, st, map[string]store.User{}}
	for _, name := range []string{"alice", "bob"} {
		id, err := st.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		e.users[name] = store.User{ID: id, Username: name}
	}
	return e
}

// run dispatches argv as user with --json and returns the exit code,
// the envelope's data and stderr.
func (e queryEnv) run(user string, argv ...string) (int, json.RawMessage, string) {
	var out, errOut bytes.Buffer
	c := &Ctx{User: e.users[user], Scope: "full", Store: e.st, Cfg: config.Config{},
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}
	code := Dispatch(c, append(argv, "--json"))
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	json.Unmarshal(out.Bytes(), &env)
	return code, env.Data, errOut.String() + out.String()
}

func (e queryEnv) repo(owner, name, vis string) store.Repo {
	id, err := e.st.CreateRepo("user", e.users[owner].ID, name, vis)
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := e.st.RepoByID(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestQueryCommands(t *testing.T) {
	e := newQueryEnv(t)
	pub := e.repo("alice", "pub", "public")
	secret := e.repo("bob", "secret", "private")
	for i, r := range []store.Repo{pub, secret, pub} {
		owner := e.users[r.OwnerName]
		if _, err := e.st.CreateIssue(r.ID, owner.ID, "issue "+string(rune('a'+i)), "", "md"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.CreateMR(pub.ID, e.users["alice"].ID, pub.ID, "topic", "main", "an mr", "", "", "md", false); err != nil {
		t.Fatal(err)
	}

	if code, _, msg := e.run("alice", "query", "save", "Bad Name", "is:open"); code != protocol.ExitUsage {
		t.Fatalf("bad name: exit %d %s", code, msg)
	}
	if code, _, msg := e.run("alice", "query", "save", "open", "is:open", "bogus:x"); code != protocol.ExitUsage || !strings.Contains(msg, "bogus:x") {
		t.Fatalf("bad term: exit %d %s; want 2 naming the term", code, msg)
	}
	code, data, msg := e.run("alice", "query", "save", "open", "is:open is:open", "label:needs review")
	if code != 0 {
		t.Fatalf("save: exit %d %s", code, msg)
	}
	var saved SavedQueryOut
	json.Unmarshal(data, &saved)
	if saved.Query != "is:open label:needs review" {
		t.Errorf("stored %q: want the canonical text", saved.Query)
	}
	if code, _, msg := e.run("alice", "query", "save", "open", "is:open"); code != protocol.ExitFailure || !strings.Contains(msg, "--force") {
		t.Fatalf("save over an existing name: exit %d %s", code, msg)
	}
	if code, _, msg := e.run("alice", "query", "save", "open", "is:open", "--force"); code != 0 {
		t.Fatalf("save --force: exit %d %s", code, msg)
	}

	// bob's saved query sees his private repository; the same text run
	// by alice does not, and her count does not include it.
	for _, u := range []string{"alice", "bob"} {
		if code, _, msg := e.run(u, "query", "save", "all", "is:open"); code != 0 {
			t.Fatalf("save as %s: exit %d %s", u, code, msg)
		}
	}
	count := func(user string) int {
		code, data, msg := e.run(user, "query", "show", "all")
		if code != 0 {
			t.Fatalf("show as %s: exit %d %s", user, code, msg)
		}
		var d SavedQueryOut
		json.Unmarshal(data, &d)
		return *d.Count
	}
	if a, b := count("alice"), count("bob"); a != 3 || b != 4 {
		t.Errorf("counts alice %d bob %d, want 3 and 4", a, b)
	}

	type pageOut struct {
		Items []QueryItem `json:"items"`
		Next  string      `json:"next"`
	}
	var seen []string
	cursor := ""
	for i := 0; i < 5; i++ {
		argv := []string{"query", "run", "all", "--limit", "1"}
		if cursor != "" {
			argv = append(argv, "--cursor", cursor)
		}
		code, data, msg := e.run("alice", argv...)
		if code != 0 {
			t.Fatalf("run: exit %d %s", code, msg)
		}
		var p pageOut
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		for _, it := range p.Items {
			seen = append(seen, it.Ref())
		}
		if cursor = p.Next; cursor == "" {
			break
		}
	}
	if got := strings.Join(seen, " "); got != "alice/pub!1 alice/pub#2 alice/pub#1" {
		t.Errorf("paged run = %s", got)
	}

	// issue list and mr list narrow to their own kind.
	code, data, msg = e.run("alice", "issue", "list", "--query", "all")
	var p pageOut
	json.Unmarshal(data, &p)
	if code != 0 || len(p.Items) != 2 || p.Items[0].Kind != "issue" {
		t.Errorf("issue list --query: exit %d %s", code, msg)
	}
	code, data, msg = e.run("alice", "mr", "list", "--q", "repo:alice/* is:open")
	p = pageOut{}
	json.Unmarshal(data, &p)
	if code != 0 || len(p.Items) != 1 || p.Items[0].Repo != "alice/pub" {
		t.Errorf("mr list --q: exit %d %s", code, msg)
	}
	for _, argv := range [][]string{
		{"issue", "list", "alice/pub", "--query", "all"},
		{"issue", "list", "--query", "all", "--state", "closed"},
		{"issue", "list", "--query", "all", "--q", "is:open"},
		{"issue", "list", "--q", "is:mr"},
		{"mr", "list", "--q", "assignee:@me"},
		{"mr", "list", "--q", "nope:x"},
		{"issue", "list", "--query", "all", "--cursor", "bad"},
		{"issue", "list", "--query", "all", "--cursor", encodeCursor("issue", "3")},
	} {
		if code, _, msg := e.run("alice", argv...); code != protocol.ExitUsage {
			t.Errorf("%q: exit %d %s, want usage", argv, code, msg)
		}
	}
	if code, _, _ := e.run("alice", "issue", "list", "--query", "nosuch"); code != protocol.ExitNotFound {
		t.Errorf("unknown saved query: exit %d, want not found", code)
	}

	// Pinned queries reach the dashboard with their count and first rows.
	if code, _, msg := e.run("alice", "query", "pin", "all"); code != 0 {
		t.Fatalf("pin: exit %d %s", code, msg)
	}
	code, data, msg = e.run("alice", "dashboard")
	if code != 0 {
		t.Fatalf("dashboard: exit %d %s", code, msg)
	}
	var d DashboardOut
	json.Unmarshal(data, &d)
	if len(d.Queries) != 1 || d.Queries[0].Name != "all" || d.Queries[0].Count != 3 || len(d.Queries[0].Items) != 3 {
		t.Errorf("dashboard queries = %+v", d.Queries)
	}
	if code, _, _ := e.run("alice", "query", "unpin", "all"); code != 0 {
		t.Fatal("unpin")
	}
	_, data, _ = e.run("alice", "dashboard")
	d = DashboardOut{}
	json.Unmarshal(data, &d)
	if d.Queries == nil || len(d.Queries) != 0 {
		t.Errorf("after unpin, dashboard queries = %#v, want []", d.Queries)
	}

	code, data, _ = e.run("alice", "query", "list")
	var list []SavedQueryOut
	json.Unmarshal(data, &list)
	if code != 0 || len(list) != 2 || list[0].Name != "all" || list[1].Name != "open" {
		t.Errorf("query list = %+v", list)
	}
	if code, _, _ := e.run("alice", "query", "remove", "all"); code != 0 {
		t.Fatal("remove")
	}
	if code, _, _ := e.run("alice", "query", "show", "all"); code != protocol.ExitNotFound {
		t.Errorf("show after remove: exit %d", code)
	}
	// bob's query of the same name is his own.
	if code, _, _ := e.run("bob", "query", "show", "all"); code != 0 {
		t.Errorf("bob lost his query when alice removed hers")
	}
}
