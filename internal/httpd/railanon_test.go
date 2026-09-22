package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
)

// Below 34rem the rail hides every .railopt square and the More menu
// holds them instead — but that menu is rendered only for a signed-in
// viewer. A square a signed-out visitor can use therefore cannot be
// .railopt: it would hide with nothing to hold it. Explore was, so a
// signed-out phone had no route to the listing from any page (#248).
func TestSignedOutRailDropsNothing(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "explore.html", struct {
		basePage
		Tab    string
		Query  string
		Facets []facetGroup
		Repos  []describedRepo
	}{basePage{Site: "gitbay"}, "explore", "", nil, nil})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	end := strings.Index(out, "</nav>")
	if end < 0 {
		t.Fatalf("no rail in the rendered page:\n%s", out)
	}
	rail := out[:end]
	if strings.Contains(rail, "railopt") {
		t.Errorf("a signed-out rail marks a square railopt, with no More menu to hold it:\n%s", rail)
	}
	if !strings.Contains(rail, `href="/explore"`) {
		t.Errorf("a signed-out rail carries no Explore square:\n%s", rail)
	}
}
