package mailin

import (
	"net/mail"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// authenticated checks the sender against the mail host's own verdict:
// the topmost Authentication-Results header (RFC 8601) whose authserv-id
// is authserv. The mail host adds its header above any the message
// arrived with, so a lower header claiming the same id is the sender's
// and is not read. It returns "" when the header shows dmarc=pass for
// the From domain, or dkim=pass with a header.d aligned with it, and the
// refusal's reason otherwise.
func authenticated(h mail.Header, authserv, from string) string {
	_, fromDomain, ok := strings.Cut(strings.ToLower(from), "@")
	if !ok || fromDomain == "" {
		return "no From domain"
	}
	for _, v := range h["Authentication-Results"] {
		segs := splitResults(tokenize(v))
		if len(segs) == 0 || len(segs[0]) == 0 || segs[0][0].kind != tokAtom ||
			!strings.EqualFold(segs[0][0].text, authserv) {
			continue
		}
		for _, seg := range segs[1:] {
			method, result, props, ok := resinfo(seg)
			if !ok || result != "pass" {
				continue
			}
			switch method {
			case "dmarc":
				if props["header.from"] == fromDomain {
					return ""
				}
			case "dkim":
				if aligned(props["header.d"], fromDomain) {
					return ""
				}
			}
		}
		return "sender not authenticated by " + authserv + " (no DMARC pass or aligned DKIM pass)"
	}
	return "no Authentication-Results from " + authserv
}

type tokKind int

const (
	tokAtom tokKind = iota
	tokQuoted
	tokEquals
	tokSemi
)

type token struct {
	kind tokKind
	text string // an atom's text, or a quoted string's content unescaped
	// joined marks a token with no whitespace or comment before it, so
	// "x"@example.org is one value.
	joined bool
}

// tokenize splits a header value into atoms, quoted strings, "=" and
// ";". Comments, nested or not, and whitespace separate tokens and are
// dropped; backslash escapes are honoured in both comments and quoted
// strings, so nothing inside either can end it early. An unterminated
// quoted string or comment runs to the end of the value.
func tokenize(s string) []token {
	var out []token
	joined := false
	emit := func(t token) {
		t.joined = joined
		out = append(out, t)
		joined = true
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			joined = false
			i++
		case c == '(':
			depth := 0
			for ; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == '(' {
					depth++
				} else if s[i] == ')' {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
			}
			joined = false
		case c == '"':
			var b strings.Builder
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
				i++
			}
			i++ // the closing quote
			emit(token{kind: tokQuoted, text: b.String()})
		case c == '=':
			emit(token{kind: tokEquals})
			i++
		case c == ';':
			emit(token{kind: tokSemi})
			i++
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n()\";=\\", rune(s[j])) {
				j++
			}
			if j == i { // a stray backslash
				j++
			}
			emit(token{kind: tokAtom, text: s[i:j]})
			i = j
		}
	}
	return out
}

// splitResults splits tokens at each ";".
func splitResults(ts []token) [][]token {
	segs := [][]token{nil}
	for _, t := range ts {
		if t.kind == tokSemi {
			segs = append(segs, nil)
			continue
		}
		segs[len(segs)-1] = append(segs[len(segs)-1], t)
	}
	return segs
}

// resinfo reads "method[/version]=result" and the "name=value" pairs
// after it. Method, result and each value must be plain atoms; a quoted
// value (a reason, or a quoted local part) is kept only as a quoted
// value and never read as a domain. ok is false when the method does
// not parse.
func resinfo(ts []token) (method, result string, props map[string]string, ok bool) {
	props = map[string]string{}
	if len(ts) < 3 || ts[0].kind != tokAtom || ts[1].kind != tokEquals || ts[2].kind != tokAtom {
		return "", "", props, false
	}
	method, _, _ = strings.Cut(strings.ToLower(ts[0].text), "/")
	result = strings.ToLower(ts[2].text)
	i := 3
	// A value continues through tokens joined to it: "x"@example.org.
	for i < len(ts) && ts[i].joined && ts[i].kind != tokEquals {
		i++
	}
	for i < len(ts) {
		if ts[i].kind != tokAtom || i+2 >= len(ts) || ts[i+1].kind != tokEquals {
			i++
			continue
		}
		name := strings.ToLower(ts[i].text)
		v := ts[i+2]
		j := i + 3
		plain := v.kind == tokAtom
		for j < len(ts) && ts[j].joined && ts[j].kind != tokEquals {
			plain = false
			j++
		}
		// The first value for a name is the one the mail host wrote
		// beside the result.
		if _, seen := props[name]; !seen {
			if plain {
				props[name] = strings.ToLower(v.text)
			} else {
				props[name] = "" // present, but not a plain domain
			}
		}
		i = j
	}
	return method, result, props, true
}

// aligned is DMARC relaxed alignment: the signing domain and the From
// domain have the same organizational domain (public suffix plus one
// label). A domain that is itself a public suffix aligns with nothing.
func aligned(d, from string) bool {
	if d == "" {
		return false
	}
	od, err := publicsuffix.EffectiveTLDPlusOne(d)
	if err != nil {
		return false
	}
	of, err := publicsuffix.EffectiveTLDPlusOne(from)
	return err == nil && od == of
}
