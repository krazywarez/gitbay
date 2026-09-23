package httpd

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

func plainServer() *Server {
	cfg := config.Default()
	cfg.Server.SiteURL = "https://forge.test/"
	return &Server{cfg: cfg}
}

func get(t *testing.T, h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Host = "forge.test"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The stylesheet is gzipped for a client that accepts it and served as-is
// for one that does not; both bodies are the same bytes (#232).
func TestStylesheetIsCompressed(t *testing.T) {
	h := plainServer().Handler()
	plain := get(t, h, "/static/style.css", nil)
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("identity: %d %q", plain.Code, plain.Header().Get("Content-Encoding"))
	}
	zipped := get(t, h, "/static/style.css", map[string]string{"Accept-Encoding": "gzip, br"})
	if zipped.Code != 200 || zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip: %d %q vary=%q", zipped.Code, zipped.Header().Get("Content-Encoding"), zipped.Header().Get("Vary"))
	}
	if zipped.Body.Len() >= plain.Body.Len()/2 {
		t.Fatalf("gzip body %d bytes, plain %d", zipped.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !bytes.Equal(body, plain.Body.Bytes()) {
		t.Fatal("gunzipped body differs from the identity body")
	}
	if zipped.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Fatal("ETag changed with encoding")
	}
	// A 304 carries no body to compress and no encoding header.
	notMod := get(t, h, "/static/style.css", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": plain.Header().Get("ETag")})
	if notMod.Code != 304 || notMod.Header().Get("Content-Encoding") != "" {
		t.Fatalf("304: %d %q", notMod.Code, notMod.Header().Get("Content-Encoding"))
	}
}

// A binary type is not touched: the font route keeps its bytes and no
// encoding header.
func TestBinaryResponsesPassThrough(t *testing.T) {
	h := plainServer().Handler()
	w := get(t, h, "/favicon.svg", map[string]string{"Accept-Encoding": "gzip"})
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("svg is text and should gzip: %d %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	rec := httptest.NewRecorder()
	compressed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		w.Write([]byte("0000"))
	})).ServeHTTP(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "0000" {
		t.Fatalf("git transport touched: %q %q", rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
}

// A flush mid-response reaches the connection with what was written so
// far decodable, which is what lets a page stream through gzip.
func TestGzipWriterFlushes(t *testing.T) {
	rec := httptest.NewRecorder()
	h := compressed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<p>first</p>")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		if !rec.Flushed {
			t.Fatal("the flush did not reach the connection")
		}
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("gzip header: %v", err)
		}
		got, _ := io.ReadAll(zr) // no trailer yet: ends in ErrUnexpectedEOF
		if !strings.Contains(string(got), "<p>first</p>") {
			t.Fatalf("flushed body decodes to %q", got)
		}
		io.WriteString(w, "<p>second</p>")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
}
