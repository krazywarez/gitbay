package hookd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/policy"
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

func refusedRows(t *testing.T, st *store.Store, action string) []store.AuditEntry {
	t.Helper()
	rows, err := st.AuditEntries(store.AuditFilter{ActionPrefix: action, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Refused hook requests and refused pushes are audited; the token never
// lands in a row (#275).
func TestHookRefusalsAreAudited(t *testing.T) {
	sock, st, repoID, uid := serveSocket(t)

	forged := Request{Hook: "pre-receive", RepoID: repoID, UserID: uid, Scope: "full", Token: "not-a-live-token"}
	if resp, err := Ask(sock, forged, nil); err != nil || resp.Allow {
		t.Fatalf("forged: %+v, %v", resp, err)
	}
	rows := refusedRows(t, st, "refused hook")
	if len(rows) != 1 || rows[0].Actor != "" || strings.Contains(rows[0].Data, forged.Token) ||
		!strings.Contains(rows[0].Data, "not started by this server") || !strings.Contains(rows[0].Data, `"hook":"pre-receive"`) {
		t.Fatalf("refused hook rows: %+v", rows)
	}

	token, err := st.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Hook: "pre-receive", RepoID: repoID, UserID: uid, Scope: "full", Token: token,
		Updates: []policy.RefUpdate{{Ref: "refs/merge-requests/1/head", Old: zeroSHA40, New: strings.Repeat("a", 40)}}}
	if resp, err := Ask(sock, req, nil); err != nil || resp.Allow {
		t.Fatalf("push to a server-owned ref: %+v, %v", resp, err)
	}
	rows = refusedRows(t, st, "refused push")
	if len(rows) != 1 || rows[0].Actor != "alice" || strings.Contains(rows[0].Data, token) ||
		!strings.Contains(rows[0].Data, "alice/app") || !strings.Contains(rows[0].Data, "refs/merge-requests/1/head") {
		t.Fatalf("refused push rows: %+v", rows)
	}
}

// A connection from another uid is audited with no actor.
func TestPeerRefusalIsAudited(t *testing.T) {
	old := peerCheck
	peerCheck = func(net.Conn) error { return errors.New("peer uid not permitted") }
	t.Cleanup(func() { peerCheck = old })
	sock, st, repoID, uid := serveSocket(t)
	if resp, err := Ask(sock, Request{Hook: "pre-receive", RepoID: repoID, UserID: uid}, nil); err != nil || resp.Allow {
		t.Fatalf("refused peer: %+v, %v", resp, err)
	}
	rows := refusedRows(t, st, "refused hook")
	if len(rows) != 1 || rows[0].Actor != "" || !strings.Contains(rows[0].Data, "peer uid not permitted") {
		t.Fatalf("refused hook rows: %+v", rows)
	}
}
