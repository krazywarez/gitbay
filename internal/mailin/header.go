package mailin

import (
	"bytes"
	"strings"
)

// rawHeader is what the checks read from the header section as
// fetched, before net/mail or the dkim package interpret it.
type rawHeader struct {
	count map[string]int // field name, lower case, to occurrences
	dkimB []string       // b= of each DKIM-Signature, in order, whitespace removed
	cte   string         // Content-Transfer-Encoding, unfolded, trimmed, lower case
}

// single names the fields a reply may carry at most once; From must
// appear exactly once.
var single = []string{"from", "to", "cc", "message-id", "content-type", "content-transfer-encoding"}

// parseRawHeader reads the header section of raw. A field name must be
// RFC 5322 ftext (printable US-ASCII other than ":"), so "From : x",
// which net/mail files under another name than the dkim package does,
// is refused rather than read two ways. It returns the refusal's
// reason, or "".
func parseRawHeader(raw []byte) (rawHeader, string) {
	h := rawHeader{count: map[string]int{}}
	var dkimVals []string
	cur := -1 // index in dkimVals of the DKIM-Signature being continued
	inCTE := false
	for len(raw) > 0 {
		line := raw
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			line, raw = raw[:i], raw[i+1:]
		} else {
			raw = nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(h.count) == 0 {
				return h, "malformed header"
			}
			if cur >= 0 {
				dkimVals[cur] += string(line)
			}
			if inCTE {
				h.cte += string(line)
			}
			continue
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok || len(name) == 0 {
			return h, "malformed header"
		}
		for _, c := range name {
			if c < 33 || c > 126 {
				return h, "malformed header field name"
			}
		}
		n := strings.ToLower(string(name))
		h.count[n]++
		cur = -1
		inCTE = n == "content-transfer-encoding"
		if inCTE {
			h.cte = string(value)
		}
		if n == "dkim-signature" {
			dkimVals = append(dkimVals, string(value))
			cur = len(dkimVals) - 1
		}
	}
	h.cte = strings.ToLower(strings.TrimSpace(h.cte))
	for _, v := range dkimVals {
		h.dkimB = append(h.dkimB, tagB(v))
	}
	if n := h.count["from"]; n != 1 {
		if n == 0 {
			return h, "no From field"
		}
		return h, "more than one From field"
	}
	for _, n := range single[1:] {
		if h.count[n] > 1 {
			return h, "more than one " + n + " field"
		}
	}
	return h, ""
}

// tagB returns the b= tag of a DKIM-Signature value with whitespace
// removed, or "".
func tagB(v string) string {
	for _, t := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(t, "=")
		if ok && strings.TrimSpace(k) == "b" {
			return strings.Join(strings.Fields(val), "")
		}
	}
	return ""
}
