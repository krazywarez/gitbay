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
	Verb  string // "opened issue", "merged"
	Ref   string // "#12", "!35", "v0.4.0"
	Repo  string
	URL   string
	When  string
}

// feedLines turns stored events into readable lines. An unknown kind
// still shows: the feed says what happened even for events added later.
func feedLines(events []store.FeedEvent) []feedLine {
	out := make([]feedLine, 0, len(events))
	for _, e := range events {
		var d struct {
			Number int64  `json:"number"`
			Job    string `json:"job"`
			Tag    string `json:"tag"`
		}
		json.Unmarshal([]byte(e.Data), &d)

		l := feedLine{Actor: e.Actor, Repo: e.RepoPath, When: e.CreatedAt}
		if l.Actor == "" {
			l.Actor = "gitbay"
		}
		kind, rest, _ := strings.Cut(e.Kind, ".")
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
