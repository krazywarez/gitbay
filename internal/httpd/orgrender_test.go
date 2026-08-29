package httpd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Org is rendered by go-org, whose default configuration reads #+INCLUDE: and
// #+SETUPFILE: targets straight off disk. The content being rendered is not
// trusted — a README or wiki page is whatever someone pushed — so those
// keywords must never reach the filesystem.
//
// The leaking form is `#+INCLUDE: "<path>" src <lang>`: the file becomes a
// source block, which survives the sanitizer as a chroma-highlighted <pre>.

const orgSecret = "SENTINEL-SERVER-SIDE-SECRET"

func secretFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte(orgSecret), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	return path
}

func TestOrgIncludeDoesNotReadAbsolutePaths(t *testing.T) {
	path := secretFile(t)
	for _, kind := range []string{"src text", "example", "export html"} {
		src := "#+INCLUDE: \"" + path + "\" " + kind + "\n"
		out := string(renderReadme("README.org", []byte(src)))
		if strings.Contains(out, orgSecret) {
			t.Errorf("#+INCLUDE %q read a server file into the page:\n%s", kind, out)
		}
	}
}

// A relative include resolves against filepath.Dir(document path). renderReadme
// passes a bare filename, so that directory is the daemon's working directory
// and traversal reaches anything above it. go.mod is a stand-in for any file
// the daemon can read but a reader should not see.
func TestOrgIncludeDoesNotTraverseRelativePaths(t *testing.T) {
	src := "#+INCLUDE: \"../../go.mod\" src text\n"

	out := string(renderReadme("README.org", []byte(src)))

	if strings.Contains(out, "module gitbay.org/gitbay") {
		t.Fatalf("relative #+INCLUDE traversed out of the working directory:\n%s", out)
	}
}

// The guard itself, asserted directly. #+SETUPFILE: reads at parse time and
// folds the result into buffer settings rather than printing it, so a rendered
// page is a weak place to observe that read; this is not.
func TestOrgConfigRefusesToReadFiles(t *testing.T) {
	path := secretFile(t)

	if _, err := orgConfig().ReadFile(path); err == nil {
		t.Fatal("orgConfig().ReadFile opened a file; #+INCLUDE and #+SETUPFILE must be refused")
	}
}

// #+SETUPFILE: reads at parse time and folds the result into buffer settings.
// It does not print the file, but it still reads it, and anything it defines —
// a macro, say — becomes observable in the output.
func TestOrgSetupFileDoesNotReadServerFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.org")
	if err := os.WriteFile(path, []byte("#+MACRO: leak "+orgSecret+"\n"), 0o600); err != nil {
		t.Fatalf("write setup file: %v", err)
	}
	src := "#+SETUPFILE: " + path + "\n\n{{{leak}}}\n"

	out := string(renderReadme("README.org", []byte(src)))

	if strings.Contains(out, orgSecret) {
		t.Fatalf("#+SETUPFILE read a server file:\n%s", out)
	}
}

// The keyword itself is harmless text; only the file read is the problem. A
// document that uses it should still render everything else.
func TestOrgIncludeLeavesTheRestOfTheDocumentIntact(t *testing.T) {
	src := "* Real Heading\n\n#+INCLUDE: \"/etc/passwd\" src text\n\nBody text.\n"

	out := string(renderReadme("README.org", []byte(src)))

	if !strings.Contains(out, "Real Heading") {
		t.Errorf("heading missing from output:\n%s", out)
	}
	if !strings.Contains(out, "Body text.") {
		t.Errorf("body missing from output:\n%s", out)
	}
	if strings.Contains(out, "root:") {
		t.Errorf("include read /etc/passwd:\n%s", out)
	}
}

// Ordinary org must keep rendering exactly as before.
func TestOrgRenderingIsUnaffectedByTheIncludeGuard(t *testing.T) {
	src := "* Heading\n\nSome /emphasis/ and =code=.\n\n#+BEGIN_SRC go\nfmt.Println(\"hi\")\n#+END_SRC\n"

	out := string(renderReadme("README.org", []byte(src)))

	// The source block is chroma-highlighted, so its text is split across spans;
	// check the block and a token rather than the joined source line.
	for _, want := range []string{"Heading", "<em>emphasis</em>", "<code>code</code>",
		`<pre class="chroma">`, "Println"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}
