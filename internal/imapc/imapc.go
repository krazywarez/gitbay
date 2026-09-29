// Package imapc is the IMAP4rev1 client the reply-by-mail poller needs
// (#295): LOGIN, SELECT or EXAMINE, UID SEARCH UNSEEN, UID FETCH
// BODY.PEEK[], UID STORE +FLAGS (\Seen), LOGOUT. Nothing else. The
// connection is TLS from the first byte or upgraded with STARTTLS
// before LOGIN; there is no plaintext mode.
package imapc

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// MaxMessage is the largest message Fetch returns; a larger one is
// refused by its RFC822.SIZE before its body is fetched (ErrTooLarge).
const MaxMessage = 10 << 20

// Limits on what one command may make the client read. A server that
// exceeds one has its connection closed and the command returns
// ErrLimit.
const (
	cmdBudget   = MaxMessage + 1<<20 // bytes read for one command
	maxUntagged = 1000               // untagged responses to one command
	maxLine     = 1 << 20            // one response line outside literals
)

// MaxUnseen is the most UIDs Unseen returns; the rest wait for the next
// poll.
const MaxUnseen = 10000

var (
	ErrTooLarge = errors.New("message larger than the fetch limit")
	ErrLimit    = errors.New("IMAP server exceeded a response limit; connection closed")
)

// rootCAs verifies the server's certificate; nil is the system pool.
// Tests set it.
var rootCAs *x509.CertPool

// Client is one authenticated IMAP session.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
	left int64 // bytes the current command may still read
}

// Dial connects to addr (host:port) and reads the greeting. With
// starttls it upgrades the connection before returning; otherwise TLS
// runs from the first byte.
func Dial(addr string, starttls bool, timeout time.Duration) (*Client, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{ServerName: host, RootCAs: rootCAs, MinVersion: tls.VersionTLS12}
	d := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	if starttls {
		conn, err = d.Dial("tcp", addr)
	} else {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tlsCfg)
	}
	if err != nil {
		return nil, err
	}
	c := New(conn)
	conn.SetDeadline(time.Now().Add(timeout))
	if err := c.greeting(); err != nil {
		conn.Close()
		return nil, err
	}
	if starttls {
		if _, err := c.cmd("STARTTLS"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
		tc := tls.Client(conn, tlsCfg)
		if err := tc.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
		c.conn, c.r = tc, bufio.NewReader(tc)
	}
	return c, nil
}

// New wraps a connection that is already past its TLS handshake, or a
// test's pipe. The greeting has not been read.
func New(conn net.Conn) *Client {
	return &Client{conn: conn, r: bufio.NewReader(conn)}
}

// SetDeadline bounds the session's remaining I/O.
func (c *Client) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

func (c *Client) greeting() error {
	c.left = cmdBudget
	resp, err := c.readResponse()
	line := resp.line
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "* OK") && !strings.HasPrefix(line, "* PREAUTH") {
		return fmt.Errorf("unexpected greeting %q", clip(line))
	}
	return nil
}

// Login authenticates. The password goes as a quoted string, so it may
// not hold a line break or a byte outside printable ASCII.
func (c *Client) Login(user, pass string) error {
	u, err := quote(user)
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	p, err := quote(pass)
	if err != nil {
		return fmt.Errorf("password: %w", err)
	}
	if _, err := c.cmd("LOGIN " + u + " " + p); err != nil {
		// The server's text is not echoed: some quote the command.
		return errors.New("LOGIN refused")
	}
	return nil
}

// Select opens mailbox for reading and writing flags; Examine opens it
// read-only. Both return the number of messages in it.
func (c *Client) Select(mailbox string) (int, error)  { return c.open("SELECT", mailbox) }
func (c *Client) Examine(mailbox string) (int, error) { return c.open("EXAMINE", mailbox) }

func (c *Client) open(verb, mailbox string) (int, error) {
	m, err := quote(mailbox)
	if err != nil {
		return 0, err
	}
	untagged, err := c.cmd(verb + " " + m)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", verb, mailbox, err)
	}
	exists := 0
	for _, u := range untagged {
		f := strings.Fields(u.line)
		if len(f) >= 3 && strings.EqualFold(f[2], "EXISTS") {
			exists, _ = strconv.Atoi(f[1])
		}
	}
	return exists, nil
}

// Unseen returns the UIDs of messages without \Seen.
func (c *Client) Unseen() ([]uint32, error) {
	untagged, err := c.cmd("UID SEARCH UNSEEN")
	if err != nil {
		return nil, fmt.Errorf("UID SEARCH: %w", err)
	}
	var uids []uint32
	for _, u := range untagged {
		f := strings.Fields(u.line)
		if len(f) < 2 || !strings.EqualFold(f[1], "SEARCH") {
			continue
		}
		for _, s := range f[2:] {
			n, err := strconv.ParseUint(s, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("UID SEARCH: bad uid %q", clip(s))
			}
			if len(uids) < MaxUnseen {
				uids = append(uids, uint32(n))
			}
		}
	}
	return uids, nil
}

// Fetch returns the whole message without setting \Seen. A message
// over MaxMessage is refused by its size first. A server answering
// BODY[] with NIL or "" returns an empty message.
func (c *Client) Fetch(uid uint32) ([]byte, error) {
	untagged, err := c.cmd(fmt.Sprintf("UID FETCH %d RFC822.SIZE", uid))
	if err != nil {
		return nil, fmt.Errorf("UID FETCH: %w", err)
	}
	for _, u := range untagged {
		up := strings.ToUpper(u.line)
		if i := strings.Index(up, "RFC822.SIZE "); i >= 0 && strings.Contains(up, " FETCH ") {
			f := strings.Fields(strings.TrimRight(up[i+len("RFC822.SIZE "):], ")"))
			if len(f) > 0 {
				if n, err := strconv.ParseInt(strings.TrimRight(f[0], ")"), 10, 64); err == nil && n > MaxMessage {
					return nil, ErrTooLarge
				}
			}
		}
	}
	untagged, err = c.cmd(fmt.Sprintf("UID FETCH %d BODY.PEEK[]", uid))
	if err != nil {
		return nil, fmt.Errorf("UID FETCH: %w", err)
	}
	for _, u := range untagged {
		f := strings.Fields(u.line)
		if len(f) < 3 || !strings.EqualFold(f[2], "FETCH") {
			continue
		}
		if u.tooLarge {
			return nil, ErrTooLarge
		}
		if u.body != nil {
			return u.body, nil
		}
		up := strings.ToUpper(u.line)
		if strings.Contains(up, "BODY[] NIL") || strings.Contains(up, `BODY[] ""`) {
			return []byte{}, nil
		}
	}
	return nil, fmt.Errorf("UID FETCH %d: no message body in the response", uid)
}

// MarkSeen sets \Seen.
func (c *Client) MarkSeen(uid uint32) error {
	if _, err := c.cmd(fmt.Sprintf("UID STORE %d +FLAGS.SILENT (\\Seen)", uid)); err != nil {
		return fmt.Errorf("UID STORE: %w", err)
	}
	return nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	c.cmd("LOGOUT")
	return c.conn.Close()
}

type response struct {
	line     string // the response with each literal's bytes left out
	body     []byte // the BODY[] literal, when the response carried one
	tooLarge bool   // the BODY[] literal was over MaxMessage and not kept
}

// cmd sends one tagged command and collects the untagged responses up to
// its completion. A NO or BAD completion is an error.
func (c *Client) cmd(command string) ([]response, error) {
	c.tag++
	c.left = cmdBudget
	tag := "g" + strconv.Itoa(c.tag)
	if _, err := io.WriteString(c.conn, tag+" "+command+"\r\n"); err != nil {
		return nil, err
	}
	var untagged []response
	for {
		resp, err := c.readResponse()
		if err != nil {
			return nil, err
		}
		line := resp.line
		if rest, ok := strings.CutPrefix(line, tag+" "); ok {
			status, _, _ := strings.Cut(rest, " ")
			if strings.EqualFold(status, "OK") {
				return untagged, nil
			}
			return nil, fmt.Errorf("%s", clip(rest))
		}
		if strings.HasPrefix(line, "* BYE") && command != "LOGOUT" {
			return nil, fmt.Errorf("server closed the session: %s", clip(line))
		}
		if strings.HasPrefix(line, "*") {
			if len(untagged) >= maxUntagged {
				return nil, c.limit()
			}
			untagged = append(untagged, resp)
		}
		// A "+" continuation is not expected: no command here sends a
		// literal.
	}
}

// limit closes a connection whose server exceeded a limit.
func (c *Client) limit() error {
	c.conn.Close()
	return ErrLimit
}

// readResponse reads one response: a line, and for each literal it
// announces ("{n}" at the end of a line) the n bytes and the rest of the
// response after them. Only the literal after "BODY[]" is kept; any
// other is read and discarded. Everything read counts against the
// command's budget.
func (c *Client) readResponse() (response, error) {
	var b strings.Builder
	var resp response
	for {
		line, err := c.readLine()
		if err != nil {
			return response{}, err
		}
		b.WriteString(line)
		n, ok := literalSize(line)
		if !ok {
			resp.line = b.String()
			return resp, nil
		}
		if n > c.left {
			return response{}, c.limit()
		}
		c.left -= n
		prefix := strings.TrimRight(line[:strings.LastIndexByte(line, '{')], " ")
		keep := resp.body == nil && !resp.tooLarge && strings.HasSuffix(strings.ToUpper(prefix), "BODY[]")
		if !keep || n > MaxMessage {
			if _, err := io.CopyN(io.Discard, c.r, n); err != nil {
				return response{}, err
			}
			if keep {
				resp.tooLarge = true
			}
			continue
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return response{}, err
		}
		resp.body = buf
	}
}

func (c *Client) readLine() (string, error) {
	var b []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return "", err
		}
		b = append(b, chunk...)
		c.left -= int64(len(chunk)) + 2
		if len(b) > maxLine || c.left < 0 {
			return "", c.limit()
		}
		if !isPrefix {
			return string(b), nil
		}
	}
}

// literalSize reads a trailing "{n}" (or "{n+}").
func literalSize(line string) (int64, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, false
	}
	i := strings.LastIndexByte(line, '{')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(line[i+1:len(line)-1], "+"), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// quote renders s as an IMAP quoted string.
func quote(s string) (string, error) {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return "", errors.New("only printable ASCII can be sent")
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
