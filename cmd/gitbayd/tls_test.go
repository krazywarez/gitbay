package main

import (
	"crypto/tls"
	"testing"
)

// The floor is stated in code rather than inherited from the Go
// release the binary was built with (#281).
func TestServerTLSMinimum(t *testing.T) {
	if got := serverTLS(nil).MinVersion; got != tls.VersionTLS12 {
		t.Fatalf("files mode: MinVersion %#x, want %#x", got, tls.VersionTLS12)
	}
	// autocert's config carries the ALPN protocols TLS-ALPN-01 needs;
	// setting the floor must keep them.
	base := &tls.Config{NextProtos: []string{"h2", "http/1.1", "acme-tls/1"}}
	got := serverTLS(base)
	if got.MinVersion != tls.VersionTLS12 || len(got.NextProtos) != 3 {
		t.Fatalf("acme mode: %+v", got)
	}
}
