package httpd

import (
	"testing"

	"gitbay.org/gitbay/internal/gitutil"
)

// The navigator lists the file's directory, directories first, links each
// entry to its tree or blob page, marks the file itself, and links the
// parent (the tree root when the file is at the top).
func TestFileNavMarksCurrentAndLinksParent(t *testing.T) {
	entries := []gitutil.TreeEntry{
		{Type: "blob", Name: "main.go"},
		{Type: "tree", Name: "sub"},
		{Type: "blob", Name: "util.go"},
	}
	nav := fileNavFor("krz/gitbay", "main", "cmd/gitbay/util.go", entries)
	if nav.Title != "cmd/gitbay" {
		t.Errorf("title = %q", nav.Title)
	}
	if nav.Parent != "/krz/gitbay/tree/main/cmd" {
		t.Errorf("parent = %q", nav.Parent)
	}
	if len(nav.Entries) != 3 || nav.Entries[0].Name != "sub/" || !nav.Entries[0].Dir {
		t.Fatalf("entries not directories-first: %+v", nav.Entries)
	}
	if nav.Entries[0].URL != "/krz/gitbay/tree/main/cmd/gitbay/sub" {
		t.Errorf("dir url = %q", nav.Entries[0].URL)
	}
	if nav.Entries[2].Name != "util.go" || !nav.Entries[2].Current || nav.Entries[2].URL != "/krz/gitbay/blob/main/cmd/gitbay/util.go" {
		t.Errorf("current entry: %+v", nav.Entries[2])
	}
	if nav.Entries[1].Current {
		t.Error("main.go marked current")
	}

	root := fileNavFor("krz/gitbay", "main", "Makefile", []gitutil.TreeEntry{{Type: "blob", Name: "Makefile"}})
	if root.Title != "gitbay" || root.Parent != "" {
		t.Errorf("root nav: title %q parent %q", root.Title, root.Parent)
	}
}
