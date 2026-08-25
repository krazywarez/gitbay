package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleases(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "tag", "-a", "v1.0", "-m", "first")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1.0")

	// Create: tag must exist; duplicates refused.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v9.9"); code != 3 || !strings.Contains(errOut, "push the tag first") {
		t.Fatalf("missing tag: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v1.0",
		"--title", "'First light'", "--notes", "'the **first** release'"); code != 0 {
		t.Fatalf("release create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v1.0"); code != 2 {
		t.Fatal("duplicate release accepted")
	}

	// Edit: notes replace from stdin, absent flags keep their field.
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "edit", "alice/app", "v1.0"); code != 2 {
		t.Fatal("edit with nothing to change accepted")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "the **rebuilt** notes\n", "release", "edit", "alice/app", "v1.0", "--file", "-"); code != 0 {
		t.Fatalf("release edit: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0")
	if !strings.Contains(out, "the **rebuilt** notes") || !strings.Contains(out, "First light") {
		t.Fatalf("edit lost a field:\n%s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "edit", "alice/app", "v1.0", "--title", "'Second light'"); code != 0 {
		t.Fatal("title edit failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0")
	if !strings.Contains(out, "Second light") || !strings.Contains(out, "the **rebuilt** notes") {
		t.Fatalf("title edit lost notes:\n%s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "edit", "alice/app", "v9.9", "--title", "x"); code != 3 {
		t.Fatal("edit of missing release accepted")
	}

	// Assets: upload from stdin, validation, dedup, round-trip.
	payload := "BINARY\x01\x02payload for the release asset\n"
	if _, errOut, code := inst.ssh(t, aliceKey, payload, "release", "asset", "add", "alice/app", "v1.0", "tool-linux-amd64"); code != 0 {
		t.Fatalf("asset add: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, payload, "release", "asset", "add", "alice/app", "v1.0", "tool-linux-amd64"); code != 2 {
		t.Fatal("duplicate asset accepted")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "asset", "add", "alice/app", "v1.0", "empty-file"); code != 2 || !strings.Contains(errOut, "empty asset") {
		t.Fatalf("empty asset: exit %d, %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "x", "release", "asset", "add", "alice/app", "v1.0", "../evil"); code != 2 {
		t.Fatal("bad asset name accepted")
	}
	got, _, code := inst.ssh(t, aliceKey, "", "release", "asset", "get", "alice/app", "v1.0", "tool-linux-amd64")
	if code != 0 || got != payload {
		t.Fatalf("asset round-trip: exit %d, %q", code, got)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0", "--json")
	if !strings.Contains(out, `"name":"tool-linux-amd64"`) ||
		!strings.Contains(out, fmt.Sprintf(`"size":%d`, len(payload))) ||
		!strings.Contains(out, `"sha256":"`) || !strings.Contains(out, "Second light") {
		t.Fatalf("release show: %s", out)
	}

	// Web: page renders notes and assets; download streams exact bytes.
	status, body := inst.get(t, "/alice/app/releases")
	if status != 200 || !strings.Contains(body, "Second light") ||
		!strings.Contains(body, "<strong>rebuilt</strong>") ||
		!strings.Contains(body, "tool-linux-amd64") {
		t.Fatalf("releases page: %d\n%s", status, body)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/app/releases/download/v1.0/tool-linux-amd64", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	dl := make([]byte, len(payload)+10)
	n, _ := resp.Body.Read(dl)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(dl[:n]) != payload ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "tool-linux-amd64") {
		t.Fatalf("web download: %d, %q", resp.StatusCode, dl[:n])
	}
	if resp, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/app/releases/download/v1.0/nope", inst.httpPort)); resp.StatusCode != 404 {
		t.Fatalf("missing asset download: %d", resp.StatusCode)
	}

	// Remove, then delete: DB rows and disk both go.
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "asset", "remove", "alice/app", "v1.0", "tool-linux-amd64"); code != 0 {
		t.Fatal("asset remove failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "asset", "get", "alice/app", "v1.0", "tool-linux-amd64"); code != 3 {
		t.Fatal("removed asset still served")
	}
	if _, _, code := inst.ssh(t, aliceKey, payload, "release", "asset", "add", "alice/app", "v1.0", "again"); code != 0 {
		t.Fatal("re-add failed")
	}
	assetRoot := filepath.Join(inst.root, "repos", "alice", "app.git", "gitbay-releases")
	if fis, err := os.ReadDir(assetRoot); err != nil || len(fis) == 0 {
		t.Fatal("asset dir missing on disk")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "release", "delete", "alice/app", "v1.0", "--yes"); code != 0 {
		t.Fatal("release delete failed")
	}
	if fis, _ := os.ReadDir(assetRoot); len(fis) != 0 {
		t.Fatal("asset files survived release delete")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "release", "list", "alice/app", "--json"); strings.Contains(out, "v1.0") {
		t.Fatalf("deleted release listed: %s", out)
	}

	// Archived repos refuse release writes.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "archive", "alice/app"); code != 0 {
		t.Fatal("archive failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v1.0"); code != 4 || !strings.Contains(errOut, "archived") {
		t.Fatalf("archived release create: exit %d, %s", code, errOut)
	}
}
