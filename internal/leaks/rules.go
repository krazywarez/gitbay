// Package leaks finds credentials in git content. The rules are token
// formats that identify themselves — a vendor prefix, a fixed length, a
// key block with a body — rather than entropy guesses, so a finding is
// worth stopping a push for. Only where a match is (path, line, rule) ever
// leaves this package; the matched text does not.
package leaks

import (
	"bytes"
	"regexp"
)

// Rule is one credential format. keyword is a literal every match
// contains, checked before the expression so most content costs a substring
// search and nothing else.
type Rule struct {
	ID      string
	keyword string
	re      *regexp.Regexp
}

var rules = []Rule{
	{"private-key", "PRIVATE KEY", regexp.MustCompile(`-----BEGIN[ A-Z0-9]*PRIVATE KEY(?: BLOCK)?-----[ \t]*\r?\n(?:[A-Za-z0-9-]+: [^\n]*\r?\n)*\s*[A-Za-z0-9+/=]{40,}`)},
	{"age-secret-key", "AGE-SECRET-KEY-1", regexp.MustCompile(`AGE-SECRET-KEY-1[QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L]{58}`)},
	{"aws-access-key-id", "A", regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b`)},
	{"github-token", "gh", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b`)},
	{"github-fine-grained-token", "github_pat_", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{82}\b`)},
	{"gitlab-token", "glpat-", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{"gitbay-token", "gb_", regexp.MustCompile(`\bgb_[0-9a-f]{64}\b`)},
	{"slack-token", "xox", regexp.MustCompile(`\bxox[baprs]-[0-9]{6,}-[A-Za-z0-9-]{10,}\b`)},
	{"slack-webhook", "hooks.slack.com", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Z0-9]{6,}/B[A-Z0-9]{6,}/[A-Za-z0-9]{20,}`)},
	{"stripe-live-key", "_live_", regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{24,}\b`)},
	{"google-api-key", "AIza", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"npm-token", "npm_", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"pypi-token", "pypi-AgEIcHlwaS5vcmc", regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_\-]{50,}`)},
	{"openai-key", "sk-", regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_\-]{40,}`)},
	{"anthropic-key", "sk-ant-", regexp.MustCompile(`\bsk-ant-(?:api|admin)[0-9]{2}-[A-Za-z0-9_\-]{80,}`)},
	{"sendgrid-key", "SG.", regexp.MustCompile(`\bSG\.[A-Za-z0-9_\-]{22}\.[A-Za-z0-9_\-]{43}\b`)},
	{"digitalocean-token", "_v1_", regexp.MustCompile(`\bdo[opr]_v1_[a-f0-9]{64}\b`)},
	{"shopify-token", "shp", regexp.MustCompile(`\bshp(?:at|ca|pa|ss)_[a-fA-F0-9]{32}\b`)},
}

// Hit is one match inside a blob.
type Hit struct {
	Line int    `json:"line"`
	Rule string `json:"rule"`
}

// maxHitsPerBlob bounds what one file reports; a file with more is not
// made more urgent by listing them all.
const maxHitsPerBlob = 10

// Find returns where data matches a rule, one hit per rule and line.
func Find(data []byte) []Hit {
	var out []Hit
	for _, r := range rules {
		if !bytes.Contains(data, []byte(r.keyword)) {
			continue
		}
		last := 0
		for _, loc := range r.re.FindAllIndex(data, maxHitsPerBlob) {
			line := 1 + bytes.Count(data[:loc[0]], []byte("\n"))
			if line == last {
				continue
			}
			last = line
			out = append(out, Hit{Line: line, Rule: r.ID})
			if len(out) >= maxHitsPerBlob {
				return out
			}
		}
	}
	return out
}

// binary reports whether data looks like something other than text: a NUL
// in the first 8000 bytes, git's own test.
func binary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}
