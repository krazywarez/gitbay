package mailin

import (
	"bytes"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

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

// FuzzDKIM runs the DKIM check, the dkim package's signature and key
// record parsing included, on arbitrary messages: no input panics.
func FuzzDKIM(f *testing.F) {
	msg := "From: bob@example.test\r\nTo: x@y\r\nSubject: s\r\n\r\nhi\r\n"
	for _, canon := range []dkim.Canonicalization{dkim.CanonicalizationSimple, dkim.CanonicalizationRelaxed} {
		var b bytes.Buffer
		o := dkim.SignOptions{Domain: "example.test", Selector: "rsa", Signer: rsaKey,
			HeaderCanonicalization: canon, BodyCanonicalization: canon}
		if err := dkim.Sign(&b, strings.NewReader(msg), &o); err != nil {
			f.Fatal(err)
		}
		f.Add(b.Bytes())
	}
	f.Add([]byte("DKIM-Signature: v=1; a=ed25519-sha256; d=example.test; s=temp; h=from; bh=; b=\r\nFrom: a@example.test\r\n\r\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		msg, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			return
		}
		from, err := msg.Header.AddressList("From")
		if err != nil || len(from) != 1 {
			return
		}
		p := &Processor{LookupTXT: (&fakeDNS{}).lookup}
		p.dkimVerified(raw, msg.Header, from[0].Address)
	})
}
