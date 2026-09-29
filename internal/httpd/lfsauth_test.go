package httpd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/lfs"
	"gitbay.org/gitbay/internal/store"
)

func lfsRequest(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/alice/app.git/info/lfs/objects/x", nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

func lfsTestRepo(t *testing.T, st *store.Store, uid int64, name, visibility string) store.Repo {
	t.Helper()
	id, err := st.CreateRepo("user", uid, name, visibility)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// A token works only while its key does: removed, expired or on a
// disabled account, the key takes its tokens with it (#285).
func TestLFSTokenNeedsALiveKey(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	repo := lfsTestRepo(t, st, u.ID, "app", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	addKey := func(fp string, exp *time.Time) int64 {
		t.Helper()
		if err := st.AddSSHKeyFrom(u.ID, fp, "ssh-ed25519", []byte(fp), "full", "", store.KeyOrigin{ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
		k, err := st.SSHKeyByFingerprint(fp)
		if err != nil {
			t.Fatal(err)
		}
		return k.ID
	}

	live := addKey("SHA256:live", nil)
	tok := lfs.Sign(secret, repo.ID, live, "upload", time.Now())
	if op, key := s.lfsAuth(lfsRequest(tok), repo); op != "upload" || key != live {
		t.Fatalf("live key: %q, %d", op, key)
	}

	past := time.Now().Add(-time.Minute)
	expired := addKey("SHA256:expired", &past)
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, expired, "upload", time.Now())), repo); op != "" {
		t.Errorf("expired key: %q", op)
	}

	if err := st.RemoveSSHKey(u.ID, "SHA256:live"); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(tok), repo); op != "" {
		t.Errorf("removed key: %q", op)
	}

	other := addKey("SHA256:other", nil)
	otherTok := lfs.Sign(secret, repo.ID, other, "download", time.Now())
	if op, _ := s.lfsAuth(lfsRequest(otherTok), repo); op != "download" {
		t.Fatalf("second key before disable: %q", op)
	}
	if err := st.SetUserDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(otherTok), repo); op != "" {
		t.Errorf("disabled account: %q", op)
	}
}

// A token with no key comes from an anonymous batch on a public
// repository and is worth exactly what anonymous is: a download, while
// the repository is public.
func TestLFSAnonymousTokenOnlyDownloadsPublic(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	pub := lfsTestRepo(t, st, u.ID, "big", "public")
	priv := lfsTestRepo(t, st, u.ID, "vault", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, pub.ID, 0, "download", now)), pub); op != "download" {
		t.Errorf("public download: %q", op)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, pub.ID, 0, "upload", now)), pub); op != "" {
		t.Errorf("anonymous upload: %q", op)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, priv.ID, 0, "download", now)), priv); op != "" {
		t.Errorf("private download: %q", op)
	}
}

func lfsTestKey(t *testing.T, st *store.Store, uid int64, fp, scope string) int64 {
	t.Helper()
	if err := st.AddSSHKey(uid, fp, "ssh-ed25519", []byte(fp), scope, ""); err != nil {
		t.Fatal(err)
	}
	k, err := st.SSHKeyByFingerprint(fp)
	if err != nil {
		t.Fatal(err)
	}
	return k.ID
}

// A token carries only the access its key's account still has: a
// collaborator removed from the repository, or a reader of a public
// repository made private, loses the token with the access (#285).
func TestLFSTokenNeedsCurrentAccess(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	repo := lfsTestRepo(t, st, u.ID, "app", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	bob, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.GrantAccess(repo.ID, bob, "write"); err != nil {
		t.Fatal(err)
	}
	bobKey := lfsTestKey(t, st, bob, "SHA256:bob", "full")
	up := lfs.Sign(secret, repo.ID, bobKey, "upload", now)
	if op, _ := s.lfsAuth(lfsRequest(up), repo); op != "upload" {
		t.Fatalf("collaborator upload: %q", op)
	}
	if err := st.GrantAccess(repo.ID, bob, "read"); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(up), repo); op != "" {
		t.Errorf("upload after write was taken away: %q", op)
	}
	if err := st.RevokeAccess(repo.ID, bob); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, bobKey, "download", now)), repo); op != "" {
		t.Errorf("download after access was revoked: %q", op)
	}

	pub := lfsTestRepo(t, st, u.ID, "big", "public")
	carol, err := st.CreateUser("carol", false)
	if err != nil {
		t.Fatal(err)
	}
	carolKey := lfsTestKey(t, st, carol, "SHA256:carol", "full")
	down := lfs.Sign(secret, pub.ID, carolKey, "download", now)
	if op, _ := s.lfsAuth(lfsRequest(down), pub); op != "download" {
		t.Fatalf("public download: %q", op)
	}
	if err := st.SetRepoVisibility(pub.ID, "private"); err != nil {
		t.Fatal(err)
	}
	pub, err = st.RepoByID(pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(down), pub); op != "" {
		t.Errorf("download after the repository went private: %q", op)
	}
}

// A deploy key's token lasts as long as the deploy key: removed from the
// repository or on a disabled account it is refused, and a read-only
// binding never uploads.
func TestLFSDeployKeyToken(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	repo := lfsTestRepo(t, st, u.ID, "app", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rw := fmt.Sprintf("deploy:%d:rw", repo.ID)
	ro := fmt.Sprintf("deploy:%d:ro", repo.ID)

	live := lfsTestKey(t, st, u.ID, "SHA256:deploy-live", rw)
	if op, key := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, live, "upload", now)), repo); op != "upload" || key != live {
		t.Fatalf("live deploy key: %q, %d", op, key)
	}

	removed := lfsTestKey(t, st, u.ID, "SHA256:deploy-removed", rw)
	tok := lfs.Sign(secret, repo.ID, removed, "download", now)
	if err := st.RemoveDeployKey(repo.ID, "SHA256:deploy-removed"); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(tok), repo); op != "" {
		t.Errorf("removed deploy key: %q", op)
	}

	readOnly := lfsTestKey(t, st, u.ID, "SHA256:deploy-ro", ro)
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, readOnly, "download", now)), repo); op != "download" {
		t.Errorf("read-only deploy key download: %q", op)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, readOnly, "upload", now)), repo); op != "" {
		t.Errorf("read-only deploy key upload: %q", op)
	}

	if err := st.SetUserDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, live, "download", now)), repo); op != "" {
		t.Errorf("deploy key of a disabled account: %q", op)
	}
}

// An upload token minted before the repository was archived uploads
// nothing after it, as git-lfs-authenticate would refuse to mint one.
func TestLFSUploadTokenRefusedOnceArchived(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	repo := lfsTestRepo(t, st, u.ID, "app", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	key := lfsTestKey(t, st, u.ID, "SHA256:owner", "full")
	now := time.Now()
	up := lfs.Sign(secret, repo.ID, key, "upload", now)
	if op, _ := s.lfsAuth(lfsRequest(up), repo); op != "upload" {
		t.Fatalf("upload before archiving: %q", op)
	}
	if _, err := st.UpdateRepoSettings(repo.ID, func(rs *store.RepoSettings) { rs.Archived = true }); err != nil {
		t.Fatal(err)
	}
	repo, err = st.RepoByID(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(up), repo); op != "" {
		t.Errorf("upload after archiving: %q", op)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, key, "download", now)), repo); op != "download" {
		t.Errorf("download after archiving: %q", op)
	}
}
