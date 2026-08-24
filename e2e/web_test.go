package e2e

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/sig"
)

func (i *instance) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", i.httpPort, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestWebUI(t *testing.T) {
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// Public repo with real content: a README, a source file, a tag, and
	// one SSHSIG-signed commit for the badge check.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/site"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/site"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hello site\n\nsome *markdown*\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "first commit")
	mustGit(t, dir, env, "tag", "v1.0")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1.0")

	// A signed commit on top, built with the M4 fixture helpers.
	sshRaw, _ := os.ReadFile(aliceKey)
	signer, err := ssh.ParsePrivateKey(sshRaw)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	buildSignedCommitOn(t, dir, env, head, "signed tip", "alice@example.test", signer)
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Private repo must be invisible everywhere.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatal("create private failed")
	}

	// Index lists the public repo, not the private one.
	status, body := inst.get(t, "/")
	if status != 200 || !strings.Contains(body, "alice/site") {
		t.Fatalf("index: %d\n%s", status, body)
	}
	if strings.Contains(body, "secret") {
		t.Fatal("index leaks private repo")
	}

	// Repo home: tree entries plus rendered README.
	status, body = inst.get(t, "/alice/site")
	if status != 200 || !strings.Contains(body, "src/") || !strings.Contains(body, "README.md") {
		t.Fatalf("repo home: %d\n%s", status, body)
	}
	if !strings.Contains(body, "<h1>hello site</h1>") || !strings.Contains(body, "<em>markdown</em>") {
		t.Fatalf("README not rendered:\n%s", body)
	}

	// Subdirectory tree and blob with highlighting.
	status, body = inst.get(t, "/alice/site/tree/main/src")
	if status != 200 || !strings.Contains(body, "main.go") {
		t.Fatalf("tree src: %d", status)
	}
	status, body = inst.get(t, "/alice/site/blob/main/src/main.go")
	if status != 200 || !strings.Contains(body, "package") {
		t.Fatalf("blob: %d", status)
	}

	// Raw serves exact bytes with nosniff.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/site/raw/main/src/main.go", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(raw) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("raw bytes: %q", raw)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("raw missing nosniff")
	}

	// Log: both commits, with badges matching the M4 states exactly.
	status, body = inst.get(t, "/alice/site/log")
	if status != 200 {
		t.Fatalf("log: %d", status)
	}
	if !strings.Contains(body, "badge-verified") || !strings.Contains(body, "signed tip") {
		t.Fatalf("log missing verified badge:\n%s", body)
	}
	if !strings.Contains(body, "badge-unsigned") || !strings.Contains(body, "first commit") {
		t.Fatalf("log missing unsigned badge:\n%s", body)
	}

	// Commit page for the signed tip.
	tip := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	status, body = inst.get(t, "/alice/site/commit/"+tip)
	if status != 200 || !strings.Contains(body, "badge-verified") || !strings.Contains(body, "alice") {
		t.Fatalf("commit page: %d\n%s", status, body)
	}

	// Refs page shows branch and tag.
	status, body = inst.get(t, "/alice/site/refs")
	if status != 200 || !strings.Contains(body, "main") || !strings.Contains(body, "v1.0") {
		t.Fatalf("refs: %d", status)
	}

	// Archive downloads a valid gzip.
	resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/site/archive/main.tar.gz", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("archive not gzip: %v", err)
	}
	tarBytes, _ := io.ReadAll(gz)
	resp.Body.Close()
	if !strings.Contains(string(tarBytes), "README.md") {
		t.Fatal("archive missing content")
	}

	// README formats: org-mode renders, HTML renders sanitized, unknown
	// extensions fall back to plaintext, and richer formats win conflicts.
	readmeRepo := func(name, file, content string) {
		t.Helper()
		if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/"+name); code != 0 {
			t.Fatalf("repo create %s failed", name)
		}
		w := t.TempDir()
		mustGit(t, w, env, "clone", inst.sshURL("alice/"+name), "r")
		d := filepath.Join(w, "r")
		os.WriteFile(filepath.Join(d, file), []byte(content), 0o644)
		mustGit(t, d, env, "checkout", "-q", "-b", "main")
		mustGit(t, d, env, "add", ".")
		mustGit(t, d, env, "commit", "-q", "-m", "readme")
		mustGit(t, d, env, "push", "-q", "origin", "main")
	}

	readmeRepo("orgdoc", "README.org", "* Heading\n\nSome /emphasis/ here.\n")
	status, body = inst.get(t, "/alice/orgdoc")
	if status != 200 || !strings.Contains(body, "headline-1") || !strings.Contains(body, "<em>emphasis</em>") {
		t.Fatalf("org README not rendered:\n%s", body)
	}

	readmeRepo("htmldoc", "README.html", "<p id=\"ok\">fine</p><script>alert(1)</script>")
	status, body = inst.get(t, "/alice/htmldoc")
	if status != 200 || !strings.Contains(body, "fine</p>") {
		t.Fatalf("html README not rendered:\n%s", body)
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("repo HTML script survived sanitization")
	}

	readmeRepo("txtdoc", "README.txt", "plain <text> & stuff\n")
	status, body = inst.get(t, "/alice/txtdoc")
	if status != 200 || !strings.Contains(body, "plain &lt;text&gt; &amp; stuff") {
		t.Fatalf("txt README not escaped-plaintext:\n%s", body)
	}

	// Private repo pages: 404, indistinguishable from nonexistent.
	for _, p := range []string{"/alice/secret", "/alice/secret/log", "/alice/nothere"} {
		if status, _ := inst.get(t, p); status != 404 {
			t.Errorf("GET %s = %d, want 404", p, status)
		}
	}
}

// buildSignedCommitOn adds one SSHSIG-signed commit on top of parent,
// reusing the M4 fixture machinery.
func buildSignedCommitOn(t *testing.T, dir string, env []string, parent, subject, email string, signer ssh.Signer) {
	t.Helper()
	tree := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", parent+"^{tree}"))
	specs := []commitSpec{{
		authorEmail: email,
		subject:     subject,
		sign: func(p []byte) string {
			s, err := sig.MarshalSSHSig(signer, p)
			if err != nil {
				t.Fatal(err)
			}
			return string(s)
		},
	}}
	buildChain(t, dir, env, tree, parent, specs)
}
