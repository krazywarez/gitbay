package control

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestSecretScanSettings(t *testing.T) {
	st, repo, u, root := browseFixture(t)
	p := repo.Path()
	dispatchIn(t, st, u, root, "", "repo", "settings", "secret-scan", p, "block")
	dispatchIn(t, st, u, root, "", "repo", "settings", "secret-scan-allow", p, "testdata/**", "*.pem", "*.pem")
	got, err := st.RepoByID(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.SecretScan != "block" || strings.Join(got.Settings.SecretScanAllow, " ") != "testdata/** *.pem" {
		t.Fatalf("settings = %+v", got.Settings)
	}
	show := dispatchIn(t, st, u, root, "", "repo", "settings", "show", p)
	if !strings.Contains(show, "secret scan             block") || !strings.Contains(show, "testdata/**, *.pem") {
		t.Errorf("show:\n%s", show)
	}
	dispatchIn(t, st, u, root, "", "repo", "settings", "secret-scan-allow", p)
	if got, _ := st.RepoByID(repo.ID); len(got.Settings.SecretScanAllow) != 0 {
		t.Errorf("allowlist not cleared: %v", got.Settings.SecretScanAllow)
	}
}

func TestSecretScanHistory(t *testing.T) {
	st, repo, u, root := browseFixture(t)
	p := repo.Path()
	src, dir := filepath.Join(root, "src"), RepoDir(root, repo.OwnerName, repo.Name)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitTestEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Assembled, so this file is not itself a finding.
	token := "gh" + "p_" + strings.Repeat("aB3dE5", 6)
	os.MkdirAll(filepath.Join(src, "testdata"), 0o755)
	os.WriteFile(filepath.Join(src, "deploy.env"), []byte("# deploy\nGITHUB_TOKEN="+token+"\n"), 0o644)
	os.WriteFile(filepath.Join(src, "testdata", "fixture.env"), []byte(token+"\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "config")
	git(dir, "fetch", "-q", src, "main:main")

	var got struct {
		Data SecretScanOut `json:"data"`
	}
	if err := json.Unmarshal([]byte(dispatchIn(t, st, u, root, "", "repo", "secret-scan", p, "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data.Findings) != 2 || got.Data.Findings[0].Path != "deploy.env" || got.Data.Findings[0].Line != 2 {
		t.Fatalf("findings = %+v", got.Data.Findings)
	}
	if out := dispatchIn(t, st, u, root, "", "repo", "secret-scan", p, "--json"); strings.Contains(out, token) {
		t.Error("output carries the matched value")
	}

	if _, err := st.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) { s.SecretScanAllow = []string{"testdata/**"} }); err != nil {
		t.Fatal(err)
	}
	plain := dispatchIn(t, st, u, root, "", "repo", "secret-scan", p)
	if !strings.Contains(plain, "findings   1") || strings.Contains(plain, "testdata/") {
		t.Errorf("allowlisted plain:\n%s", plain)
	}
	out := atTerminalIn(t, st, u, root, "repo", "secret-scan", p)
	if !strings.Contains(out, "Findings (1)") || !strings.Contains(out, "deploy.env:2") {
		t.Errorf("screen:\n%s", out)
	}
	checkLegend(t, out)
}
