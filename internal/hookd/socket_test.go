package hookd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

func serveSocket(t *testing.T) (sock string, st *store.Store, repoID, uid int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	if uid, err = st.CreateUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if repoID, err = st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Server.Root = t.TempDir()
	stop, err := Serve(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	return SocketPath(cfg.Server.Root), st, repoID, uid
}

func TestSocketIsOwnerOnly(t *testing.T) {
	sock, _, _, _ := serveSocket(t)
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

// A request speaks for a receive-pack sshd started, and only for the
// repository, account and scope that push was started with (#282).
func TestHookRequestNeedsItsPushToken(t *testing.T) {
	sock, st, repoID, uid := serveSocket(t)
	req := Request{Hook: "pre-receive", RepoID: repoID, UserID: uid, Scope: "full"}

	resp, err := Ask(sock, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Allow || !strings.Contains(resp.Message, "not started by this server") {
		t.Fatalf("no token: %+v", resp)
	}

	token, err := st.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	req.Token = token
	if resp, err = Ask(sock, req, nil); err != nil || !resp.Allow {
		t.Fatalf("with token: %+v, %v", resp, err)
	}

	other, err := st.CreateUser("mallory", false)
	if err != nil {
		t.Fatal(err)
	}
	forged := req
	forged.UserID = other
	if resp, err = Ask(sock, forged, nil); err != nil || resp.Allow {
		t.Fatalf("token for another account: %+v, %v", resp, err)
	}

	otherRepo, err := st.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	forged = req
	forged.RepoID = otherRepo
	if resp, err = Ask(sock, forged, nil); err != nil || resp.Allow {
		t.Fatalf("token for another repository: %+v, %v", resp, err)
	}

	forged = req
	forged.Scope = "read"
	if resp, err = Ask(sock, forged, nil); err != nil || resp.Allow {
		t.Fatalf("token for another scope: %+v, %v", resp, err)
	}

	if err := st.DeletePushToken(token); err != nil {
		t.Fatal(err)
	}
	if resp, err = Ask(sock, req, nil); err != nil || resp.Allow {
		t.Fatalf("finished push: %+v, %v", resp, err)
	}
}
