package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Generated once with ssh-keygen -t ed25519; a valid authorized_keys line.
const testRunnerPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILAr2r82jFsCJwsEyrEf2wgKy9Dv45xYYici6Ii7NyCS runner@test\n"

func repoRunnerCtx(t *testing.T, st *store.Store, uid int64, admin bool, stdin string) (*Ctx, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return &Ctx{
		User:   store.User{ID: uid, Username: "alice", IsAdmin: admin},
		Scope:  "full",
		Source: "SHA256:session",
		Store:  st,
		Cfg:    config.Config{Server: config.Server{SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &out,
		JSON:   true,
	}, &out
}

// A fresh key is registered on the caller's account with scope runner and
// attached; a second add is a no-op; list shows it; remove detaches and
// leaves the key on the account.
func TestRepoRunnerAddListRemove(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	c, out := repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("add: exit %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"fingerprint":"SHA256:`) {
		t.Fatalf("add output: %s", out.String())
	}
	keys, _ := st.ListSSHKeys(uid)
	if len(keys) != 1 || keys[0].Scope != "runner" {
		t.Fatalf("key not registered as runner: %+v", keys)
	}
	fp := keys[0].Fingerprint
	c, out = repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("second add: exit %d %s", code, out.String())
	}
	c, out = repoRunnerCtx(t, st, uid, false, "")
	if code := runRepoRunnerList(c, []string{repo.Path()}); code != protocol.ExitOK || strings.Count(out.String(), fp) != 1 {
		t.Fatalf("list: exit %d %s", code, out.String())
	}
	c, out = repoRunnerCtx(t, st, uid, false, "")
	if code := runRepoRunnerRemove(c, []string{repo.Path(), fp}); code != protocol.ExitOK {
		t.Fatalf("remove: exit %d %s", code, out.String())
	}
	if ok, _ := st.RunnerAttached(keys[0].ID, repo.ID); ok {
		t.Fatal("still attached after remove")
	}
	if keys, _ = st.ListSSHKeys(uid); len(keys) != 1 {
		t.Fatal("remove dropped the key from the account")
	}
	c, out = repoRunnerCtx(t, st, uid, false, "")
	if code := runRepoRunnerRemove(c, []string{repo.Path(), fp}); code != protocol.ExitNotFound {
		t.Fatalf("remove twice: exit %d, want %d", code, protocol.ExitNotFound)
	}
}

// A key that already exists with another scope is never promoted, and
// another account's runner key is refused unless the caller is an admin.
func TestRepoRunnerAddRefusesWrongKeys(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	c, _ := repoRunnerCtx(t, st, uid, false, testRunnerPub)
	// Register the same key as a full key first.
	if code := runKeysAdd(c, nil); code != protocol.ExitOK {
		t.Fatal("keys add failed")
	}
	c, out := repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitDenied {
		t.Fatalf("full key accepted as runner: exit %d %s", code, out.String())
	}
	keys, _ := st.ListSSHKeys(uid)
	if keys[0].Scope != "full" {
		t.Fatalf("scope changed to %s", keys[0].Scope)
	}
	// Someone else's runner key.
	bob, _ := st.CreateUser("bob", false)
	if err := st.AddSSHKey(bob, "SHA256:bobrunner", "ssh-ed25519", []byte("x"), "runner", ""); err != nil {
		t.Fatal(err)
	}
	st.RemoveSSHKey(uid, keys[0].Fingerprint)
	if err := st.AddSSHKey(bob, keys[0].Fingerprint, "ssh-ed25519", keys[0].Blob, "runner", ""); err != nil {
		t.Fatal(err)
	}
	c, out = repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitDenied {
		t.Fatalf("another account's key attached by a non-admin: exit %d %s", code, out.String())
	}
	c, out = repoRunnerCtx(t, st, uid, true, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("admin could not attach another account's runner key: exit %d %s", code, out.String())
	}
	// The runner clones what it builds: bob cannot read alice's private
	// repository, so not even an admin may attach his key to it.
	secretID, err := st.CreateRepo("user", uid, "secret", "private")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := st.RepoByID(secretID)
	if err != nil {
		t.Fatal(err)
	}
	c, out = repoRunnerCtx(t, st, uid, true, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{secret.Path()}); code != protocol.ExitDenied {
		t.Fatalf("key attached to a repo its account cannot read: exit %d %s", code, out.String())
	}
	if runners, _ := st.ListRepoRunners(secret.ID); len(runners) != 0 {
		t.Fatalf("attached anyway: %+v", runners)
	}
}
