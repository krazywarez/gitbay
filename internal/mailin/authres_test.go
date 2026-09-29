package mailin

import (
	"net/mail"
	"strings"
	"testing"
)

func arHeader(values ...string) mail.Header {
	return mail.Header{"Authentication-Results": values}
}

func TestAuthenticatedCrafted(t *testing.T) {
	const id = "mx.example.net"
	for _, tc := range []struct {
		name, value, from string
		pass              bool
	}{
		{"plain dmarc pass", `mx.example.net; dmarc=pass header.from=victim.example`, "a@victim.example", true},
		{"quoted local part in smtp.mailfrom",
			`mx.example.net; spf=pass smtp.mailfrom="x; dmarc=pass header.from=victim.example y"@evil.example; dmarc=pass header.from=evil.example`,
			"a@victim.example", false},
		{"quoted reason",
			`mx.example.net; spf=fail reason="bad; dmarc=pass header.from=victim.example"; dmarc=fail header.from=victim.example`,
			"a@victim.example", false},
		{"comment containing a fake result",
			`mx.example.net; spf=none (sender says; dmarc=pass header.from=victim.example) smtp.mailfrom=evil.example; dmarc=fail header.from=victim.example`,
			"a@victim.example", false},
		{"escaped quote inside a quoted string",
			`mx.example.net; spf=pass smtp.mailfrom="x\"; dmarc=pass header.from=victim.example; \"y"@evil.example`,
			"a@victim.example", false},
		{"nested comment with an escaped paren",
			`mx.example.net; spf=none (a (b\) ; dmarc=pass header.from=victim.example) c); dmarc=fail header.from=victim.example`,
			"a@victim.example", false},
		{"quoted header.from value", `mx.example.net; dmarc=pass header.from="victim.example"`, "a@victim.example", false},
		{"dkim on a public suffix", `mx.example.net; dkim=pass header.d=github.io`, "bob@user.github.io", false},
		{"dkim relaxed alignment", `mx.example.net; dkim=pass header.d=example.com`, "a@mail.example.com", true},
		{"dkim unrelated domain", `mx.example.net; dkim=pass header.d=example.org`, "a@example.com", false},
		{"dkim header.i only", `mx.example.net; dkim=pass header.i=@example.com`, "a@example.com", false},
		{"property named like a method",
			`mx.example.net; spf=pass dmarc=pass header.from=victim.example`, "a@victim.example", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := authenticated(arHeader(tc.value), id, tc.from)
			if (got == "") != tc.pass {
				t.Fatalf("authenticated = %q, want pass %v", got, tc.pass)
			}
		})
	}
}

// FuzzAuthResults: no input panics, and a header whose only mention of
// the victim domain is inside a quoted string or a comment never
// passes. Prefix and suffix are arbitrary text with the characters that
// could open or close a quote or comment removed, so the wrapped payload
// stays wrapped.
func FuzzAuthResults(f *testing.F) {
	f.Add("spf=pass smtp.mailfrom=", "@evil.example; dmarc=pass header.from=evil.example", 0, "x; dmarc=pass header.from=victim.example y")
	f.Add("spf=none ", "; dkim=pass header.d=evil.example", 1, "dmarc=pass header.from=victim.example")
	f.Add("", "", 2, `a\"; dkim=pass header.d=victim.example; \"b`)
	strip := strings.NewReplacer(`"`, "", "(", "", ")", "", `\`, "")
	f.Fuzz(func(t *testing.T, prefix, suffix string, wrap int, payload string) {
		authenticated(arHeader(prefix+payload+suffix), "mx.example.net", "a@victim.example")
		prefix, suffix = strip.Replace(prefix), strip.Replace(suffix)
		if strings.Contains(strings.ToLower(prefix+suffix), "victim") {
			return
		}
		var wrapped string
		switch wrap % 3 {
		case 0: // a quoted string, escapes kept balanced
			wrapped = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(payload) + `"`
		case 1: // a comment, parentheses escaped
			wrapped = "(" + strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(payload) + ")"
		default: // payload already escaped by the fuzzer, inside quotes
			for i := 0; i < len(payload); i++ {
				if payload[i] == '\\' {
					if i+1 == len(payload) {
						return // it would escape the closing quote
					}
					i++
				} else if payload[i] == '"' {
					return // an unescaped quote ends the string early
				}
			}
			wrapped = `"` + payload + `"`
		}
		v := "mx.example.net; " + prefix + wrapped + suffix
		if authenticated(arHeader(v), "mx.example.net", "a@victim.example") == "" {
			t.Fatalf("passed with the victim domain only inside a quote or comment: %q", v)
		}
	})
}
