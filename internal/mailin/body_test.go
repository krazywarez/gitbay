package mailin

import (
	"bytes"
	"errors"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
)

func TestStripQuoted(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"gmail",
			"Agreed, ship it.\n\nOn Mon, Sep 28, 2026 at 9:00 AM gitbay <reply+abc@gitbay.example> wrote:\n\n> alice commented on #1\n>\n> looks fine\n",
			"Agreed, ship it."},
		{"gmail, attribution wrapped",
			"Agreed.\n\nOn Mon, Sep 28, 2026 at 9:00 AM gitbay <\nreply+abc@gitbay.example> wrote:\n\n> alice commented\n",
			"Agreed."},
		{"apple mail",
			"Fixed in the next push.\n\nSent from my iPhone\n\n> On Sep 28, 2026, at 09:00, gitbay <reply+abc@gitbay.example> wrote:\n> \n> alice commented on #1\n",
			"Fixed in the next push."},
		{"apple mail, unquoted attribution",
			"Yes.\n\nOn 28 Sep 2026, at 09:00, gitbay <reply+abc@gitbay.example> wrote:\n\n> alice commented\n",
			"Yes."},
		{"outlook",
			"Will do.\r\n\r\n________________________________\r\nFrom: gitbay <reply+abc@gitbay.example>\r\nSent: Monday, September 28, 2026 9:00 AM\r\nTo: Bob\r\nSubject: [alice/app] #1: title\r\n\r\nalice commented on #1\r\n",
			"Will do."},
		{"outlook, no rule",
			"Will do.\n\nFrom: gitbay <reply+abc@gitbay.example>\nSent: Monday, September 28, 2026 9:00 AM\nTo: Bob\n\nalice commented on #1\n",
			"Will do."},
		{"outlook, original message",
			"Noted.\n\n-----Original Message-----\nFrom: gitbay\nalice commented\n",
			"Noted."},
		{"thunderbird",
			"Thanks, merged.\n\n-- \nBob Example\nExample Corp\n\nOn 9/28/26 09:00, gitbay wrote:\n> alice commented on #1\n",
			"Thanks, merged."},
		{"thunderbird, quote first",
			"On 9/28/26 09:00, gitbay wrote:\n> alice commented on #1\n\nThat was me.\n",
			""},
		{"inline answers keep the answers",
			"> does this build?\nYes, on main.\n> and the tests?\nAll green.\n",
			"Yes, on main.\nAll green."},
		{"a From: line in prose is kept",
			"From: the log it looks like a timeout.\nRetrying.\n",
			"From: the log it looks like a timeout.\nRetrying."},
		{"markdown rule is not a signature",
			"one\n\n---\n\ntwo\n",
			"one\n\n---\n\ntwo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripQuoted(tc.in); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func body(t *testing.T, raw string) (string, error) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return textBody(textproto.MIMEHeader(msg.Header), msg.Body, 1<<16)
}

func TestTextBody(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"plain, no content type", "Subject: x\r\n\r\nhello\r\n", "hello\r\n"},
		{"quoted-printable", "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=C3=A9 =\r\nau lait\r\n", "café au lait\r\n"},
		{"base64", "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGVs\r\nbG8=\r\n", "hello"},
		{"latin-1", "Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9\r\n", "café\r\n"},
		{"alternative prefers plain",
			"Content-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--b\r\nContent-Type: text/plain\r\n\r\nplain\r\n--b--\r\n",
			"plain"},
		{"mixed with nested alternative and an attachment",
			"Content-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nattached\r\n--m\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nbody\r\n--a--\r\n--m--\r\n",
			"body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := body(t, tc.raw)
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for name, raw := range map[string]string{
		"html only":        "Content-Type: text/html\r\n\r\n<p>hi</p>\r\n",
		"unknown charset":  "Content-Type: text/plain; charset=x-nonesuch\r\n\r\nhi\r\n",
		"unknown encoding": "Content-Type: text/plain\r\nContent-Transfer-Encoding: x-uuencode\r\n\r\nhi\r\n",
	} {
		if _, err := body(t, raw); !errors.Is(err, errNoText) {
			t.Errorf("%s: %v, want errNoText", name, err)
		}
	}
	if _, err := body(t, "Subject: x\r\n\r\n"+string(bytes.Repeat([]byte("a"), 8<<16+1))); !errors.Is(err, errTooLong) {
		t.Errorf("oversized body: %v", err)
	}
}
