package httpd

import (
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
)

// Raw content carries its blob id as an ETag: a branch is revalidated and
// answered with a 304 while the blob is unchanged, a commit sha is
// immutable, and a different blob gets a different tag.
func TestRawCaching(t *testing.T) {
	s := symbolServer(t)
	h := s.Handler()

	w := get(t, h, "/alice/app/raw/main/main.go", nil)
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatalf("raw: %d, %d bytes", w.Code, w.Body.Len())
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if got := w.Header().Get("Cache-Control"); got != "public, no-cache" {
		t.Errorf("branch Cache-Control = %q", got)
	}

	w = get(t, h, "/alice/app/raw/main/main.go", map[string]string{"If-None-Match": etag})
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Errorf("revalidation: %d, %d bytes", w.Code, w.Body.Len())
	}
	w = get(t, h, "/alice/app/raw/main/main.go", map[string]string{"If-None-Match": `W/"0000", ` + etag})
	if w.Code != 304 {
		t.Errorf("weak list revalidation: %d", w.Code)
	}

	sha, err := gitutil.ResolveRef(control.RepoDir(s.cfg.Server.Root, "alice", "app"), "main")
	if err != nil {
		t.Fatal(err)
	}
	w = get(t, h, "/alice/app/raw/"+sha+"/main.go", nil)
	if w.Code != 200 {
		t.Fatalf("raw at sha: %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("sha Cache-Control = %q", got)
	}
	if w.Header().Get("ETag") != etag {
		t.Errorf("same blob, different ETag: %q vs %q", w.Header().Get("ETag"), etag)
	}

	w = get(t, h, "/alice/app/raw/main/a/dup.go", map[string]string{"If-None-Match": etag})
	if w.Code != 200 || w.Header().Get("ETag") == etag {
		t.Errorf("another blob: %d, ETag %q", w.Code, w.Header().Get("ETag"))
	}

	if w := get(t, h, "/alice/app/raw/main/a", nil); w.Code != 404 {
		t.Errorf("a tree path: %d", w.Code)
	}
}
