package control

import (
	"os"
	"path/filepath"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

// A transfer into an org folds the repository's same-named labels into
// the org's rows. The directory moves first, so a move that fails leaves
// the record, and the fold, untouched (#212).
func TestRepoTransferMovesDirectoryBeforeRecord(t *testing.T) {
	f := newOrgFixture(t)
	root := t.TempDir()
	if err := f.st.SetLabel(f.app, "bug", "#00ff00"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetOrgLabel(f.org, "bug", "#ff0000"); err != nil {
		t.Fatal(err)
	}
	oldDir := RepoDir(root, "alice", "app")
	newDir := RepoDir(root, "acme", "app")
	if err := os.MkdirAll(oldDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// A file where the org's directory must go makes the move fail.
	if err := os.WriteFile(filepath.Dir(newDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	repoLabels := func() int {
		var n int
		f.st.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE repo_id = ?", f.app.ID).Scan(&n)
		return n
	}
	run := func() (int, string) {
		c, out := f.ctx(f.alice)
		c.Cfg.Server.Root = root
		return runRepoTransfer(c, []string{"alice/app", "acme"}), out.String()
	}
	if code, out := run(); code != protocol.ExitFailure {
		t.Fatalf("failed move: exit %d %s", code, out)
	}
	if r, _ := f.st.RepoByID(f.app.ID); r.OwnerKind != "user" || r.OwnerID != f.alice {
		t.Fatalf("owner changed after a failed move: %s/%s", r.OwnerKind, r.OwnerName)
	}
	if n := repoLabels(); n != 1 {
		t.Fatalf("labels folded after a failed move: %d repo rows", n)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("directory moved after a failed move: %v", err)
	}

	if err := os.Remove(filepath.Dir(newDir)); err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != protocol.ExitOK {
		t.Fatalf("transfer: exit %d %s", code, out)
	}
	if r, _ := f.st.RepoByID(f.app.ID); r.OwnerKind != "org" || r.OwnerID != f.org {
		t.Fatalf("owner after transfer: %s/%s", r.OwnerKind, r.OwnerName)
	}
	if n := repoLabels(); n != 0 {
		t.Fatalf("labels not folded after transfer: %d repo rows", n)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("directory not moved: %v", err)
	}
}
