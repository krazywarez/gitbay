package e2e

import (
	"bytes"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseAssetUploadFromTheWeb uploads a release asset through the
// releases page's multipart form. The bytes match what the CLI reads back
// and what the download route serves, and an upload over max_asset_bytes
// stores nothing (#296).
func TestReleaseAssetUploadFromTheWeb(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n[limits]\nmax_asset_bytes = 4096\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "tag", "-a", "v1.0", "-m", "first")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1.0")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v1.0"); code != 0 {
		t.Fatalf("release create: %s", errOut)
	}
	alice := inst.login(t, aliceKey)
	base := inst.base() + "/alice/app"

	send := func(name string, data []byte) (int, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("tag", "v1.0")
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		resp, err := alice.Post(base+"/releases/assets", mw.FormDataContentType(), &buf)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		out.ReadFrom(resp.Body)
		return resp.StatusCode, out.String()
	}

	payload := []byte("BINARY\x00\x01\x02 asset from the browser\n")
	if status, page := send("tool-linux-amd64", payload); status != 200 || !strings.Contains(page, "tool-linux-amd64") {
		t.Fatalf("upload: %d\n%s", status, page)
	}
	got, _, code := inst.ssh(t, aliceKey, "", "release", "asset", "get", "alice/app", "v1.0", "tool-linux-amd64")
	if code != 0 || got != string(payload) {
		t.Fatalf("CLI read back %q (exit %d)", got, code)
	}
	if status, body := browserGet(t, alice, base+"/releases/download/v1.0/tool-linux-amd64"); status != 200 || body != string(payload) {
		t.Fatalf("download: %d %q", status, body)
	}

	if _, page := send("big.bin", bytes.Repeat([]byte("x"), 8192)); !strings.Contains(page, "max_asset_bytes") {
		t.Fatalf("oversized upload not refused:\n%s", page)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0", "--json")
	if strings.Contains(out, "big.bin") {
		t.Fatalf("oversized asset stored: %s", out)
	}

	if status, _ := browserPost(t, alice, base+"/releases/assets", map[string][]string{
		"action": {"remove"}, "tag": {"v1.0"}, "name": {"tool-linux-amd64"}, "confirm": {"tool-linux-amd64"}}); status != 200 {
		t.Fatal("remove failed")
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0", "--json"); strings.Contains(out, "tool-linux-amd64") {
		t.Fatalf("asset still listed: %s", out)
	}
}
