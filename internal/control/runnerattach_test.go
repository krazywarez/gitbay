package control

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// attachFixture: alice (not admin) owns alice/app with a build queued;
// mallory (not admin) owns mallory/evil with an older build queued. Each
// has a runner-scoped key. The Ctx polls as the given user with the given
// key, which is what the SSH listener produces.
type attachFixture struct {
	st                   *store.Store
	alice, mallory       int64
	aliceKey, malloryKey store.SSHKey
	app, evil            store.Repo
	appBuild, evilBuild  int64
}

func newAttachFixture(t *testing.T) attachFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f attachFixture
	f.st = st
	mk := func(name, fp string) (int64, store.SSHKey, store.Repo, string) {
		uid, err := st.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddSSHKey(uid, fp, "ssh-ed25519", []byte(fp), "runner", ""); err != nil {
			t.Fatal(err)
		}
		k, _ := st.SSHKeyByFingerprint(fp)
		repoName := map[string]string{"alice": "app", "mallory": "evil"}[name]
		rid, err := st.CreateRepo("user", uid, repoName, "public")
		if err != nil {
			t.Fatal(err)
		}
		repo, _ := st.RepoByID(rid)
		return uid, k, repo, repoName
	}
	f.mallory, f.malloryKey, f.evil, _ = mk("mallory", "SHA256:mallory")
	f.alice, f.aliceKey, f.app, _ = mk("alice", "SHA256:alice")
	// mallory's build is older, so an unrestricted claim would take it.
	f.evilBuild, err = st.CreateBuild(f.evil.ID, "unit", "aaa111", "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	f.appBuild, err = st.CreateBuild(f.app.ID, "unit", "bbb222", "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f attachFixture) ctx(uid int64, key store.SSHKey, admin bool) (*Ctx, *bytes.Buffer) {
	var out bytes.Buffer
	name := "alice"
	if uid == f.mallory {
		name = "mallory"
	}
	return &Ctx{
		User:   store.User{ID: uid, Username: name, IsAdmin: admin},
		Scope:  key.Scope,
		Source: key.Fingerprint,
		Store:  f.st,
		Cfg:    config.Config{Server: config.Server{Root: "/nonexistent", SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
	}, &out
}

// A runner key with no attachment claims nothing, whatever is queued.
func TestRunnerNextUnattachedClaimsNothing(t *testing.T) {
	f := newAttachFixture(t)
	c, out := f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, nil); code != protocol.ExitOK || !strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	b, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild)
	if b.Status != "pending" {
		t.Fatalf("unattached key claimed a build: %s", b.Status)
	}
}

// An attached key claims its repository's build and not the older one
// queued elsewhere; naming a repository outside the attachments is refused.
func TestRunnerNextAttachedClaimsOwnRepoOnly(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.aliceKey.ID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, nil); code != protocol.ExitOK || !strings.Contains(out.String(), "alice/app") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if b, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild); b.Status != "pending" {
		t.Fatalf("mallory's build was touched: %s", b.Status)
	}
	c, out = f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, []string{"mallory/evil"}); code != protocol.ExitDenied {
		t.Fatalf("naming an unattached repo: exit %d, want %d: %s", code, protocol.ExitDenied, out.String())
	}
}

// The heartbeat is recorded against the key, and admin runners shows it
// with its fingerprint and attachments. The column is the attachments even
// when the key polled with a narrower -repos, and none when it has no
// attachment at all.
func TestAdminRunnersShowsKeyAndAttachments(t *testing.T) {
	f := newAttachFixture(t)
	for _, id := range []int64{f.app.ID, f.evil.ID} {
		if err := f.st.AttachRunner(f.aliceKey.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, []string{"alice/app"})
	c, _ = f.ctx(f.mallory, f.malloryKey, false)
	runRunnerNext(c, nil)
	admin, out := f.ctx(f.alice, f.aliceKey, true)
	admin.Scope = "full"
	if code := runAdminRunners(admin, nil); code != protocol.ExitOK {
		t.Fatalf("admin runners: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "alice\tSHA256:alice\t") ||
		!strings.Contains(out.String(), "\talice/app,mallory/evil\t") {
		t.Fatalf("row lacks fingerprint or attachments:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "\tnone\t") {
		t.Fatalf("mallory's unattached runner key is not none:\n%s", out.String())
	}
}

// The instance-admin bypass is the key, not the account: a runner-scoped
// key on an admin account claims only what it is attached to.
func TestRunnerNextAdminAccountRunnerKeyIsConfined(t *testing.T) {
	f := newAttachFixture(t)
	c, out := f.ctx(f.alice, f.aliceKey, true)
	if code := runRunnerNext(c, nil); code != protocol.ExitOK || !strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	for _, b := range []struct {
		repo   store.Repo
		number int64
	}{{f.app, f.appBuild}, {f.evil, f.evilBuild}} {
		if got, _ := f.st.BuildByNumber(b.repo.ID, b.number); got.Status != "pending" {
			t.Fatalf("%s claimed by an unattached runner key: %s", b.repo.Path(), got.Status)
		}
	}
	if err := f.st.AttachRunner(f.aliceKey.ID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	c, out = f.ctx(f.alice, f.aliceKey, true)
	if code := runRunnerNext(c, nil); code != protocol.ExitOK || !strings.Contains(out.String(), "alice/app") {
		t.Fatalf("attached claim: exit %d: %s", code, out.String())
	}
	if got, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild); got.Status != "pending" {
		t.Fatalf("mallory's build was claimed: %s", got.Status)
	}
}

// Untrusted builds are skipped unless the runner asks.
func TestRunnerNextUntrustedFlag(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.aliceKey.ID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, nil) // takes the trusted build
	fork, err := f.st.CreateBuild(f.app.ID, "unit", "ccc333", "refs/merge-requests/1/head", "[]", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, nil)
	if !strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("fork head claimed without --untrusted: %s", out.String())
	}
	c, out = f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, []string{"--untrusted"}); code != protocol.ExitOK || !strings.Contains(out.String(), "alice/app") {
		t.Fatalf("--untrusted did not claim the fork head: exit %d %s", code, out.String())
	}
	if b, _ := f.st.BuildByNumber(f.app.ID, fork); b.Status != "running" {
		t.Fatalf("fork build is %s, want running", b.Status)
	}
}

// runner done and runner log on a build whose repository is not attached
// to the key are refused.
func TestRunnerDoneRefusedForUnattachedBuild(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.malloryKey.ID, f.evil.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := f.ctx(f.mallory, f.malloryKey, false)
	runRunnerNext(c, nil) // mallory holds her own build
	evil, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild)
	c, out := f.ctx(f.alice, f.aliceKey, false)
	id := strconv.FormatInt(evil.ID, 10)
	if code := runRunnerDone(c, []string{id, "success"}); code != protocol.ExitDenied {
		t.Fatalf("done on an unattached build: exit %d, want %d: %s", code, protocol.ExitDenied, out.String())
	}
	c, out = f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerLog(c, []string{id}); code != protocol.ExitDenied {
		t.Fatalf("log on an unattached build: exit %d, want %d: %s", code, protocol.ExitDenied, out.String())
	}
	if b, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild); b.Status != "running" {
		t.Fatalf("build was finished by a foreign key: %s", b.Status)
	}
}

// admin runners forget drops one heartbeat row by fingerprint.
func TestAdminRunnersForget(t *testing.T) {
	f := newAttachFixture(t)
	c, _ := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, nil)
	admin, out := f.ctx(f.alice, f.aliceKey, true)
	admin.Scope = "full"
	if code := runAdminRunnersForget(admin, []string{"SHA256:nobody"}); code != protocol.ExitNotFound {
		t.Fatalf("unknown fingerprint: exit %d, want %d: %s", code, protocol.ExitNotFound, out.String())
	}
	admin, out = f.ctx(f.alice, f.aliceKey, true)
	admin.Scope = "full"
	if code := runAdminRunnersForget(admin, []string{f.aliceKey.Fingerprint}); code != protocol.ExitOK {
		t.Fatalf("forget: exit %d: %s", code, out.String())
	}
	admin, out = f.ctx(f.alice, f.aliceKey, true)
	admin.Scope = "full"
	runAdminRunners(admin, nil)
	if strings.Contains(out.String(), f.aliceKey.Fingerprint) {
		t.Fatalf("row still listed after forget:\n%s", out.String())
	}
	user, out := f.ctx(f.alice, f.aliceKey, false)
	user.Scope = "full"
	if code := runAdminRunnersForget(user, []string{f.aliceKey.Fingerprint}); code != protocol.ExitDenied {
		t.Fatalf("non-admin forgot a runner: exit %d: %s", code, out.String())
	}
}
