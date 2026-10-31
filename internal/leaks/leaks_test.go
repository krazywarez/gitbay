package leaks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Token-shaped strings are assembled here rather than written out, so this
// file is not itself a finding for this scanner or any other.
func fake(prefix string, n int, alphabet string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[i%len(alphabet)])
	}
	return b.String()
}

const (
	alnum = "aB3dE5fG7hJ9kL2mN4pQ6rS8tU"
	hex   = "0123456789abcdef"
	upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

func TestFindRules(t *testing.T) {
	keyBody := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ", 2)
	for _, tc := range []struct {
		rule, text string
	}{
		{"private-key", "-----BEGIN " + "OPENSSH PRIVATE KEY-----\n" + keyBody + "\n-----END OPENSSH PRIVATE KEY-----\n"},
		{"private-key", "-----BEGIN " + "PGP PRIVATE KEY BLOCK-----\nVersion: x\n\n" + keyBody + "\n"},
		{"aws-access-key-id", "key = " + fake("AK"+"IA", 16, upper)},
		{"github-token", fake("gh"+"p_", 36, alnum)},
		{"github-fine-grained-token", fake("github"+"_pat_", 82, alnum)},
		{"gitlab-token", fake("gl"+"pat-", 20, alnum)},
		{"gitbay-token", "Authorization: Bearer " + fake("g"+"b_", 64, hex)},
		{"slack-token", "xox" + "b-123456789012-" + fake("", 24, alnum)},
		{"stripe-live-key", fake("sk"+"_live_", 24, alnum)},
		{"google-api-key", fake("AI"+"za", 35, alnum)},
		{"npm-token", fake("np"+"m_", 36, alnum)},
		{"age-secret-key", fake("AGE-SECRET"+"-KEY-1", 58, "QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L")},
		{"digitalocean-token", fake("do"+"p_v1_", 64, hex)},
	} {
		hits := Find([]byte("line one\n" + tc.text + "\n"))
		if len(hits) != 1 || hits[0].Rule != tc.rule || hits[0].Line != 2 {
			t.Errorf("%s: hits = %+v", tc.rule, hits)
		}
	}
}

// Text that mentions a format without carrying a credential is not a
// finding: a stopping rule has to be right when it fires.
func TestFindIgnoresMentions(t *testing.T) {
	for _, text := range []string{
		"pem.Encode(f, &pem.Block{Type: \"PRIVATE KEY\", Bytes: der})",
		"-----BEGIN " + "PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----",
		"AK" + "IAEXAMPLE",
		"tokens start with gh" + "p_ followed by 36 characters",
		"sk_test_" + strings.Repeat("a", 30),
		"gb_ prefix makes leaked tokens findable",
	} {
		if hits := Find([]byte(text)); len(hits) != 0 {
			t.Errorf("%q: hits = %+v", text, hits)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test", "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir string, files map[string]string, msg string) string {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", name)
	}
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

func TestScanRange(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	token := fake("gh"+"p_", 36, alnum)
	first := commit(t, dir, map[string]string{"old.env": "TOKEN=" + token + "\n"}, "old")
	second := commit(t, dir, map[string]string{
		"a b/new.env": "x\nTOKEN=" + token + "\n",
		"binary.dat":  "\x00" + token,
		"clean.txt":   "nothing here\n",
		"old.env":     "TOKEN=" + token + "\n", // unchanged blob: already seen
	}, "new")

	// A push: only what the existing refs do not have.
	rep := Scan(context.Background(), dir, second, "--not", first)
	if rep.Error != "" || rep.Truncated {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Findings) != 1 {
		t.Fatalf("findings = %+v", rep.Findings)
	}
	f := rep.Findings[0]
	if f.Commit != second || f.Path != "a b/new.env" || f.Line != 2 || f.Rule != "github-token" {
		t.Errorf("finding = %+v", f)
	}

	// The history: both files.
	rep = Scan(context.Background(), dir, "--all")
	if len(rep.Findings) != 2 || rep.Findings[1].Path != "old.env" || rep.Findings[1].Commit != first {
		t.Errorf("history findings = %+v", rep.Findings)
	}

	// Nothing selected is an empty report, not an error.
	if rep := Scan(context.Background(), dir, second, "--not", "--all"); rep.Error != "" || len(rep.Findings) != 0 {
		t.Errorf("empty range = %+v", rep)
	}
}

func TestAllowed(t *testing.T) {
	in := []Finding{{Path: "internal/mailin/dkim_test.go"}, {Path: "testdata/k.pem"}, {Path: "config/prod.env"}}
	got := Allowed(in, []string{"testdata/**", "*_test.go", "internal/*/*_test.go"})
	if len(got) != 1 || got[0].Path != "config/prod.env" {
		t.Errorf("allowed = %+v", got)
	}
}
