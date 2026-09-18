package httpd

import (
	"encoding/json"
	"fmt"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// feedLine is one activity entry, already phrased and linked.
type feedLine struct {
	Actor string
	Verb  string // "opened issue", "merged", "ran 2 jobs on"
	Ref   string // "#12", "!35", "v0.4.0", a short sha
	Repo  string
	URL   string
	When  string   // the stored timestamp, for anything still reading it raw
	State string   // a build run's combined status; empty for anything else
	Jobs  []string // job names folded into a build run
	sha   string   // the commit a build event fired on, for fold-matching
}

// feedLines turns stored events into readable lines. An unknown kind
// still shows: the feed says what happened even for events added later.
// Build events on the same commit, adjacent in the input, fold into one
// "run" line (D04): its State is the worst of the folded jobs' outcomes,
// via worstStatus — the same rule the builds tab uses for a run's status.
func feedLines(events []store.FeedEvent) []feedLine {
	out := make([]feedLine, 0, len(events))
	statuses := make([][]string, 0, len(events))
	for _, e := range events {
		var d struct {
			Number int64  `json:"number"`
			Job    string `json:"job"`
			Tag    string `json:"tag"`
			SHA    string `json:"sha"`
		}
		json.Unmarshal([]byte(e.Data), &d)
		kind, rest, _ := strings.Cut(e.Kind, ".")

		if kind == "build" && d.SHA != "" {
			if n := len(out); n > 0 && out[n-1].sha == d.SHA && out[n-1].Repo == e.RepoPath {
				i := n - 1
				out[i].Jobs = append(out[i].Jobs, d.Job)
				statuses[i] = append(statuses[i], rest)
				out[i].Verb = fmt.Sprintf("ran %d jobs on", len(out[i].Jobs))
				out[i].Ref = fmt.Sprintf("%.10s", d.SHA)
				out[i].URL = fmt.Sprintf("/%s/commit/%s", e.RepoPath, d.SHA)
				out[i].State = worstStatus(statuses[i])
				continue
			}
		}

		l := feedLine{Actor: e.Actor, Repo: e.RepoPath, When: e.CreatedAt}
		if l.Actor == "" {
			l.Actor = "gitbay"
		}
		var st []string
		switch kind {
		case "issue":
			l.Verb, l.Ref = issueVerb(rest), fmt.Sprintf("#%d", d.Number)
			l.URL = fmt.Sprintf("/%s/issues/%d", e.RepoPath, d.Number)
		case "mr":
			l.Verb, l.Ref = mrVerb(rest), fmt.Sprintf("!%d", d.Number)
			l.URL = fmt.Sprintf("/%s/mrs/%d", e.RepoPath, d.Number)
		case "build":
			l.Verb, l.Ref = "build "+rest, d.Job
			l.URL = fmt.Sprintf("/%s/builds/%d", e.RepoPath, d.Number)
			if d.SHA != "" {
				l.sha = d.SHA
				l.Jobs = []string{d.Job}
				l.State = rest
				st = []string{rest}
			}
		case "release":
			l.Verb, l.Ref = "released", d.Tag
			l.URL = fmt.Sprintf("/%s/releases", e.RepoPath)
		case "repo":
			l.Verb = "repository " + rest
			l.URL = "/" + e.RepoPath
		default:
			l.Verb = e.Kind
			l.URL = "/" + e.RepoPath
		}
		out = append(out, l)
		statuses = append(statuses, st)
	}
	return out
}

func issueVerb(s string) string {
	switch s {
	case "created":
		return "opened issue"
	case "closed":
		return "closed issue"
	case "reopened":
		return "reopened issue"
	case "commented":
		return "commented on"
	}
	return "issue " + s
}

func mrVerb(s string) string {
	switch s {
	case "created":
		return "opened merge request"
	case "merged":
		return "merged"
	case "commented":
		return "commented on"
	case "closed":
		return "closed merge request"
	}
	return "merge request " + s
}
