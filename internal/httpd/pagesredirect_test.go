package httpd

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A redirect target built from the request path must not be able to leave
// the site. `r.URL.Path` keeps a leading `//` — Go's URL parser does not
// normalise it — and `Location: //evil.example/` is protocol-relative, so
// a browser follows it to another origin (gosecurity:S5146, #153).
//
// The directory-redirect targets are derived from the cleaned path, so
// this asserts the property rather than the spelling: whatever the code
// emits, it must be a same-origin path.
func TestPageRedirectCannotLeaveTheSite(t *testing.T) {
	cases := []string{
		"//evil.example",
		"//evil.example/deep",
		"///evil.example",
		"/\\evil.example",
		"//evil.example/../..",
	}
	for _, p := range cases {
		got := pageRedirectTarget(p)
		if got == "" {
			continue // no redirect for this shape is a fine answer
		}
		if strings.HasPrefix(got, "//") || strings.HasPrefix(got, "/\\") {
			t.Errorf("request %q redirects to %q, which a browser reads as another origin", p, got)
		}
		if !strings.HasPrefix(got, "/") {
			t.Errorf("request %q redirects to %q, which is not an absolute path", p, got)
		}
	}
}

// The ordinary case still behaves: a directory URL without a trailing
// slash gains one, so relative links inside the page resolve.
func TestPageRedirectAddsTrailingSlash(t *testing.T) {
	if got, want := pageRedirectTarget("/guide"), "/guide/"; got != want {
		t.Errorf("pageRedirectTarget(/guide) = %q, want %q", got, want)
	}
	if got, want := pageRedirectTarget("/a/b/c"), "/a/b/c/"; got != want {
		t.Errorf("pageRedirectTarget(/a/b/c) = %q, want %q", got, want)
	}
}

// Guard against a regression in the shape the fix relies on: httptest
// builds the request the same way the server sees it.
func TestRawPathKeepsDoubleSlash(t *testing.T) {
	r := httptest.NewRequest("GET", "http://host//evil.example", nil)
	if r.URL.Path != "//evil.example" {
		t.Skipf("net/url normalised the path to %q; the hazard this guards is gone", r.URL.Path)
	}
}
