package termtext

import (
	"os"
	"strings"
	"testing"
)

func TestOrgGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/doc.org")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "doc.org.plain.golden", Org(string(src), Options{Base: "https://forge.test"}))
	golden(t, "doc.org.60.golden", Org(string(src), Options{Width: 60, Base: "https://forge.test"}))
	golden(t, "doc.org.60color.golden", Org(string(src), Options{Width: 60, Color: true, Base: "https://forge.test"}))
}

// #+INCLUDE reads nothing from the server's disk.
func TestOrgIncludeIsInert(t *testing.T) {
	got := Org("#+INCLUDE: \"/etc/passwd\"\n\ntext\n", Options{})
	if strings.Contains(got, "root:") {
		t.Fatalf("include read a file: %q", got)
	}
}

func TestOrgInlineDropsLinkTargets(t *testing.T) {
	got := Inline("see [[https://x.test/a][the page]] now", "org")
	if got != "see the page now" {
		t.Errorf("Inline = %q", got)
	}
}
