package mailin

import (
	"net/mail"
	"strings"
)

// authenticated checks the sender against the mail host's own verdict:
// the topmost Authentication-Results header (RFC 8601) whose authserv-id
// is authserv. The mail host adds its header above any the message
// arrived with, so a lower header claiming the same id is the sender's
// and is not read. It returns "" when the header shows dmarc=pass for
// the From domain, or dkim=pass with a signing domain aligned with it,
// and the refusal's reason otherwise.
func authenticated(h mail.Header, authserv, from string) string {
	_, fromDomain, ok := strings.Cut(strings.ToLower(from), "@")
	if !ok || fromDomain == "" {
		return "no From domain"
	}
	for _, v := range h["Authentication-Results"] {
		parts := strings.Split(stripComments(v), ";")
		f := strings.Fields(parts[0])
		if len(f) == 0 || !strings.EqualFold(f[0], authserv) {
			continue
		}
		for _, r := range parts[1:] {
			method, result, props := resinfo(r)
			if result != "pass" {
				continue
			}
			switch method {
			case "dmarc":
				if strings.EqualFold(props["header.from"], fromDomain) {
					return ""
				}
			case "dkim":
				d := props["header.d"]
				if d == "" {
					_, d, _ = strings.Cut(props["header.i"], "@")
				}
				if aligned(strings.ToLower(d), fromDomain) {
					return ""
				}
			}
		}
		return "sender not authenticated by " + authserv + " (no DMARC pass or aligned DKIM pass)"
	}
	return "no Authentication-Results from " + authserv
}

// resinfo splits "method[/version]=result prop=value ..." into its
// method, result and properties, all lower case.
func resinfo(s string) (string, string, map[string]string) {
	props := map[string]string{}
	f := strings.Fields(s)
	if len(f) == 0 {
		return "", "", props
	}
	method, result, _ := strings.Cut(strings.ToLower(f[0]), "=")
	method, _, _ = strings.Cut(method, "/")
	for _, kv := range f[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			props[strings.ToLower(k)] = strings.ToLower(strings.Trim(v, `"`))
		}
	}
	return method, result, props
}

// aligned reports relaxed alignment, kept simple: the signing domain is
// the From domain, or one is a subdomain of the other.
func aligned(d, from string) bool {
	if !strings.Contains(d, ".") {
		return false
	}
	return d == from || strings.HasSuffix(from, "."+d) || strings.HasSuffix(d, "."+from)
}

// stripComments removes RFC 5322 comments, "(...)", nested or not,
// outside quoted strings.
func stripComments(s string) string {
	var b strings.Builder
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			if depth == 0 {
				b.WriteByte(c)
				b.WriteByte(s[i+1])
			}
			i++
		case c == '"' && depth == 0:
			quoted = !quoted
			b.WriteByte(c)
		case c == '(' && !quoted:
			depth++
		case c == ')' && !quoted && depth > 0:
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}
