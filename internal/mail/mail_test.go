package mail

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// fakeRelay is an SMTP server that never offers STARTTLS. Given a TLS
// config it speaks TLS from the first byte, as a port-465 relay does.
type fakeRelay struct {
	addr string
	mu   sync.Mutex
	data []string
}

func startRelay(t *testing.T, tlsCfg *tls.Config) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeRelay{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeRelay) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 fake\r\n")
	var body strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case inData && line == ".":
			f.mu.Lock()
			f.data = append(f.data, body.String())
			f.mu.Unlock()
			inData = false
			fmt.Fprint(conn, "250 ok\r\n")
		case inData:
			body.WriteString(line + "\n")
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			fmt.Fprint(conn, "250-fake\r\n250 SIZE 1000000\r\n")
		case line == "DATA":
			inData = true
			fmt.Fprint(conn, "354 go\r\n")
		case line == "QUIT":
			fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 ok\r\n")
		}
	}
}

func (f *fakeRelay) delivered() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data)
}

func mailCfg(host string) config.Config {
	var cfg config.Config
	cfg.Mail.SMTPHost, cfg.Mail.From = host, "gitbay@example.test"
	return cfg
}

func TestRequireTLSRefusesPlaintextRelay(t *testing.T) {
	relay := startRelay(t, nil)
	cfg := mailCfg(relay.addr)
	on := true
	cfg.Mail.RequireTLS = &on
	err := Send(cfg, "a@example.test", "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("Send = %v, want a refusal naming STARTTLS", err)
	}
	if n := relay.delivered(); n != 0 {
		t.Fatalf("%d message(s) sent in clear", n)
	}
}

// A loopback relay has no network to cross; the default leaves it in
// clear, which is what the e2e suite's fake relay relies on.
func TestLoopbackRelayDefaultsToPlaintext(t *testing.T) {
	relay := startRelay(t, nil)
	if err := Send(mailCfg(relay.addr), "a@example.test", "subject", "body"); err != nil {
		t.Fatal(err)
	}
	if n := relay.delivered(); n != 1 {
		t.Fatalf("delivered %d, want 1", n)
	}
}

func TestImplicitTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	defer ts.Close()
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	prev := rootCAs
	rootCAs = pool
	defer func() { rootCAs = prev }()

	relay := startRelay(t, &tls.Config{Certificates: ts.TLS.Certificates})
	cfg := mailCfg(relay.addr)
	cfg.Mail.TLS = "implicit"
	on := true
	cfg.Mail.RequireTLS = &on
	if err := Send(cfg, "a@example.test", "subject", "body"); err != nil {
		t.Fatal(err)
	}
	if n := relay.delivered(); n != 1 {
		t.Fatalf("delivered %d, want 1", n)
	}
}

func TestReplyToHeader(t *testing.T) {
	relay := startRelay(t, nil)
	cfg := mailCfg(relay.addr)
	if err := SendReplyTo(cfg, "a@example.test", "reply+tok@example.test", "subject", "body"); err != nil {
		t.Fatal(err)
	}
	if err := Send(cfg, "a@example.test", "subject", "body"); err != nil {
		t.Fatal(err)
	}
	relay.mu.Lock()
	with, without := relay.data[0], relay.data[1]
	relay.mu.Unlock()
	if !strings.Contains(with, "\nReply-To: reply+tok@example.test\n") {
		t.Fatalf("no Reply-To:\n%s", with)
	}
	if strings.Contains(without, "Reply-To:") {
		t.Fatalf("Send added a Reply-To:\n%s", without)
	}
	if err := SendReplyTo(cfg, "a@example.test", "x@example.test\r\nBcc: b@example.test", "s", "b"); err == nil {
		t.Fatal("a line break in the reply address was sent")
	}
}
