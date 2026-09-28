package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/backuplock"
	"gitbay.org/gitbay/internal/protocol"
)

func TestRepoDeleteAndRenameRefusedDuringBackup(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	owner, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	release, err := backuplock.Hold(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"repo", "rename", repo.Path(), "renamed"},
		{"repo", "delete", repo.Path(), "--yes"},
	} {
		c, errOut := pruneCtx(st, root, owner)
		if code := Dispatch(c, argv); code != protocol.ExitFailure || !strings.Contains(errOut.String(), "a backup is running") {
			t.Fatalf("%v during a backup: exit %d, %s", argv, code, errOut)
		}
	}
	if got, err := st.RepoByID(repo.ID); err != nil || got.Name != repo.Name {
		t.Fatalf("repository changed during a backup: %+v, %v", got, err)
	}
	release()

	c, errOut := pruneCtx(st, root, owner)
	if code := Dispatch(c, []string{"repo", "delete", repo.Path(), "--yes"}); code != protocol.ExitOK {
		t.Fatalf("delete after the backup: exit %d, %s", code, errOut)
	}
}
