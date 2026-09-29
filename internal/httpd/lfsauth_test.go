package httpd

import (
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
