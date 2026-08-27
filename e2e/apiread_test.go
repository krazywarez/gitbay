package e2e

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoTreeAndCat covers reading repository contents over the control
// plane — the capability a native client needs and could not reach at all
// before: no command returned file contents, and the web's raw route
// authenticates by session cookie, not bearer token.
func TestRepoTreeAndCat(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# app\n\nhello\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "logo.bin"), []byte{0x00, 0x01, 0x02, 0xff, 0x00}, 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Tree at the root: directories and files, with sizes and object ids.
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "tree", "alice/app", "--json")
	if code != 0 {
		t.Fatalf("repo tree: %s", errOut)
	}
	var tree struct {
		Data struct {
			Ref     string `json:"ref"`
			Dir     string `json:"dir"`
			Entries []struct {
				Name string `json:"name"`
				Type string `json:"type"`
				SHA  string `json:"sha"`
				Size int64  `json:"size"`
			} `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatalf("tree JSON: %v\n%s", err, out)
	}
	if tree.Data.Ref != "main" {
		t.Errorf("ref = %q, want main", tree.Data.Ref)
	}
	byName := map[string]string{}
	for _, e := range tree.Data.Entries {
		byName[e.Name] = e.Type
		if e.SHA == "" {
			t.Errorf("%s has no object id, so a client cannot cache it", e.Name)
		}
		if e.Type == "blob" && e.Size == 0 {
			t.Errorf("%s reports no size", e.Name)
		}
	}
	if byName["src"] != "tree" || byName["README.md"] != "blob" {
		t.Fatalf("root listing wrong: %v", byName)
	}

	// A subdirectory, and a file's contents.
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "tree", "alice/app", "src", "--json")
	if !strings.Contains(out, `"main.go"`) {
		t.Errorf("subdirectory listing: %s", out)
	}
	out, errOut, code = inst.ssh(t, aliceKey, "", "repo", "cat", "alice/app", "README.md", "--json")
	if code != 0 {
		t.Fatalf("repo cat: %s", errOut)
	}
	var file struct {
		Data struct {
			File      string `json:"file"`
			Content   string `json:"content"`
			Base64    string `json:"base64"`
			Binary    bool   `json:"binary"`
			Truncated bool   `json:"truncated"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &file); err != nil {
		t.Fatalf("cat JSON: %v\n%q", err, out)
	}
	if file.Data.Content != "# app\n\nhello\n" || file.Data.Binary {
		t.Fatalf("cat returned %+v", file.Data)
	}

	// Binary content comes back base64, never as broken UTF-8 in "content".
	file.Data = struct {
		File      string `json:"file"`
		Content   string `json:"content"`
		Base64    string `json:"base64"`
		Binary    bool   `json:"binary"`
		Truncated bool   `json:"truncated"`
	}{}
	out, errOut, code = inst.ssh(t, aliceKey, "", "repo", "cat", "alice/app", "logo.bin", "--json")
	if code != 0 {
		t.Fatalf("cat binary: exit %d %s", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &file); err != nil {
		t.Fatalf("binary cat JSON: %v\n%q", err, out)
	}
	if !file.Data.Binary || file.Data.Content != "" || file.Data.Base64 == "" {
		t.Fatalf("binary file not base64: %+v", file.Data)
	}
	if raw, err := base64.StdEncoding.DecodeString(file.Data.Base64); err != nil ||
		len(raw) != 5 || raw[3] != 0xff {
		t.Fatalf("base64 does not round-trip: %v %v", raw, err)
	}

	// Plain output is the file itself, so `repo cat` pipes like cat does.
	out, _, code = inst.ssh(t, aliceKey, "", "repo", "cat", "alice/app", "README.md")
	if code != 0 || out != "# app\n\nhello\n" {
		t.Fatalf("plain cat = %q", out)
	}

	// Reads honour repository visibility: this repo is private.
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "tree", "alice/app"); code == 0 {
		t.Error("a stranger listed a private repository's tree")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "cat", "alice/app", "README.md"); code == 0 {
		t.Error("a stranger read a private repository's file")
	}

	// Paths cannot climb out of the repository.
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "src/../../.."} {
		if _, _, code := inst.ssh(t, aliceKey, "", "repo", "cat", "alice/app", bad); code == 0 {
			t.Errorf("path %q was accepted", bad)
		}
	}

	// Missing things are not found rather than server errors.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "cat", "alice/app", "nope.txt"); code != 3 {
		t.Errorf("missing file exit = %d, want 3", code)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "tree", "alice/app", "--ref", "nosuch"); code != 3 {
		t.Errorf("missing ref exit = %d, want 3", code)
	}
}
