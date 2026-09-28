package control

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestRepoReadmePicksTheRichestFormat(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "README.md"), []byte("# app\n\nhello\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	c, errOut := pruneCtx(st, root, store.User{ID: uid})
	c.Cfg.Limits.MaxBlobBytes = 100 << 20
	if code := Dispatch(c, []string{"repo", "readme", repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := c.Stdout.(*bytes.Buffer).String(); got != "# app\n\nhello\n" {
		t.Errorf("readme = %q", got)
	}
}
