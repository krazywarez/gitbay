package codescan

import (
	"gitbay.org/gitbay/internal/store"
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
	for i := 0; i < maxResults+5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"ruleId":"r","message":{"text":"m` + strings.Repeat("x", i%7) + itoa(i) + `"}}`)
	}
	b.WriteString(`]}]}`)
	got, truncated, err := Parse([]byte(b.String()))
	if err != nil || !truncated || len(got) != maxResults {
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

// sarifOf builds a one-run document from results given as JSON objects.
func sarifOf(results ...string) []byte {
	return []byte(`{"runs":[{"tool":{"driver":{"name":"t"}},"results":[` + strings.Join(results, ",") + `]}]}`)
}

func result(line int, snippet, extra string) string {
	region := `"startLine":` + itoa(line)
	if snippet != "" {
		region += `,"snippet":{"text":` + strconv.Quote(snippet) + `}`
	}
	if extra != "" {
		extra = "," + extra
	}
	return `{"ruleId":"r","message":{"text":"m"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"x.go"},"region":{` +
		region + `}}}]` + extra + `}`
}

func fingerprints(t *testing.T, raw []byte) []string {
	t.Helper()
	got, _, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range got {
		out = append(out, f.Fingerprint)
	}
	return out
}

// Hits of one rule in one file are separate findings, and the first keeps
// the fingerprint a scan without snippets always gave it.
func TestParseKeepsAlikeHits(t *testing.T) {
	fps := fingerprints(t, sarifOf(result(3, "", ""), result(9, "", "")))
	if len(fps) != 2 || fps[0] == fps[1] {
		t.Fatalf("fingerprints = %v", fps)
	}
	if want := fingerprint(hit{f: store.SecurityFinding{Tool: "t", Rule: "r", Path: "x.go", Message: "m"}}, map[string]int{}); fps[0] != want {
		t.Errorf("first hit = %s, want %s", fps[0], want)
	}
	// Moved, they are still the same two.
	if moved := fingerprints(t, sarifOf(result(13, "", ""), result(19, "", ""))); strings.Join(moved, " ") != strings.Join(fps, " ") {
		t.Errorf("moved = %v, was %v", moved, fps)
	}
}

// With the matched code, a new hit above others leaves theirs alone, and
// reindenting the code changes nothing.
func TestParseSnippetsSurviveInsertion(t *testing.T) {
	before := fingerprints(t, sarifOf(result(3, "a()", ""), result(9, "b(x, y)", "")))
	after := fingerprints(t, sarifOf(result(1, "c()", ""), result(4, "a()", ""), result(10, "\t  b(x,\n y)", "")))
	if len(after) != 3 || after[1] != before[0] || after[2] != before[1] {
		t.Errorf("before %v after %v", before, after)
	}
}

// The tool's own fingerprints are used when every result has a distinct
// one, and ignored when they are placeholders or shared.
func TestParseOwnFingerprints(t *testing.T) {
	own := func(v string) string { return `"fingerprints":{"matchBasedId/v1":"` + v + `"}` }
	a := fingerprints(t, sarifOf(result(3, "", own("aaa")), result(9, "", own("bbb"))))
	b := fingerprints(t, sarifOf(result(30, "", own("aaa")), result(1, "", own("bbb"))))
	if len(a) != 2 || a[0] != b[1] || a[1] != b[0] {
		t.Errorf("own fingerprints not followed: %v %v", a, b)
	}
	plain := fingerprints(t, sarifOf(result(3, "", ""), result(9, "", "")))
	for _, v := range []string{"requires login", "same"} {
		if got := fingerprints(t, sarifOf(result(3, "", own(v)), result(9, "", own(v)))); strings.Join(got, " ") != strings.Join(plain, " ") {
			t.Errorf("%q: got %v, want the fallback %v", v, got, plain)
		}
	}
}
