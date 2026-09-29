package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A push to the default branch is indexed in the background, and
// repo symbols then finds a Go function where it is defined (#293).
func TestSymbolsIndexAfterPush(t *testing.T) {
	t.Setenv("GITBAY_SYMBOLS_TICK", "200ms")
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "symbols", "alice/app", "Serve"); code != 3 ||
		!strings.Contains(errOut, "no symbol index") {
		t.Fatalf("before any push: exit %d: %s", code, errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "server.go"), []byte("package app\n\n// Serve runs.\nfunc Serve() {}\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	var out string
	waitFor(t, "symbol index", func() bool {
		var code int
		out, _, code = inst.ssh(t, aliceKey, "", "repo", "symbols", "alice/app", "Serve", "--json")
		return code == 0
	})
	var res struct {
		Data []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(res.Data) != 1 || res.Data[0].Name != "Serve" || res.Data[0].Kind != "function" ||
		res.Data[0].Path != "server.go" || res.Data[0].Line != 4 {
		t.Fatalf("repo symbols: %s", out)
	}

	// The index follows read access: to bob the repository does not exist.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "symbols", "alice/app", "Serve"); code != 3 ||
		!strings.Contains(errOut, "not found") {
		t.Fatalf("outsider: exit %d: %s", code, errOut)
	}
}
