package e2e

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestACMEServe verifies the acme wiring offline: the HTTPS listener is up
// with autocert answering handshakes, and the port-80-style helper listener
// serves redirects. Actual issuance needs a reachable CA and a public DNS
// name, which a test cannot have; what matters here is that the plumbing is
// correct and failure to issue does not kill the daemon.
func TestACMEServe(t *testing.T) {
	inst := startInstanceWith(t, "") // helper for binary + keys; killed below
	inst.proc.Process.Kill()
	inst.proc.Wait()

	httpsPort := freePort(t)
	acmeHTTPPort := freePort(t)
	cfg := fmt.Sprintf(`
[server]
root = %q
site_url = "https://gitbay.example"
[ssh]
port = %d
[http]
addr = "127.0.0.1:%d"
tls = "acme"
acme_email = "noreply@gitbay.example"
acme_http_addr = "127.0.0.1:%d"
`, inst.root, inst.port, httpsPort, acmeHTTPPort)
	if err := os.WriteFile(inst.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	inst.proc = exec.Command(inst.gitbayd, "--config", inst.config, "serve")
	inst.proc.Stderr = os.Stderr
	if err := inst.proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.proc.Process.Kill(); inst.proc.Wait() })

	wait := func(port int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("port %d never came up", port)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	wait(httpsPort)
	wait(acmeHTTPPort)

	// The helper listener redirects everything to the canonical HTTPS host.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/repo/log?x=1", acmeHTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently ||
		resp.Header.Get("Location") != "https://gitbay.example/alice/repo/log?x=1" {
		t.Fatalf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// A TLS handshake reaches autocert, which tries (and fails) to issue —
	// the handshake errors, the daemon survives, the listener stays up.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp",
		fmt.Sprintf("127.0.0.1:%d", httpsPort),
		&tls.Config{ServerName: "gitbay.example", InsecureSkipVerify: true})
	if err == nil {
		conn.Close()
		t.Fatal("handshake unexpectedly succeeded with no CA reachable")
	}
	wait(httpsPort) // still listening after the failed handshake

	// Certificates cache under the server root.
	if _, err := os.Stat(filepath.Join(inst.root, "acme")); err != nil {
		t.Fatalf("acme cache dir: %v", err)
	}

	// A host outside the whitelist is refused before any issuance attempt.
	conn2, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp",
		fmt.Sprintf("127.0.0.1:%d", httpsPort),
		&tls.Config{ServerName: "evil.example", InsecureSkipVerify: true})
	if err == nil {
		conn2.Close()
		t.Fatal("handshake for non-whitelisted host succeeded")
	}
}
