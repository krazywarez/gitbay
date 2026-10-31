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
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
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
	seen := map[string]bool{}
	var out []store.SecurityFinding
	truncated := false
	for _, run := range doc.Runs {
		tool := clean(run.Tool.Driver.Name, 40)
		for _, r := range run.Results {
			if len(out) >= MaxFindings {
				truncated = true
				break
			}
			f := store.SecurityFinding{Tool: tool, Rule: clean(r.RuleID, 200),
				Level: level(r.Level), Message: clean(r.Message.Text, 200)}
			if len(r.Locations) > 0 {
				loc := r.Locations[0].PhysicalLocation
				f.Path = relPath(loc.ArtifactLocation.URI)
				f.Line = max(loc.Region.StartLine, 0)
			}
			f.Fingerprint = fingerprint(f)
			if seen[f.Fingerprint] {
				continue
			}
			seen[f.Fingerprint] = true
			out = append(out, f)
		}
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

// fingerprint identifies a finding across scans. The line is left out so
// an edit above it does not make it a new finding.
func fingerprint(f store.SecurityFinding) string {
	sum := sha256.Sum256([]byte(f.Tool + "\x00" + f.Rule + "\x00" + f.Path + "\x00" + f.Message))
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
