package httpd

import (
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// TestCrumbsLinkDirectoriesToTree: on a blob or blame page the parent
// crumbs are directories, so they link to the tree; only the leaf keeps the
// page's kind. They used to carry the page's kind throughout, and
// /blob/<ref>/<dir> is a 404 (#103).
func TestCrumbsLinkDirectoriesToTree(t *testing.T) {
	p := repoPage{Repo: store.Repo{OwnerName: "krz", Name: "gitbay"}, Ref: "main"}
	for _, kind := range []string{"blob", "blame", "tree"} {
		cs := crumbs(p, kind, "internal/control/control.go")
		want := []string{
			"/krz/gitbay/tree/main/internal",
			"/krz/gitbay/tree/main/internal/control",
			"/krz/gitbay/" + kind + "/main/internal/control/control.go",
		}
		if len(cs) != len(want) {
			t.Fatalf("%s: %d crumbs, want %d", kind, len(cs), len(want))
		}
		for i, c := range cs {
			if c.URL != want[i] {
				t.Errorf("%s crumb %d: %s, want %s", kind, i, c.URL, want[i])
			}
		}
	}
	if cs := crumbs(p, "tree", ""); len(cs) != 0 {
		t.Errorf("root: %d crumbs, want none", len(cs))
	}
}
