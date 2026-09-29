package mailin

import (
	"bytes"
	"net/mail"
	"net/textproto"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/mailreply"
)

// FuzzReply runs what an inbound message goes through before any
// account is looked up: header parsing, token extraction and
// verification, body extraction and quote stripping.
func FuzzReply(f *testing.F) {
	f.Add([]byte("From: a@b\r\nTo: reply+abc@x.example\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nhi=\r\n\r\n> q\r\n--b--\r\n"))
	f.Add([]byte("Subject: x\r\n\r\nOn Mon wrote:\r\n> a\r\n-- \r\nsig\r\n"))
	key := [][]byte{[]byte("0123456789abcdef0123456789abcdef")}
	f.Fuzz(func(t *testing.T, raw []byte) {
		msg, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			return
		}
		automatic(msg.Header)
		if tok := findToken(msg.Header, "reply@x.example"); tok != "" {
			mailreply.Verify(key, tok, fuzzNow)
		}
		if s, err := textBody(textproto.MIMEHeader(msg.Header), msg.Body, 1<<16); err == nil {
			stripQuoted(s)
		}
	})
}

var fuzzNow = time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
