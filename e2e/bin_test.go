package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The suite starts an instance per test, and each one needs gitbayd. Built
// per test, that is 200-odd links of a byte-identical 35MB binary: go's
// build cache covers the compile but not the final link, so it cost about
// 0.8s every time, a quarter of the whole run.
//
// Built once per process instead, under a directory TestMain owns. The
// binaries outlive any single test, so t.TempDir is the wrong home for
// them — it is removed when the test that asked for it ends.
var (
	gitbaydBin = &builtBin{name: "gitbayd", pkg: "gitbay.org/gitbay/cmd/gitbayd"}
	gitbayBin  = &builtBin{name: "gitbay", pkg: "gitbay.org/gitbay/cmd/gitbay"}
	runnerBin  = &builtBin{name: "gitbay-runner", pkg: "gitbay.org/gitbay/cmd/gitbay-runner"}
)

// binDir is where the built binaries live, set by TestMain.
var binDir string

type builtBin struct {
	name string
	pkg  string

	once sync.Once
	path string
	out  []byte
	err  error
}

// get builds the binary on first use and returns the same path thereafter.
// Lazily, so `-run TestOneThing` does not link the two binaries it has no
// use for.
func (b *builtBin) get(t *testing.T) string {
	t.Helper()
	b.once.Do(func() {
		b.path = filepath.Join(binDir, b.name)
		cmd := exec.Command("go", "build", "-o", b.path, b.pkg)
		cmd.Dir = ".."
		b.out, b.err = cmd.CombinedOutput()
	})
	if b.err != nil {
		t.Fatalf("build %s: %v\n%s", b.name, b.err, b.out)
	}
	return b.path
}

func buildGitbayd(t *testing.T) string   { return gitbaydBin.get(t) }
func buildGitbayCLI(t *testing.T) string { return gitbayBin.get(t) }
func buildRunner(t *testing.T) string    { return runnerBin.get(t) }

// makeBinDir is called by TestMain. Returned rather than deferred because
// TestMain ends in os.Exit, which runs no deferred functions.
func makeBinDir() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "e2e-bin")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}
