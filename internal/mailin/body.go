package mailin

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

var errNoText = errors.New("no text/plain part")

// maxParts and maxDepth bound the walk through a multipart message.
const (
	maxParts = 64
	maxDepth = 5
)

// textBody returns the message's first text/plain part that is not an
// attachment, decoded to UTF-8. A message with only HTML has none, and
// is refused rather than converted.
func textBody(h textproto.MIMEHeader, body io.Reader, limit int64) (string, error) {
	parts := 0
	return walk(h, body, limit, 0, &parts)
}

func walk(h textproto.MIMEHeader, body io.Reader, limit int64, depth int, parts *int) (string, error) {
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return "", errNoText
	}
	switch {
	case mt == "text/plain":
		if d, _, _ := mime.ParseMediaType(h.Get("Content-Disposition")); d == "attachment" {
			return "", errNoText
		}
		return decodeText(h.Get("Content-Transfer-Encoding"), params["charset"], body, limit)
	case strings.HasPrefix(mt, "multipart/") && depth < maxDepth:
		if params["boundary"] == "" {
			return "", errNoText
		}
		mr := multipart.NewReader(body, params["boundary"])
		for {
			// NextRawPart leaves quoted-printable to decodeText, so every
			// part is decoded the same way.
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return "", errNoText
			}
			if err != nil {
				return "", err
			}
			if *parts++; *parts > maxParts {
				return "", errNoText
			}
			s, err := walk(p.Header, p, limit, depth+1, parts)
			if err == nil {
				return s, nil
			}
			if !errors.Is(err, errNoText) {
				return "", err
			}
		}
	}
	return "", errNoText
}

var errTooLong = errors.New("reply is longer than a comment may be")

func decodeText(cte, charset string, body io.Reader, limit int64) (string, error) {
	var r io.Reader = body
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, &skipSpace{r: bufio.NewReader(r)})
	case "", "7bit", "8bit", "binary":
	default:
		return "", errNoText
	}
	switch cs := strings.ToLower(strings.TrimSpace(charset)); cs {
	case "", "utf-8", "utf8", "us-ascii":
	default:
		enc, err := htmlindex.Get(cs)
		if err != nil {
			return "", errNoText
		}
		r = enc.NewDecoder().Reader(r)
	}
	// Quoted history is stripped after reading, so the read allows for
	// a reply several times the size of a comment before refusing it.
	raw, err := io.ReadAll(io.LimitReader(r, 8*limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > 8*limit {
		return "", errTooLong
	}
	return strings.ToValidUTF8(string(raw), string(utf8.RuneError)), nil
}

// skipSpace drops the line breaks and spaces base64 bodies are wrapped
// with.
type skipSpace struct{ r *bufio.Reader }

func (s *skipSpace) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		b, err := s.r.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		if b == '\r' || b == '\n' || b == ' ' || b == '\t' {
			continue
		}
		p[n] = b
		n++
	}
	return n, nil
}

var (
	// "On Mon, Sep 28, 2026 at 9:00 AM gitbay <reply+…@…> wrote:", which
	// Gmail, Apple Mail and Thunderbird all put above the quote, and
	// which Gmail wraps onto a second line when it is long.
	attribution = regexp.MustCompile(`(?i)^on\s.*\bwrote:\s*$`)
	// Outlook: "-----Original Message-----", or a rule of underscores
	// above a From:/Sent: header block.
	originalMessage = regexp.MustCompile(`(?i)^\s*-{2,}\s*original message\s*-{2,}\s*$`)
	underscores     = regexp.MustCompile(`^\s*_{10,}\s*$`)
	headerFrom      = regexp.MustCompile(`(?i)^\s*\*?from:\*?\s`)
	headerSentDate  = regexp.MustCompile(`(?i)^\s*\*?(sent|date):\*?\s`)
	mobileSig       = regexp.MustCompile(`(?i)^sent from my \S`)
)

// stripQuoted returns the text a person wrote in a reply: quoted lines
// ("> …") dropped wherever they are, and everything from the first
// separator a mail client puts above the quoted message, or from the
// signature delimiter "-- ", cut off.
func stripQuoted(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	cut := len(lines)
	for i, l := range lines {
		t := strings.TrimRight(l, " \t")
		next := ""
		if i+1 < len(lines) {
			next = strings.TrimSpace(lines[i+1])
		}
		switch {
		case l == "-- " || t == "--":
		case originalMessage.MatchString(t):
		case underscores.MatchString(t):
		case attribution.MatchString(strings.TrimSpace(t)):
		case strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "on ") &&
			attribution.MatchString(strings.TrimSpace(t)+" "+next):
		case headerFrom.MatchString(t) && followedByHeader(lines[i+1:]):
		default:
			continue
		}
		cut = i
		break
	}
	var out []string
	for _, l := range lines[:cut] {
		if strings.HasPrefix(strings.TrimLeft(l, " "), ">") {
			continue
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	// A phone's canned signature, when it is the last thing written.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) > 0 && mobileSig.MatchString(strings.TrimSpace(out[len(out)-1])) {
		out = out[:len(out)-1]
	}
	return strings.TrimSpace(collapseBlank(strings.Join(out, "\n")))
}

// followedByHeader reports whether a Sent: or Date: line comes within
// the next few lines, as in the header block Outlook quotes.
func followedByHeader(rest []string) bool {
	for i := 0; i < len(rest) && i < 4; i++ {
		if headerSentDate.MatchString(rest[i]) {
			return true
		}
	}
	return false
}

// collapseBlank turns runs of blank lines, left where quoted lines were
// dropped, into one.
func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
