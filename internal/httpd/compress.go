package httpd

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// compressed gzips text responses for clients that accept it. Every body
// the forge writes itself is text — pages, the stylesheet, feeds, JSON —
// and none is precompressed; the stylesheet alone went from 67 KB to 15 KB
// (#232). The decision is made when the headers are final, on the content
// type, so git transport, LFS and binary downloads pass through untouched.
func compressed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		cw := &gzipWriter{ResponseWriter: w}
		defer cw.Close()
		next.ServeHTTP(cw, r)
	})
}

var compressibleTypes = []string{"text/", "application/json", "application/atom+xml", "image/svg+xml"}

func compressible(contentType string) bool {
	for _, p := range compressibleTypes {
		if strings.HasPrefix(contentType, p) {
			return true
		}
	}
	return false
}

// gzipWriter decides on the first WriteHeader or Write whether the body
// is compressed, then either wraps the underlying writer or steps aside.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *gzipWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	if status == http.StatusNotModified || status == http.StatusNoContent ||
		h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		return
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	g.gz = gzip.NewWriter(g.ResponseWriter)
}

func (g *gzipWriter) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		// net/http would sniff the type on this write; do it first so the
		// decision sees it.
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.decide(http.StatusOK)
	}
	if g.gz == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

func (g *gzipWriter) Close() {
	if g.gz != nil {
		g.gz.Close()
	}
}

// Flush sends what the gzip stream holds, then flushes the connection, so
// a streamed page reaches the browser as it is written.
func (g *gzipWriter) Flush() {
	g.FlushError()
}

// FlushError is Flush with the error a failed flush produces.
// http.ResponseController.Flush prefers this over Flush when both are
// implemented, so a write that fails partway through a stream is reported
// instead of silently dropped.
func (g *gzipWriter) FlushError() error {
	if !g.decided {
		g.decide(http.StatusOK)
	}
	if g.gz != nil {
		if err := g.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
