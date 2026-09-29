package imapc

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeServer answers the commands this client sends from a map of UID to
// message. It records the commands it saw.
type fakeServer struct {
	msgs map[uint32]string
	seen map[uint32]bool
	cmds []string
}

func (f *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "* OK fake ready\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		tag, cmd, _ := strings.Cut(line, " ")
		f.cmds = append(f.cmds, cmd)
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "STARTTLS"):
			fmt.Fprintf(conn, "%s OK begin\r\n", tag)
			tc := tls.Server(conn, serverTLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, r = tc, bufio.NewReader(tc)
		case strings.HasPrefix(up, "LOGIN"):
			if cmd != `LOGIN "u" "p\"w"` {
				fmt.Fprintf(conn, "%s NO [AUTHENTICATIONFAILED] %s\r\n", tag, cmd)
				continue
			}
			fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1\r\n%s OK logged in\r\n", tag)
		case strings.HasPrefix(up, "SELECT"), strings.HasPrefix(up, "EXAMINE"):
			fmt.Fprintf(conn, "* %d EXISTS\r\n* 0 RECENT\r\n%s OK done\r\n", len(f.msgs), tag)
		case up == "UID SEARCH UNSEEN":
			var ids []string
			for uid := range f.msgs {
				if !f.seen[uid] {
					ids = append(ids, fmt.Sprint(uid))
				}
			}
			fmt.Fprintf(conn, "* SEARCH %s\r\n%s OK done\r\n", strings.Join(ids, " "), tag)
		case strings.HasPrefix(up, "UID FETCH"):
			var uid uint32
			fmt.Sscanf(cmd, "UID FETCH %d", &uid)
			m := f.msgs[uid]
			// FLAGS ahead of the body and UID after it, as some servers order them.
			fmt.Fprintf(conn, "* 1 FETCH (FLAGS () BODY[] {%d}\r\n%s UID %d)\r\n%s OK done\r\n", len(m), m, uid, tag)
		case strings.HasPrefix(up, "UID STORE"):
			var uid uint32
			fmt.Sscanf(cmd, "UID STORE %d", &uid)
			f.seen[uid] = true
			fmt.Fprintf(conn, "%s OK done\r\n", tag)
		case up == "LOGOUT":
			fmt.Fprintf(conn, "* BYE\r\n%s OK bye\r\n", tag)
			return
		default:
			fmt.Fprintf(conn, "%s BAD unknown\r\n", tag)
		}
	}
}

var serverTLS *tls.Config

func setupTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	prev := rootCAs
	rootCAs = pool
	t.Cleanup(func() { rootCAs = prev })
	serverTLS = &tls.Config{Certificates: ts.TLS.Certificates}
}

func listen(t *testing.T, f *fakeServer, implicit bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		ln = tls.NewListener(ln, serverTLS)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	// The test certificate is for example.com and 127.0.0.1.
	return ln.Addr().String()
}

func TestSession(t *testing.T) {
	setupTLS(t)
	body := "From: a@example.test\r\nSubject: x\r\n\r\nhello {3}\r\n"
	for _, starttls := range []bool{false, true} {
		f := &fakeServer{msgs: map[uint32]string{7: body, 9: "other"}, seen: map[uint32]bool{9: true}}
		c, err := Dial(listen(t, f, !starttls), starttls, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Login("u", `p"w`); err != nil {
			t.Fatal(err)
		}
		n, err := c.Select("INBOX")
		if err != nil || n != 2 {
			t.Fatalf("Select = %d, %v", n, err)
		}
		uids, err := c.Unseen()
		if err != nil || len(uids) != 1 || uids[0] != 7 {
			t.Fatalf("Unseen = %v, %v", uids, err)
		}
		got, err := c.Fetch(7)
		if err != nil || string(got) != body {
			t.Fatalf("Fetch = %q, %v", got, err)
		}
		if err := c.MarkSeen(7); err != nil {
			t.Fatal(err)
		}
		if uids, _ := c.Unseen(); len(uids) != 0 {
			t.Fatalf("still unseen: %v", uids)
		}
		c.Close()
		if !strings.Contains(strings.Join(f.cmds, "\n"), "UID FETCH 7 BODY.PEEK[]") {
			t.Fatalf("fetch did not peek: %v", f.cmds)
		}
		if starttls && f.cmds[0] != "STARTTLS" {
			t.Fatalf("first command %q, want STARTTLS before LOGIN", f.cmds[0])
		}
	}
}

func TestLoginRefusedHidesServerText(t *testing.T) {
	setupTLS(t)
	f := &fakeServer{msgs: map[uint32]string{}, seen: map[uint32]bool{}}
	c, err := Dial(listen(t, f, true), false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Login("u", "secret")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Login = %v", err)
	}
	if err := c.Login("u", "bad\r\npass"); err == nil {
		t.Fatal("a line break in the password was sent")
	}
}

// A server with a certificate the client does not trust is refused.
func TestUntrustedCertificate(t *testing.T) {
	setupTLS(t)
	f := &fakeServer{msgs: map[uint32]string{}, seen: map[uint32]bool{}}
	addr := listen(t, f, true)
	rootCAs = x509.NewCertPool()
	if _, err := Dial(addr, false, 5*time.Second); err == nil {
		t.Fatal("dialled a server with an untrusted certificate")
	}
}

func TestLiteralSize(t *testing.T) {
	for line, want := range map[string]int64{
		"* 1 FETCH (BODY[] {12}": 12,
		"* 1 FETCH (BODY[] {5+}": 5,
		"* OK {x}":               -1,
		"* OK done":              -1,
	} {
		n, ok := literalSize(line)
		if (want < 0) == ok || (ok && n != want) {
			t.Errorf("literalSize(%q) = %d, %v", line, n, ok)
		}
	}
}
