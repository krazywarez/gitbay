package imapc

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	// raw, when set, answers a command in place of the default: it
	// writes whatever it likes and reports whether it handled it.
	raw func(conn net.Conn, tag, cmd string) bool
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
		if f.raw != nil && f.raw(conn, tag, cmd) {
			continue
		}
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
		case strings.HasPrefix(up, "UID FETCH") && strings.HasSuffix(up, "RFC822.SIZE"):
			var uid uint32
			fmt.Sscanf(cmd, "UID FETCH %d", &uid)
			fmt.Fprintf(conn, "* 1 FETCH (UID %d RFC822.SIZE %d)\r\n%s OK done\r\n", uid, len(f.msgs[uid]), tag)
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

// session dials f over implicit TLS and logs in.
func session(t *testing.T, f *fakeServer) *Client {
	t.Helper()
	setupTLS(t)
	if f.msgs == nil {
		f.msgs, f.seen = map[uint32]string{}, map[uint32]bool{}
	}
	c, err := Dial(listen(t, f, true), false, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Login("u", `p"w`); err != nil {
		t.Fatal(err)
	}
	return c
}

// A server that answers one FETCH with literal after literal is cut off
// at the command's byte budget, however it labels them.
func TestHostileRepeatedLiterals(t *testing.T) {
	chunk := strings.Repeat("x", 10<<20)
	f := &fakeServer{raw: func(conn net.Conn, tag, cmd string) bool {
		if !strings.HasPrefix(cmd, "UID FETCH 1 BODY") {
			return false
		}
		for i := 0; i < 5; i++ {
			if _, err := fmt.Fprintf(conn, "* 1 FETCH (FLAGS {%d}\r\n%s)\r\n", len(chunk), chunk); err != nil {
				return true
			}
		}
		fmt.Fprintf(conn, "%s OK done\r\n", tag)
		return true
	}}
	c := session(t, f)
	f.msgs[1] = "small"
	if _, err := c.Fetch(1); !errors.Is(err, ErrLimit) {
		t.Fatalf("Fetch = %v, want ErrLimit", err)
	}
}

// A body literal over MaxMessage is refused even when RFC822.SIZE lied.
func TestLyingSize(t *testing.T) {
	big := strings.Repeat("x", MaxMessage+10)
	f := &fakeServer{raw: func(conn net.Conn, tag, cmd string) bool {
		if !strings.HasPrefix(cmd, "UID FETCH 1 BODY") {
			return false
		}
		fmt.Fprintf(conn, "* 1 FETCH (BODY[] {%d}\r\n%s)\r\n%s OK done\r\n", len(big), big, tag)
		return true
	}}
	c := session(t, f)
	f.msgs[1] = "small"
	if _, err := c.Fetch(1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Fetch = %v, want ErrTooLarge", err)
	}
}

func TestSizeRefusedBeforeFetch(t *testing.T) {
	f := &fakeServer{}
	c := session(t, f)
	f.msgs[1] = strings.Repeat("x", MaxMessage+1)
	if _, err := c.Fetch(1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Fetch = %v, want ErrTooLarge", err)
	}
	for _, cmd := range f.cmds {
		if strings.Contains(cmd, "BODY.PEEK") {
			t.Fatal("fetched the body of an oversized message")
		}
	}
}

func TestHugeSearch(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20000; i++ {
		fmt.Fprintf(&b, " %d", i)
	}
	f := &fakeServer{raw: func(conn net.Conn, tag, cmd string) bool {
		if cmd != "UID SEARCH UNSEEN" {
			return false
		}
		fmt.Fprintf(conn, "* SEARCH%s\r\n%s OK done\r\n", b.String(), tag)
		return true
	}}
	c := session(t, f)
	uids, err := c.Unseen()
	if err != nil || len(uids) != MaxUnseen {
		t.Fatalf("Unseen = %d uids, %v", len(uids), err)
	}
}

func TestTooManyUntagged(t *testing.T) {
	f := &fakeServer{raw: func(conn net.Conn, tag, cmd string) bool {
		if cmd != "UID SEARCH UNSEEN" {
			return false
		}
		for i := 0; i < maxUntagged+10; i++ {
			fmt.Fprint(conn, "* OK noise\r\n")
		}
		fmt.Fprintf(conn, "%s OK done\r\n", tag)
		return true
	}}
	c := session(t, f)
	if _, err := c.Unseen(); !errors.Is(err, ErrLimit) {
		t.Fatalf("Unseen = %v, want ErrLimit", err)
	}
}

func TestEmptyBody(t *testing.T) {
	for _, form := range []string{"NIL", `""`} {
		f := &fakeServer{raw: func(conn net.Conn, tag, cmd string) bool {
			if !strings.HasPrefix(cmd, "UID FETCH 1 BODY") {
				return false
			}
			fmt.Fprintf(conn, "* 1 FETCH (UID 1 BODY[] %s)\r\n%s OK done\r\n", form, tag)
			return true
		}}
		c := session(t, f)
		f.msgs[1] = ""
		if b, err := c.Fetch(1); err != nil || len(b) != 0 {
			t.Fatalf("%s: Fetch = %q, %v", form, b, err)
		}
	}
}

// A NO to one FETCH is a RefusedError; the session goes on.
func TestFetchRefused(t *testing.T) {
	f := &fakeServer{raw: func(conn net.Conn, tag, cmd string) bool {
		if !strings.HasPrefix(cmd, "UID FETCH 1 ") {
			return false
		}
		fmt.Fprintf(conn, "%s NO [UNAVAILABLE] try later\r\n", tag)
		return true
	}}
	c := session(t, f)
	f.msgs[1], f.msgs[2] = "a", "b"
	var re *RefusedError
	if _, err := c.Fetch(1); !errors.As(err, &re) {
		t.Fatalf("Fetch = %v, want a RefusedError", err)
	}
	if b, err := c.Fetch(2); err != nil || string(b) != "b" {
		t.Fatalf("next Fetch = %q, %v", b, err)
	}
}
