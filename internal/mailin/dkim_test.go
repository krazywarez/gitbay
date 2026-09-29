package mailin

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"gitbay.org/gitbay/internal/mailreply"
)

// Fixed test keys: RSA 2048 and Ed25519.
const rsaPEM = `-----BEGIN PRIVATE KEY-----
MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQC6i925olmivu5U
iNMbgSLK4SEZNq4TIPQYPwJ3fHHFa+6V3jtD/2HllkZCrRlBAvra7H8q7TfoCj2K
lN7hLGkawZM0x9qhxn+AUKuTTL3XeRdl3HQejfuuQvhAQkBU53lF2v0mqzVAEz/k
cxcNT068yWeCT4GWyT4/A1yy1lfTzyZXrKNcdEDQ5ah8M47kaEYeeRAza19roV3F
StAggPG/ORC64JXQkwbIEOgNNUYp7hUUWen+kKd5qsuoxaYaWGJ/cEHeEFzsaZN2
fA//qFAt3jwfbQvnpPzp1c6txkiDnYJgHXp5gRcdDuZ8q85bmeqZf1OBCR2d7Ery
q1L1sFdRAgMBAAECggEADoc2DW8HbBVSmmLNjibQftxpp30KsZKvb/P4TTXz5lwx
iJp2IyWQikDZ1/eDL/z7bHFetgkjgX7KrDBL6116EgthW4r1DARZibS+qAoh/tX/
bH9uy7JjF38/tkFyoSol17rmXEyZKRRWtYQBF5hFmY5V8WAfx46EuoOYhJUM4gHt
3hN6xqvGEjLTgb1xLf08+ntgTzPW3QSiE3A0b0nyRbu8t8PjP0DoABdORuI16hsi
8V1wUoz1iTKGtV1a98moSxyWWC0udBp+tdhZJ+yun5zLqd/cALvXsl8Y7cIIuyLa
B/mLTkM1SX9swZ06tSa4vRd9fPQz0J5zbgo8Mf3FwQKBgQDH7SuSZ3YpTMhoAmz/
V6UVN4NNkYkHqNEB3bv0VRG4bPtLXJH9vyhchtwHQMsKTwbqgsFiUON2VwZ0/pfc
0hDooWQHaoVJWRjfZQLD5K42QKQlcDowyvMYT046UViJOSQeTj4IbT0/2EehkHXA
qCoITU2I6zIfF1Te8yI1wFmdTwKBgQDu3f9rIZq+ihaLrrGMkagofWmVKUzFqVwK
ckcKXZJ0uQ/ns1er+SITVU3WdOrLsFE+zpE5CH8LG9FIoFhNcCwvzCXV5iACqIi8
4PgkxNDYTo9kMW3yWuWiFcZpLvA7BOCnvW4gmmIofLe1OMBE9F5VquECibeoamPb
uQgzELnZXwKBgB24BbgXpRryjP/ZDHbQgnuq6tvG/IWk9JzAZ0YktyOhH6HOOu1r
UwaeDWsOmKAJq0+E7FY/C/D1csJFbjGnEFhkVUg871893VKn40dXYQYzibL/Acdr
A8PjVg+ZM/4B/np6ywHZqzcoYU2E+dwPo1/kjdgCjkrM3xLdNYKj+y5FAoGAOJaz
OggeBuHj8XeTbH/dXKpJZyL/oxw6R+dG2TfNyIVHNVcRgBZncjkVVachMNw2gzCg
yuguYM1YSWJjSQU4EqLEm+YG01pl+ok5gEx4RaZm5g+nwnCyUjHibWzHUNQY/OQt
wN+SPZE+XFpzgmJ6LsVqxRUnQ2jg+17ciGx/+vUCgYArCRxfa4ecYlZQYTvtbpwY
pLt6iUht7y69jkwSUTkOn+ZjBDcVWrJwfjt8R9BkMB/2rcwUj4Yo9DcgiWkfHSpM
W5QuMVb8coft4mC7G37mEoWWdNrc0TLkbfTSr+liNXoWp0QGL7/RQO7HHK+9QVD0
FfatkTVO1YuBsyGp9eE5rQ==
-----END PRIVATE KEY-----`

const edPEM = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEICVufJC+iEzea5y5QlUD39QNmX2n/0c93QCcQrfQH8W8
-----END PRIVATE KEY-----`

var rsaKey, edKey = parseKey(rsaPEM), parseKey(edPEM)

func parseKey(s string) crypto.Signer {
	b, _ := pem.Decode([]byte(s))
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		panic(err)
	}
	return k.(crypto.Signer)
}

func keyRecord(pub crypto.PublicKey) string {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(k)
	default:
		b, err := x509.MarshalPKIXPublicKey(k)
		if err != nil {
			panic(err)
		}
		return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(b)
	}
}

// fakeDNS answers by selector whatever the domain: rsa, ed and key1 are
// the fixed keys, short is a 512-bit RSA key, temp fails temporarily,
// and anything else does not exist.
type fakeDNS struct{ lookups int }

func (d *fakeDNS) lookup(_ context.Context, name string) ([]string, error) {
	d.lookups++
	sel, _, _ := strings.Cut(name, ".")
	switch sel {
	case "rsa", "key1":
		return []string{keyRecord(rsaKey.Public())}, nil
	case "ed":
		return []string{keyRecord(edKey.Public())}, nil
	case "short":
		n := new(big.Int).Lsh(big.NewInt(1), 511)
		n.Add(n, big.NewInt(0x2f))
		return []string{keyRecord(&rsa.PublicKey{N: n, E: 65537})}, nil
	case "temp":
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	case "timeout":
		return nil, context.DeadlineExceeded
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

var exampleKeys = []string{"from", "to", "subject", "date", "message-id", "mime-version", "content-type"}

func signMsg(t *testing.T, msg, domain, selector string, key crypto.Signer, canon dkim.Canonicalization, mod func(*dkim.SignOptions)) string {
	t.Helper()
	o := dkim.SignOptions{Domain: domain, Selector: selector, Signer: key,
		HeaderCanonicalization: canon, BodyCanonicalization: canon, HeaderKeys: exampleKeys}
	if mod != nil {
		mod(&o)
	}
	var b bytes.Buffer
	if err := dkim.Sign(&b, strings.NewReader(msg), &o); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func dkimSetup(t *testing.T) (*fixture, *fakeDNS) {
	t.Helper()
	f := setup(t)
	dns := &fakeDNS{}
	f.p.LookupTXT = dns.lookup
	f.p.Cfg.Mail.Inbound.RequireDKIM = true
	for _, a := range []string{"bob@cleberg.net", "bob@mail.example.test", "bob@user.github.io"} {
		if err := f.st.AddEmail(f.bob, a, "admin", true); err != nil {
			t.Fatal(err)
		}
	}
	return f, dns
}

func TestDKIM(t *testing.T) {
	type tc struct {
		name, from, domain, selector string
		key                          crypto.Signer
		canon                        dkim.Canonicalization
		mod                          func(*dkim.SignOptions)
		after                        func(string) string // applied to the signed message
		reason                       string              // "" posts
		retry                        bool
	}
	var relaxed, simple dkim.Canonicalization = dkim.CanonicalizationRelaxed, dkim.CanonicalizationSimple
	for _, c := range []tc{
		{name: "rsa relaxed", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed},
		{name: "rsa simple", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: simple},
		{name: "ed25519 relaxed", from: "bob@example.test", domain: "example.test", selector: "ed", key: edKey, canon: relaxed},
		{name: "ed25519 simple", from: "bob@example.test", domain: "example.test", selector: "ed", key: edKey, canon: simple},
		{name: "the shape Migadu sends", from: "bob@cleberg.net", domain: "cleberg.net", selector: "key1", key: rsaKey, canon: simple,
			after: func(m string) string {
				if !strings.Contains(m, "d=cleberg.net;") || !strings.Contains(m, "s=key1;") || !strings.Contains(m, "c=simple/simple;") ||
					!strings.Contains(m, "h=from:to:subject:date:message-id:mime-version:content-type;") || !strings.Contains(m, "a=rsa-sha256;") {
					panic("signature not in the expected shape:\n" + m)
				}
				return m
			}},
		{name: "signing domain a subdomain of From's", from: "bob@example.test", domain: "mail.example.test", selector: "rsa", key: rsaKey, canon: relaxed},
		{name: "From a subdomain of the signing domain", from: "bob@mail.example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed},
		{name: "body altered", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			after: func(m string) string { return m + "appended\r\n" }, reason: "body hash did not verify"},
		{name: "header altered", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			after: func(m string) string { return strings.Replace(m, "Subject: Re:", "Subject: Fwd:", 1) }, reason: "signature did not verify"},
		{name: "From not in h=", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			after: func(m string) string { return strings.Replace(m, "h=from:to:", "h=to:", 1) }, reason: "From field not signed"},
		{name: "misaligned d=", from: "bob@example.test", domain: "attacker.example", selector: "rsa", key: rsaKey, canon: relaxed,
			reason: "d=attacker.example: d= not aligned"},
		{name: "public suffix d=", from: "bob@user.github.io", domain: "github.io", selector: "rsa", key: rsaKey, canon: relaxed,
			reason: "d=github.io: d= not aligned"},
		{name: "expired x=", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			mod: func(o *dkim.SignOptions) { o.Expiration = time.Now().Add(-time.Minute) }, reason: "expired"},
		{name: "l= refused", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			after:  func(m string) string { return strings.Replace(m, "DKIM-Signature: ", "DKIM-Signature: l=4; ", 1) },
			reason: "body length"},
		{name: "rsa-sha1 refused", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			after: func(m string) string { return strings.Replace(m, "a=rsa-sha256", "a=rsa-sha1", 1) }, reason: "too weak"},
		{name: "512-bit key refused", from: "bob@example.test", domain: "example.test", selector: "short", key: rsaKey, canon: relaxed,
			reason: "too short"},
		{name: "no key", from: "bob@example.test", domain: "example.test", selector: "gone", key: rsaKey, canon: relaxed,
			reason: "no key for signature"},
		{name: "second From field", from: "bob@example.test", domain: "example.test", selector: "rsa", key: rsaKey, canon: relaxed,
			after: func(m string) string { return "From: bob@example.test\r\n" + m }, reason: "more than one From field"},
		{name: "DNS temporary failure", from: "bob@example.test", domain: "example.test", selector: "temp", key: rsaKey, canon: relaxed,
			reason: "key lookup failed", retry: true},
		{name: "DNS timeout", from: "bob@example.test", domain: "example.test", selector: "timeout", key: rsaKey, canon: relaxed,
			reason: "key lookup failed", retry: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := dkimSetup(t)
			m := signMsg(t, f.messageAs(t, f.bob, c.from, "<d@x>", "", "hi"), c.domain, c.selector, c.key, c.canon, c.mod)
			if c.after != nil {
				m = c.after(m)
			}
			res := f.p.Handle([]byte(m))
			if c.reason == "" {
				if !res.Posted {
					t.Fatalf("not posted: %+v", res)
				}
				return
			}
			if res.Posted || res.Retry != c.retry || !strings.Contains(res.Reason, c.reason) {
				t.Fatalf("result %+v, want reason %q retry %v", res, c.reason, c.retry)
			}
			audit := f.refusalReasons(t)
			if c.retry {
				if audit != "" {
					t.Fatalf("a retry was audited:\n%s", audit)
				}
				return
			}
			if !strings.Contains(audit, c.reason) {
				t.Fatalf("refusal not audited:\n%s", audit)
			}
			if strings.Contains(audit, "appended") || strings.Contains(audit, `"hi`) {
				t.Fatalf("audit carries content:\n%s", audit)
			}
		})
	}
}

func TestDKIMNoSignature(t *testing.T) {
	f, _ := dkimSetup(t)
	res := f.p.Handle([]byte(f.message(t, "bob@example.test", "hi")))
	if res.Posted || res.Retry || !strings.Contains(res.Reason, "no DKIM-Signature") {
		t.Fatalf("result %+v", res)
	}
}

func TestDKIMFutureTime(t *testing.T) {
	f, _ := dkimSetup(t)
	m := signMsg(t, f.message(t, "bob@example.test", "hi"), "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
	f.p.Now = func() time.Time { return time.Now().Add(-time.Hour) }
	if res := f.p.Handle([]byte(m)); res.Posted || !strings.Contains(res.Reason, "dated in the future") {
		t.Fatalf("result %+v", res)
	}
}

// Only the first maxSignatures signatures are checked.
func TestDKIMTooManySignatures(t *testing.T) {
	for _, bad := range []int{maxSignatures - 1, maxSignatures} {
		f, _ := dkimSetup(t)
		msg := f.messageAs(t, f.bob, "bob@example.test", "<many@x>", "", "hi")
		m := signMsg(t, msg, "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
		for i := 0; i < bad; i++ {
			s := signMsg(t, msg, "attacker.example", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
			m = strings.TrimSuffix(s, msg) + m
		}
		res := f.p.Handle([]byte(m))
		if want := bad < maxSignatures; res.Posted != want {
			t.Fatalf("%d misaligned signatures first: posted %v, want %v (%+v)", bad, res.Posted, want, res)
		}
	}
}

// With both set, either passing is enough.
func TestDKIMOrAuthenticationResults(t *testing.T) {
	f, _ := dkimSetup(t)
	f.p.Cfg.Mail.Inbound.TrustedAuthservID = "mx.example.net"
	signed := signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<one@x>", "", "hi"), "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
	if res := f.p.Handle([]byte(signed)); !res.Posted {
		t.Fatalf("DKIM pass, no Authentication-Results: %+v", res)
	}
	ar := "Authentication-Results: mx.example.net; dmarc=pass header.from=example.test\r\n"
	if res := f.p.Handle([]byte(f.messageAs(t, f.bob, "bob@example.test", "<two@x>", ar, "hi"))); !res.Posted {
		t.Fatalf("Authentication-Results pass, no signature: %+v", res)
	}
	res := f.p.Handle([]byte(f.messageAs(t, f.bob, "bob@example.test", "<three@x>", "", "hi")))
	if res.Posted || !strings.Contains(res.Reason, "no Authentication-Results") || !strings.Contains(res.Reason, "no DKIM-Signature") {
		t.Fatalf("neither: %+v", res)
	}
}

func TestDKIMKeyCache(t *testing.T) {
	f, dns := dkimSetup(t)
	for _, id := range []string{"<c1@x>", "<c2@x>"} {
		m := signMsg(t, f.messageAs(t, f.bob, "bob@example.test", id, "", "hi"), "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
		if res := f.p.Handle([]byte(m)); !res.Posted {
			t.Fatalf("%s: %+v", id, res)
		}
	}
	if dns.lookups != 1 {
		t.Fatalf("%d lookups, want 1", dns.lookups)
	}
	f.p.Now = func() time.Time { return time.Now().Add(keyCacheTTL + time.Minute) }
	f.p.lookupKey("rsa._domainkey.example.test")
	if dns.lookups != 2 {
		t.Fatalf("%d lookups after the TTL, want 2", dns.lookups)
	}
}

func TestKeyCacheBounded(t *testing.T) {
	var c keyCache
	now := time.Now()
	for i := 0; i < keyCacheSize*2; i++ {
		c.put(strings.Repeat("x", i+1), nil, now)
	}
	if len(c.m) > keyCacheSize {
		t.Fatalf("%d entries", len(c.m))
	}
}

// A temporary DNS failure leaves the message unseen for the next poll.
func TestDKIMRetryInDrain(t *testing.T) {
	f, _ := dkimSetup(t)
	m := signMsg(t, f.message(t, "bob@example.test", "hi"), "example.test", "temp", rsaKey, dkim.CanonicalizationRelaxed, nil)
	mb := &fakeMailbox{seen: map[uint32]bool{}, msgs: map[uint32][]byte{1: []byte(m)}}
	if err := f.p.Drain(mb); err != nil {
		t.Fatal(err)
	}
	if mb.seen[1] || f.p.tries[1] != 1 {
		t.Fatalf("seen %v tries %d", mb.seen[1], f.p.tries[1])
	}
}

func TestLookupKeyErrors(t *testing.T) {
	p := &Processor{LookupTXT: (&fakeDNS{}).lookup}
	var de *net.DNSError
	if _, err := p.lookupKey("gone._domainkey.x"); !errors.As(err, &de) || !de.IsNotFound || de.Temporary() {
		t.Fatalf("not found: %v", err)
	}
	for _, sel := range []string{"temp", "timeout"} {
		if _, err := p.lookupKey(sel + "._domainkey.x"); !errors.As(err, &de) || !de.Temporary() {
			t.Fatalf("%s: %v", sel, err)
		}
	}
}

// "From : x" is a field net/mail files under "From " and the dkim
// package under "From". mallory, at the same provider domain as bob,
// signs her own From, rewrites it as "From : ..." (relaxed
// canonicalization still verifies it) and adds "From: bob" above:
// net/mail reads bob as the sender, the signature covers mallory.
func TestDKIMFromWithSpaceBeforeColon(t *testing.T) {
	f, _ := dkimSetup(t)
	m := signMsg(t, f.messageAs(t, f.bob, "mallory@example.test", "<poc@x>", "", "hi"), "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
	m = strings.Replace(m, "From: Someone <mallory@example.test>", "From: Someone <bob@example.test>\r\nFrom : Someone <mallory@example.test>", 1)
	res := f.p.Handle([]byte(m))
	if res.Posted || !strings.Contains(res.Reason, "malformed header field name") {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(f.refusalReasons(t), "malformed header field name") {
		t.Fatal("refusal not audited")
	}
}

func replyAddr(t *testing.T, f *fixture) string {
	return mailreply.Address(replyBase, f.token(t, f.bob))
}

func TestDKIMTokenBinding(t *testing.T) {
	var relaxed dkim.Canonicalization = dkim.CanonicalizationRelaxed
	for _, c := range []struct {
		name   string
		build  func(t *testing.T, f *fixture) string
		reason string
	}{
		{"reply address in Delivered-To only", func(t *testing.T, f *fixture) string {
			m := f.messageAs(t, f.bob, "bob@example.test", "<b1@x>", "Delivered-To: "+replyAddr(t, f)+"\r\n", "hi")
			m = strings.Replace(m, "To: gitbay <"+replyAddr(t, f)+">", "To: friend@example.test", 1)
			return signMsg(t, m, "example.test", "rsa", rsaKey, relaxed, nil)
		}, "reply address not in To or Cc"},
		{"To not in h=", func(t *testing.T, f *fixture) string {
			return signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<b2@x>", "", "hi"), "example.test", "rsa", rsaKey, relaxed,
				func(o *dkim.SignOptions) { o.HeaderKeys = []string{"from", "subject", "message-id", "content-type"} })
		}, "to not in h="},
		{"replayed with the reply address added in an unsigned Cc", func(t *testing.T, f *fixture) string {
			m := f.messageAs(t, f.bob, "bob@example.test", "<b3@x>", "", "hi")
			m = strings.Replace(m, "To: gitbay <"+replyAddr(t, f)+">", "To: friend@example.test", 1)
			m = signMsg(t, m, "example.test", "rsa", rsaKey, relaxed, nil)
			return "Cc: " + replyAddr(t, f) + "\r\n" + m
		}, "cc not in h="},
		{"replayed with the To replaced", func(t *testing.T, f *fixture) string {
			m := f.messageAs(t, f.bob, "bob@example.test", "<b4@x>", "", "hi")
			m = strings.Replace(m, "To: gitbay <"+replyAddr(t, f)+">", "To: friend@example.test", 1)
			m = signMsg(t, m, "example.test", "rsa", rsaKey, relaxed, nil)
			return strings.Replace(m, "To: friend@example.test", "To: "+replyAddr(t, f), 1)
		}, "signature did not verify"},
		{"second To field", func(t *testing.T, f *fixture) string {
			m := signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<b5@x>", "", "hi"), "example.test", "rsa", rsaKey, relaxed, nil)
			return "To: other@example.test\r\n" + m
		}, "more than one to field"},
		{"Message-ID not in h=", func(t *testing.T, f *fixture) string {
			return signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<b6@x>", "", "hi"), "example.test", "rsa", rsaKey, relaxed,
				func(o *dkim.SignOptions) { o.HeaderKeys = []string{"from", "to", "subject", "content-type"} })
		}, "message-id not in h="},
		{"Content-Type not in h=", func(t *testing.T, f *fixture) string {
			return signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<b7@x>", "", "hi"), "example.test", "rsa", rsaKey, relaxed,
				func(o *dkim.SignOptions) { o.HeaderKeys = []string{"from", "to", "subject", "message-id"} })
		}, "content-type not in h="},
		{"unsigned quoted-printable Content-Transfer-Encoding", func(t *testing.T, f *fixture) string {
			return signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<b8@x>", "Content-Transfer-Encoding: quoted-printable\r\n", "hi"),
				"example.test", "rsa", rsaKey, relaxed, nil)
		}, "content-transfer-encoding not in h= and not 7bit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := dkimSetup(t)
			res := f.p.Handle([]byte(c.build(t, f)))
			if res.Posted || res.Retry || !strings.Contains(res.Reason, c.reason) {
				t.Fatalf("result %+v, want %q", res, c.reason)
			}
			if !strings.Contains(f.refusalReasons(t), c.reason) {
				t.Fatal("refusal not audited")
			}
		})
	}
}

// Content-Transfer-Encoding in h= passes when the message has one, and
// a reply address in a signed Cc passes.
func TestDKIMSignedCTEAndCc(t *testing.T) {
	f, _ := dkimSetup(t)
	keys := append([]string{"content-transfer-encoding"}, exampleKeys...)
	m := signMsg(t, f.messageAs(t, f.bob, "bob@example.test", "<p1@x>", "Content-Transfer-Encoding: 7bit\r\n", "hi"),
		"example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, func(o *dkim.SignOptions) { o.HeaderKeys = keys })
	if res := f.p.Handle([]byte(m)); !res.Posted {
		t.Fatalf("CTE signed: %+v", res)
	}
	m = f.messageAs(t, f.bob, "bob@example.test", "<p2@x>", "", "hi")
	m = strings.Replace(m, "To: gitbay <"+replyAddr(t, f)+">", "To: friend@example.test\r\nCc: "+replyAddr(t, f), 1)
	m = signMsg(t, m, "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed,
		func(o *dkim.SignOptions) { o.HeaderKeys = append([]string{"cc"}, exampleKeys...) })
	if res := f.p.Handle([]byte(m)); !res.Posted {
		t.Fatalf("signed Cc: %+v", res)
	}
}

// With only trusted_authserv_id set, the reply address may still come
// from Delivered-To.
func TestAuthservTokenFromDeliveredTo(t *testing.T) {
	f := setup(t)
	f.p.Cfg.Mail.Inbound.TrustedAuthservID = "mx.example.net"
	m := f.messageAs(t, f.bob, "bob@example.test", "<ar@x>",
		"Authentication-Results: mx.example.net; dmarc=pass header.from=example.test\r\nDelivered-To: "+replyAddr(t, f)+"\r\n", "hi")
	m = strings.Replace(m, "To: gitbay <"+replyAddr(t, f)+">", "To: friend@example.test", 1)
	if res := f.p.Handle([]byte(m)); !res.Posted {
		t.Fatalf("result %+v", res)
	}
}

// A copy of a signed message posts once, whatever unsigned fields or
// signatures were changed on the way.
func TestDKIMDedupeBySignature(t *testing.T) {
	f, _ := dkimSetup(t)
	m := f.messageAs(t, f.bob, "bob@example.test", "<gone@x>", "", "hi")
	m = strings.Replace(m, "Message-ID: <gone@x>\r\n", "", 1)
	m = signMsg(t, m, "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil)
	if res := f.p.Handle([]byte(m)); !res.Posted {
		t.Fatalf("first: %+v", res)
	}
	if res := f.p.Handle([]byte("X-Resent: 1\r\n" + m)); res.Posted || !strings.Contains(res.Reason, "already posted") {
		t.Fatalf("copy with an unsigned field added: %+v", res)
	}

	msg := f.messageAs(t, f.bob, "bob@example.test", "<dual@x>", "", "hi")
	rsaSig := strings.TrimSuffix(signMsg(t, msg, "example.test", "rsa", rsaKey, dkim.CanonicalizationRelaxed, nil), msg)
	edSigned := signMsg(t, msg, "example.test", "ed", edKey, dkim.CanonicalizationRelaxed, nil)
	if res := f.p.Handle([]byte(rsaSig + edSigned)); !res.Posted {
		t.Fatalf("dual-signed: %+v", res)
	}
	if res := f.p.Handle([]byte(edSigned)); res.Posted || !strings.Contains(res.Reason, "already posted") {
		t.Fatalf("copy with one signature stripped: %+v", res)
	}
	if n := len(f.comments(t)); n != 2 {
		t.Fatalf("%d comments", n)
	}
}

func TestParseRawHeader(t *testing.T) {
	for _, c := range []struct{ raw, reason string }{
		{"From: a@b\r\nTo: c@d\r\n\r\nbody", ""},
		{"From: a@b\n Subject-ish continuation\nTo: c@d\n\nbody", ""},
		{"From : a@b\r\n\r\n", "malformed header field name"},
		{"From\t: a@b\r\n\r\n", "malformed header field name"},
		{"Fr\xc3\xb6m: a@b\r\nFrom: a@b\r\n\r\n", "malformed header field name"},
		{" From: a@b\r\n\r\n", "malformed header"},
		{"From: a@b\r\nnot a field\r\n\r\n", "malformed header"},
		{"From: a@b\r\n: x\r\n\r\n", "malformed header"},
		{"To: c@d\r\n\r\n", "no From field"},
		{"From: a@b\r\nfROM: c@d\r\n\r\n", "more than one From field"},
		{"From: a@b\r\nMessage-Id: 1\r\nMESSAGE-ID: 2\r\n\r\n", "more than one message-id field"},
		{"From: a@b\r\n\r\nFrom : in the body is fine\r\n", ""},
	} {
		if _, got := parseRawHeader([]byte(c.raw)); got != c.reason {
			t.Errorf("%q: %q, want %q", c.raw, got, c.reason)
		}
	}
	h, _ := parseRawHeader([]byte("DKIM-Signature: v=1; b=ab\r\n cd ;d=x\r\nFrom: a@b\r\ndkim-signature: b= ef\r\n\r\n"))
	if len(h.dkimB) != 2 || h.dkimB[0] != "abcd" || h.dkimB[1] != "ef" {
		t.Fatalf("dkimB = %q", h.dkimB)
	}
	if h, _ := parseRawHeader([]byte("From: a@b\r\nContent-Transfer-Encoding:\r\n Quoted-Printable \r\n\r\n")); h.cte != "quoted-printable" {
		t.Fatalf("cte = %q", h.cte)
	}
}

// The shape Thunderbird sends through Migadu: Content-Transfer-Encoding
// outside h=. An identity encoding posts; one that changes the decoded
// body, added unsigned, is refused.
func TestDKIMUnsignedCTE(t *testing.T) {
	for _, c := range []struct {
		cte  string
		post bool
	}{{"7bit", true}, {" 8BIT ", true}, {"binary", true}, {"quoted-printable", false}, {"base64", false}} {
		f, _ := dkimSetup(t)
		msg := "From: Bob <bob@example.test>\r\nTo: " + replyAddr(t, f) + "\r\nSubject: Re: [alice/app] #1: title\r\n" +
			"Date: Tue, 29 Sep 2026 12:00:00 -0500\r\nMessage-ID: <tb-" + strings.TrimSpace(c.cte) + "@example.test>\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=UTF-8; format=flowed\r\nContent-Transfer-Encoding:" + c.cte + "\r\n\r\nThunderbird reply.\r\n"
		m := signMsg(t, msg, "example.test", "rsa", rsaKey, dkim.CanonicalizationSimple, nil)
		if !strings.Contains(m, "a=rsa-sha256;") || !strings.Contains(m, "c=simple/simple;") || !strings.Contains(m, "d=example.test;") ||
			!strings.Contains(m, "h=from:to:subject:date:message-id:mime-version:content-type;") {
			t.Fatalf("signature not in the expected shape:\n%s", m)
		}
		res := f.p.Handle([]byte(m))
		if res.Posted != c.post {
			t.Fatalf("CTE %q: posted %v, want %v (%+v)", c.cte, res.Posted, c.post, res)
		}
		if !c.post && !strings.Contains(res.Reason, "content-transfer-encoding not in h=") {
			t.Fatalf("CTE %q: reason %q", c.cte, res.Reason)
		}
	}
}
