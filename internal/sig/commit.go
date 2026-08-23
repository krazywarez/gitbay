// Package sig verifies OpenPGP and SSHSIG signatures on git commits and
// tags, and maps them to the forge's trust states.
package sig

import (
	"bytes"
	"fmt"
	"strings"
)

// Commit is a parsed raw commit object.
type Commit struct {
	Raw            []byte
	Payload        []byte // Raw with the gpgsig header removed, byte-exact
	Signature      []byte // armored signature block, nil if unsigned
	AuthorName     string
	AuthorEmail    string
	CommitterEmail string
	Subject        string
	AuthorUnix     int64
}

// ParseCommit splits a raw commit object (as printed by `git cat-file
// commit`) into its signed payload and signature. The payload must be
// byte-exact: it is the original object minus the gpgsig header line and its
// continuation lines, nothing else.
func ParseCommit(raw []byte) (*Commit, error) {
	c := &Commit{Raw: raw}

	headerEnd := bytes.Index(raw, []byte("\n\n"))
	if headerEnd < 0 {
		return nil, fmt.Errorf("malformed commit: no header/body separator")
	}
	headers := raw[:headerEnd+1] // include trailing newline of last header
	body := raw[headerEnd+2:]

	var payload bytes.Buffer
	lines := bytes.SplitAfter(headers, []byte("\n"))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if sigBody, ok := bytes.CutPrefix(line, []byte("gpgsig ")); ok {
			// The signature value continues on lines starting with a space.
			var sig bytes.Buffer
			sig.Write(sigBody)
			for i+1 < len(lines) && bytes.HasPrefix(lines[i+1], []byte(" ")) {
				sig.Write(lines[i+1][1:])
				i++
			}
			c.Signature = bytes.TrimSuffix(sig.Bytes(), []byte("\n"))
			continue
		}
		payload.Write(line)

		switch {
		case bytes.HasPrefix(line, []byte("author ")):
			c.AuthorName, c.AuthorEmail, c.AuthorUnix = parseIdent(string(line[len("author "):]))
		case bytes.HasPrefix(line, []byte("committer ")):
			_, c.CommitterEmail, _ = parseIdent(string(line[len("committer "):]))
		}
	}
	payload.WriteByte('\n')
	payload.Write(body)
	c.Payload = payload.Bytes()

	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		c.Subject = string(body[:i])
	} else {
		c.Subject = strings.TrimRight(string(body), "\n")
	}
	return c, nil
}

// parseIdent parses "Name <email> unix tz".
func parseIdent(s string) (name, email string, unix int64) {
	s = strings.TrimSuffix(s, "\n")
	lt := strings.IndexByte(s, '<')
	gt := strings.IndexByte(s, '>')
	if lt < 0 || gt < lt {
		return s, "", 0
	}
	name = strings.TrimSpace(s[:lt])
	email = s[lt+1 : gt]
	rest := strings.Fields(s[gt+1:])
	if len(rest) >= 1 {
		fmt.Sscanf(rest[0], "%d", &unix)
	}
	return name, email, unix
}

// SigKind reports which signature format a gpgsig block holds.
type SigKind int

const (
	SigNone SigKind = iota
	SigOpenPGP
	SigSSH
	SigUnknown
)

func KindOf(sig []byte) SigKind {
	switch {
	case sig == nil:
		return SigNone
	case bytes.Contains(sig, []byte("BEGIN PGP SIGNATURE")):
		return SigOpenPGP
	case bytes.Contains(sig, []byte("BEGIN SSH SIGNATURE")):
		return SigSSH
	default:
		return SigUnknown
	}
}
