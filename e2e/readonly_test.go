package e2e

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"gitbay.org/gitbay/internal/control"
)

// ReadOnly is one flag with four consequences: a read-scoped token may run
// the command, GET /api/v1/read reaches it, it draws on the read rate
// budget, and it is not audited. A mutating command mis-flagged ReadOnly
// becomes GET-able and unaudited in one line. This test runs every
// ReadOnly command against a populated instance and fails on any command
// that changes a row (#97).
//
// Every ReadOnly command needs an entry in readArgs; a new one without
// arguments here fails the test rather than going untested.
func TestReadOnlyCommandsWriteNothing(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n[webhooks]\nallow_local = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified", "--admin")
	deployKey := inst.newKey(t, "deploy")
	must := func(stdin string, args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, stdin, args...)
		if code != 0 {
			t.Fatalf("fixture %v: exit %d\n%s%s", args, code, out, errOut)
		}
		return out
	}

	// A repository with history, a tag, a branch, a build, and everything
	// the read commands can look at.
	must("", "repo", "create", "alice/app")
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  ok:\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# app\n\nhello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("package app\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "tag", "v1")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1")
	sha := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("package app\n\nvar V = 1\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")

	must("", "issue", "create", "alice/app", "--title", "one", "--body", "body")
	must("", "issue", "label", "alice/app", "1", "--add", "bug")
	must("", "label", "set", "alice/app", "bug", "--color", "ff0000")
	must("", "milestone", "create", "alice/app", "m1")
	must("", "mr", "create", "alice/app", "--source", "feat", "--target", "main", "--title", "change")
	must("", "mr", "diff-comment", "alice/app", "1", "--path", "f.go", "--line", "3", "--message", "why")
	must("", "status", "set", "alice/app", sha, "--context", "ci/x", "--state", "success")
	must("", "release", "create", "alice/app", "v1", "--title", "first")
	must("data\n", "release", "asset", "add", "alice/app", "v1", "a.txt")
	must("", "org", "create", "theorg")
	must("", "org", "team", "create", "theorg", "core")
	must("", "token", "create", "--name", "t")
	must("", "web", "login")
	pub, _ := os.ReadFile(deployKey + ".pub")
	must(string(pub), "repo", "deploy-key", "add", "alice/app")
	must("secret\n", "repo", "secret", "set", "alice/app", "S")
	// Added last, for an event that already happened: no delivery is
	// pending to be retried while the reads run.
	must("", "webhook", "add", "alice/app", "http://127.0.0.1:1/hook", "--events", "release.created")

	readArgs := map[string][]string{
		"help":                 {},
		"whoami":               {},
		"dashboard":            {},
		"feed":                 {},
		"explore":              {},
		"audit":                {},
		"keys list":            {},
		"pgp list":             {},
		"token list":           {},
		"web sessions list":    {},
		"account export":       {},
		"org list":             {},
		"repo list":            {},
		"admin user list":      {},
		"admin runners":        {},
		"admin repo list":      {},
		"admin stats":          {},
		"admin user show":      {"alice"},
		"profile show":         {"alice"},
		"org show":             {"theorg"},
		"org members list":     {"theorg"},
		"org team list":        {"theorg"},
		"org team show":        {"theorg", "core"},
		"repo search":          {"app"},
		"repo show":            {"alice/app"},
		"repo access list":     {"alice/app"},
		"repo settings show":   {"alice/app"},
		"repo topics":          {"alice/app"},
		"repo refs":            {"alice/app"},
		"repo log":             {"alice/app"},
		"repo tree":            {"alice/app"},
		"repo cat":             {"alice/app", "f.go"},
		"repo blame":           {"alice/app", "f.go"},
		"repo grep":            {"alice/app", "hello"},
		"repo diff":            {"alice/app", "main", "feat"},
		"repo commit":          {"alice/app", sha},
		"repo download":        {"alice/app"},
		"repo deploy-key list": {"alice/app"},
		"repo secret list":     {"alice/app"},
		"repo mirror list":     {"alice/app"},
		"repo domain list":     {"alice/app"},
		"repo deps status":     {"alice/app"},
		"status list":          {"alice/app", sha},
		"issue list":           {"alice/app"},
		"issue show":           {"alice/app", "1"},
		"issue templates":      {"alice/app"},
		"label list":           {"alice/app"},
		"milestone list":       {"alice/app"},
		"mr list":              {"alice/app"},
		"mr show":              {"alice/app", "1"},
		"mr diff":              {"alice/app", "1"},
		"mr threads":           {"alice/app", "1"},
		"build list":           {"alice/app"},
		"build jobs":           {"alice/app"},
		"build show":           {"alice/app", "1"},
		"build log":            {"alice/app", "1"},
		"release list":         {"alice/app"},
		"release show":         {"alice/app", "v1"},
		"release asset get":    {"alice/app", "v1", "a.txt"},
		"notifications list":   nil,
		"search":               {"app"},
		"mr revisions":         {"alice/app", "1"},
		"mr range-diff":        {"alice/app", "1"},
		"webhook list":         {"alice/app"},
		"webhook deliveries":   {"alice/app"},
		"wiki list":            {"alice/app"},
		"wiki show":            {"alice/app"},
	}
	// Reads whose subject legitimately does not exist in this fixture.
	notFoundOK := map[string]bool{"wiki show": true, "repo deps status": true}

	dbPath := filepath.Join(inst.root, "gitbay.db")
	before := dbFingerprint(t, dbPath)
	for _, cmd := range control.Commands() {
		if !cmd.ReadOnly {
			continue
		}
		path := strings.Join(cmd.Path, " ")
		args, ok := readArgs[path]
		if !ok {
			t.Errorf("%s is ReadOnly and has no arguments in this test; add an entry", path)
			continue
		}
		_, errOut, code := inst.ssh(t, aliceKey, "", append(append([]string{}, cmd.Path...), args...)...)
		if code != 0 && !(code == 3 && notFoundOK[path]) {
			t.Errorf("%s: exit %d: %s", path, code, strings.TrimSpace(errOut))
		}
		after := dbFingerprint(t, dbPath)
		for table, h := range after {
			// The signature cache is filled by whichever read first shows
			// a commit; a memo of a pure function is not state.
			if table == "commit_signatures" {
				continue
			}
			if before[table] != h {
				t.Errorf("%s is ReadOnly but changed table %s", path, table)
			}
		}
		before = after
	}
}

// dbFingerprint hashes every row of every table, per table. Columns that
// record a read happening (a key's last use, an account's last sight) are
// left out: an ssh session touches them by design.
func dbFingerprint(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string]string{}
	for _, table := range tables {
		cols, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for cols.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
			switch name {
			case "last_used_at", "last_seen", "last_seen_at":
				continue
			}
			names = append(names, fmt.Sprintf("%q", name))
		}
		cols.Close()
		h := sha256.New()
		data, err := db.Query(fmt.Sprintf("SELECT %s FROM %q ORDER BY %s", strings.Join(names, ","), table, strings.Join(names, ",")))
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for data.Next() {
			data.Scan(ptrs...)
			fmt.Fprintf(h, "%v\n", vals)
		}
		data.Close()
		out[table] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}
