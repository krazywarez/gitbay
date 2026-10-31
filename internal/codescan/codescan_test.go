package codescan

import (
	"strconv"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	raw := []byte(`{"version":"2.1.0","runs":[
	{"tool":{"driver":{"name":"opengrep"}},"results":[
		{"ruleId":"r1","message":{"text":"first\nline"},
		 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"file:///workspace/a/b.go"},"region":{"startLine":3}}}]},
		{"ruleId":"r1","message":{"text":"first\nline"},
		 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"./a/b.go"},"region":{"startLine":3}}}]},
		{"ruleId":"r2","level":"note","message":{"text":"second"},
		 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"a/a.go"},"region":{"startLine":9}}}]}
	]},
	{"tool":{"driver":{"name":"other"}},"results":[{"ruleId":"x","level":"bogus","message":{"text":"no location"}}]}
	]}`)
	got, truncated, err := Parse(raw)
	if err != nil || truncated {
		t.Fatalf("Parse: %v %v", err, truncated)
	}
	if len(got) != 3 {
		t.Fatalf("findings = %+v", got)
	}
	// Sorted by path; the duplicate under two URI spellings folded; the
	// default level applied; whitespace collapsed.
	if got[0].Path != "" || got[0].Level != "warning" || got[0].Tool != "other" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Path != "a/a.go" || got[1].Level != "note" {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].Path != "a/b.go" || got[2].Line != 3 || got[2].Message != "first line" || got[2].Level != "warning" {
		t.Errorf("got[2] = %+v", got[2])
	}
	if _, _, err := Parse([]byte(`{"nope":1}`)); err == nil {
		t.Error("a document without runs parsed")
	}
}

func TestParseTruncates(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"runs":[{"tool":{"driver":{"name":"t"}},"results":[`)
	for i := 0; i < MaxFindings+5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"ruleId":"r","message":{"text":"m` + strings.Repeat("x", i%7) + itoa(i) + `"}}`)
	}
	b.WriteString(`]}]}`)
	got, truncated, err := Parse([]byte(b.String()))
	if err != nil || !truncated || len(got) != MaxFindings {
		t.Fatalf("len %d truncated %v err %v", len(got), truncated, err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// A finding that moves is the same finding.
func TestSameIgnoresLines(t *testing.T) {
	a, _, _ := Parse([]byte(`{"runs":[{"tool":{"driver":{"name":"t"}},"results":[{"ruleId":"r","message":{"text":"m"},
		"locations":[{"physicalLocation":{"artifactLocation":{"uri":"x.go"},"region":{"startLine":1}}}]}]}]}`))
	b, _, _ := Parse([]byte(`{"runs":[{"tool":{"driver":{"name":"t"}},"results":[{"ruleId":"r","message":{"text":"m"},
		"locations":[{"physicalLocation":{"artifactLocation":{"uri":"x.go"},"region":{"startLine":40}}}]}]}]}`))
	if !Same(ByFingerprint(a), ByFingerprint(b)) {
		t.Error("a moved finding counted as a change")
	}
}
