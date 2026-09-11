package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// orgFixture: alice admins org acme with acme/core (public) and acme/priv
// (private); bob is a plain member; carol is outside. alice also owns
// alice/app.
type orgFixture struct {
	st                *store.Store
	alice, bob, carol int64
	org               int64
	core, priv, app   store.Repo
}

func newOrgFixture(t *testing.T) orgFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f orgFixture
	f.st = st
	user := func(name string) int64 {
		id, err := st.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.alice, f.bob, f.carol = user("alice"), user("bob"), user("carol")
	if f.org, err = st.CreateOrg("acme", f.alice); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgMember(f.org, f.bob, "member"); err != nil {
		t.Fatal(err)
	}
	repo := func(kind string, owner int64, name, vis string) store.Repo {
		id, err := st.CreateRepo(kind, owner, name, vis)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := st.RepoByID(id)
		return r
	}
	f.core = repo("org", f.org, "core", "public")
	f.priv = repo("org", f.org, "priv", "private")
	f.app = repo("user", f.alice, "app", "public")
	return f
}

func (f orgFixture) ctx(uid int64) (*Ctx, *bytes.Buffer) {
	var out bytes.Buffer
	name := map[int64]string{f.alice: "alice", f.bob: "bob", f.carol: "carol"}[uid]
	return &Ctx{
		User:   store.User{ID: uid, Username: name},
		Scope:  "full",
		Source: "SHA256:session",
		Store:  f.st,
		Cfg:    config.Config{Server: config.Server{SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
		JSON:   true,
	}, &out
}

func TestReadableOrgRepoIDs(t *testing.T) {
	f := newOrgFixture(t)
	ids, err := ReadableOrgRepoIDs(f.st, store.User{ID: f.bob, Username: "bob"}, f.org)
	if err != nil || len(ids) != 2 {
		t.Fatalf("member reads %v, %v; want both", ids, err)
	}
	ids, _ = ReadableOrgRepoIDs(f.st, store.User{ID: f.carol, Username: "carol"}, f.org)
	if len(ids) != 1 || ids[0] != f.core.ID {
		t.Fatalf("outsider reads %v; want core only", ids)
	}
	ids, _ = ReadableOrgRepoIDs(f.st, store.User{}, f.org)
	if len(ids) != 1 || ids[0] != f.core.ID {
		t.Fatalf("anonymous reads %v; want core only", ids)
	}
	ids, _ = ReadableScope(f.st, store.User{ID: f.alice, Username: "alice"}, f.app)
	if len(ids) != 1 || ids[0] != f.app.ID {
		t.Fatalf("user repo scope %v; want itself", ids)
	}
}

func TestRepoLabelCommandsRefuseOrgNames(t *testing.T) {
	f := newOrgFixture(t)
	if _, err := f.st.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice)
	if code := runLabelSet(c, []string{"acme/core", "bug", "--color", "ff0000"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org label set acme bug") {
		t.Fatalf("label set over org name: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runLabelRemove(c, []string{"acme/core", "bug"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org label remove acme bug") {
		t.Fatalf("label remove of org row: exit %d %s", code, out.String())
	}
	out.Reset()
	// issue label --add resolves to the org row, and label list marks it.
	iid, _ := f.st.CreateIssue(f.core.ID, f.alice, "c1", "", "md")
	_ = iid
	if code := runIssueLabel(c, []string{"acme/core", "1", "--add", "bug"}); code != protocol.ExitOK {
		t.Fatalf("issue label: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runLabelList(c, []string{"acme/core"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"org":true`) || !strings.Contains(out.String(), `"issues":1`) {
		t.Fatalf("label list: exit %d %s", code, out.String())
	}
}

func TestRepoMilestoneCommandsRefuseOrgTitles(t *testing.T) {
	f := newOrgFixture(t)
	if _, _, err := f.st.CreateOrgMilestone(f.org, "v1", "", ""); err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice)
	if code := runMilestoneCreate(c, []string{"acme/core", "v1"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org milestone create acme v1") {
		t.Fatalf("milestone create over org title: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runMilestoneClose(c, []string{"acme/core", "v1"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org milestone close acme v1") {
		t.Fatalf("milestone close of org row: exit %d %s", code, out.String())
	}
	out.Reset()
	// Attaching by title from a repo resolves the org milestone.
	f.st.CreateIssue(f.core.ID, f.alice, "c1", "", "md")
	if code := runIssueMilestone(c, []string{"acme/core", "1", "v1"}); code != protocol.ExitOK {
		t.Fatalf("issue milestone: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runMilestoneList(c, []string{"acme/core"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"org":true`) || !strings.Contains(out.String(), `"open":1`) {
		t.Fatalf("milestone list: exit %d %s", code, out.String())
	}
}
