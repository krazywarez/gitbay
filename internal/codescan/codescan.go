// Package codescan reads the SARIF a security build uploads and renders
// the issue that reports it. Only the parts of SARIF 2.1.0 a finding needs
// are read: the tool's name, and each result's rule, level, message and
// first location.
package codescan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// IssueTitle is fixed so the issue is recognizable across scans.
const IssueTitle = "Code scanning findings"

// MaxUpload bounds one upload. MaxFindings bounds what is kept from it.
const (
	MaxUpload   = 8 << 20
	MaxFindings = 500
)

type sarif struct {
	Runs []struct {
		Tool struct {
			Driver struct {
				Name string `json:"name"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
						Snippet   struct {
							Text string `json:"text"`
						} `json:"snippet"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
			Fingerprints        map[string]string `json:"fingerprints"`
			PartialFingerprints map[string]string `json:"partialFingerprints"`
		} `json:"results"`
	} `json:"runs"`
}

// Parse returns the findings in a SARIF document, deduplicated by
// fingerprint and sorted by path and line. It reports whether results past
// MaxFindings were dropped.
func Parse(raw []byte) ([]store.SecurityFinding, bool, error) {
	var doc sarif
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("not SARIF: %w", err)
	}
	if doc.Runs == nil {
		return nil, false, errors.New("not SARIF: no runs")
	}
	var hits []hit
	for _, run := range doc.Runs {
		tool := clean(run.Tool.Driver.Name, 40)
		var found []hit
		for _, r := range run.Results {
			h := hit{f: store.SecurityFinding{Tool: tool, Rule: clean(r.RuleID, 200),
				Level: level(r.Level), Message: clean(r.Message.Text, 200)}}
			if len(r.Locations) > 0 {
				loc := r.Locations[0].PhysicalLocation
				h.f.Path = relPath(loc.ArtifactLocation.URI)
				h.f.Line = max(loc.Region.StartLine, 0)
				h.snippet = strings.Join(strings.Fields(loc.Region.Snippet.Text), " ")
			}
			h.own = ownFingerprint(r.Fingerprints, r.PartialFingerprints)
			found = append(found, h)
		}
		if !distinct(found) {
			for i := range found {
				found[i].own = ""
			}
		}
		hits = append(hits, found...)
	}
	// File order, so hits that look alike are numbered the same way on
	// every scan.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].f.Path != hits[j].f.Path {
			return hits[i].f.Path < hits[j].f.Path
		}
		return hits[i].f.Line < hits[j].f.Line
	})
	seen := map[string]bool{}
	alike := map[string]int{}
	var out []store.SecurityFinding
	truncated := false
	for _, h := range hits {
		if seen[h.place()] {
			continue
		}
		seen[h.place()] = true
		if len(out) >= MaxFindings {
			truncated = true
			break
		}
		h.f.Fingerprint = fingerprint(h, alike)
		out = append(out, h.f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out, truncated, nil
}

// hit is one result before its fingerprint is settled: own is the tool's
// fingerprint for it, snippet the code it matched, either possibly empty.
type hit struct {
	f            store.SecurityFinding
	own, snippet string
}

// place is where a result was reported. One reported twice at the same
// place is one finding.
func (h hit) place() string {
	return h.f.Tool + "\x00" + h.f.Rule + "\x00" + h.f.Path + "\x00" + strconv.Itoa(h.f.Line) + "\x00" + h.f.Message
}

// ownFingerprint picks the tool's fingerprint for a result, preferring the
// full ones over partial, and the first by name for a stable choice.
func ownFingerprint(sets ...map[string]string) string {
	for _, set := range sets {
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if v := set[name]; v != "" && !strings.ContainsAny(v, " \t\r\n") && len(v) <= 200 {
				return name + "=" + v
			}
		}
	}
	return ""
}

// distinct reports whether a run's own fingerprints can be trusted: every
// result has one, and no two places share one. A tool that writes a
// placeholder or a per-rule constant there falls back to ours.
func distinct(found []hit) bool {
	at := map[string]string{}
	for _, h := range found {
		if h.own == "" {
			return false
		}
		if p, ok := at[h.own]; ok && p != h.place() {
			return false
		}
		at[h.own] = h.place()
	}
	return true
}

// fingerprint identifies a finding across scans. The line is left out so
// an edit above it does not make it a new finding. The tool's own
// fingerprint is used when it gives one. Otherwise the matched code tells
// apart hits of one rule in one file, and hits that still look alike are
// numbered in file order, the first keeping the plain form; alike counts
// them across calls.
func fingerprint(h hit, alike map[string]int) string {
	key := h.f.Tool + "\x00" + h.f.Rule + "\x00" + h.f.Path + "\x00"
	if h.own != "" {
		key += "own\x00" + h.own
	} else {
		key += h.f.Message
		if h.snippet != "" {
			key += "\x00" + h.snippet
		}
	}
	n := alike[key]
	alike[key]++
	if n > 0 {
		key += "\x00" + strconv.Itoa(n)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:12])
}

// level is SARIF's result level, which defaults to warning.
func level(l string) string {
	switch l {
	case "error", "warning", "note", "none":
		return l
	}
	return "warning"
}

// relPath turns an artifact URI into a repository path: tools write
// `file:///workspace/x.go`, `./x.go` or `x.go` for the same file.
func relPath(uri string) string {
	p := uri
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		p = u.Path
	}
	p = strings.TrimPrefix(p, "/workspace/")
	p = strings.TrimPrefix(p, "./")
	return clean(p, 300)
}

// clean is one line of at most n bytes of valid UTF-8, since every field
// lands in an issue body and a mail.
func clean(s string, n int) string {
	s = strings.ToValidUTF8(strings.Join(strings.Fields(s), " "), "")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

// Same reports whether two sets name the same findings, which decides
// between leaving the issue alone and rewriting it. Both arrive sorted by
// fingerprint. A line moving is not a change.
func Same(a, b []store.SecurityFinding) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Fingerprint != b[i].Fingerprint {
			return false
		}
	}
	return true
}

// ByFingerprint sorts a set the way the store returns it, for Same.
func ByFingerprint(fs []store.SecurityFinding) []store.SecurityFinding {
	out := append([]store.SecurityFinding(nil), fs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

// Body renders the issue: one list per tool, a line per finding.
func Body(branch string, findings []store.SecurityFinding, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The scheduled code scan of `%s` reported these findings. "+
		"This issue is kept up to date as that changes, and closed once a scan reports none.\n", branch)
	byTool := map[string][]store.SecurityFinding{}
	var tools []string
	for _, f := range findings {
		if _, ok := byTool[f.Tool]; !ok {
			tools = append(tools, f.Tool)
		}
		byTool[f.Tool] = append(byTool[f.Tool], f)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		name := tool
		if name == "" {
			name = "Unnamed tool"
		}
		fmt.Fprintf(&b, "\n### %s\n\n", name)
		for _, f := range byTool[tool] {
			fmt.Fprintf(&b, "- `%s:%d` %s (%s): %s\n", f.Path, f.Line, f.Rule, f.Level, f.Message)
		}
	}
	if truncated {
		fmt.Fprintf(&b, "\nOnly the first %d results are listed.\n", MaxFindings)
	}
	return b.String()
}
