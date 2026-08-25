package control

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// closePat matches closing keywords; refPat matches any same-repo issue
// reference. Cross-repo references stay display-only (autolink) — acting
// across repositories would need its own authorization story.
var (
	closePat = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[ :]+#(\d+)\b`)
	refPat   = regexp.MustCompile(`(^|[\s([{:])#(\d+)\b`)
)

const maxMessageCommits = 100

// ProcessCommitMessages acts on issue references in commits that just
// landed on the default branch (old..new): closing keywords close the
// issue, bare #N leaves a reference comment. Each (issue, sha) pair acts
// at most once, ever. actorID — the pusher or merger — authorizes and
// signs the resulting comments; failures are logged, never fatal, because
// this runs after the push or merge already succeeded.
func ProcessCommitMessages(st *store.Store, dir string, repo store.Repo, actorID int64, old, new string) {
	msgs, err := gitutil.RevListMessages(dir, old, new, maxMessageCommits)
	if err != nil {
		slog.Error("commit refs: listing messages", "repo", repo.Path(), "err", err)
		return
	}
	for _, m := range msgs {
		closes := map[int64]bool{}
		for _, g := range closePat.FindAllStringSubmatch(m.Message, -1) {
			if n, err := strconv.ParseInt(g[1], 10, 64); err == nil {
				closes[n] = true
			}
		}
		refs := map[int64]bool{}
		for _, g := range refPat.FindAllStringSubmatch(m.Message, -1) {
			if n, err := strconv.ParseInt(g[2], 10, 64); err == nil && !closes[n] {
				refs[n] = true
			}
		}
		subject, _, _ := strings.Cut(m.Message, "\n")
		for n := range closes {
			actOnIssue(st, repo, actorID, m.SHA, n, true, subject)
		}
		for n := range refs {
			actOnIssue(st, repo, actorID, m.SHA, n, false, subject)
		}
	}
}

// RecordLandedCommits attributes commits that just landed on the default
// branch to accounts by verified author email, for the activity graph.
// Dedup by (repo, sha) makes rebases and re-runs harmless; unresolvable
// authors are simply not activity.
func RecordLandedCommits(st *store.Store, dir string, repo store.Repo, old, new string) {
	authors, err := gitutil.RevListAuthors(dir, old, new, maxMessageCommits)
	if err != nil {
		return
	}
	for _, a := range authors {
		if uid, ok := st.UserIDByVerifiedEmail(a.Email); ok {
			st.RecordCommitActivity(repo.ID, a.SHA, uid, a.Day)
		}
	}
}

func actOnIssue(st *store.Store, repo store.Repo, actorID int64, sha string, number int64, close bool, subject string) {
	issue, err := st.IssueByNumber(repo.ID, number)
	if err != nil {
		return // no such issue: the reference is just text
	}
	fresh, err := st.TryRecordCommitRef(issue.ID, sha)
	if err != nil || !fresh {
		return
	}
	short := sha
	if len(short) > 10 {
		short = short[:10]
	}
	// Informational system entries, not comments from the pusher; the
	// linked sha renders clickable on the web.
	link := fmt.Sprintf("[%s](/%s/commit/%s)", short, repo.Path(), sha)
	if close && issue.State == "open" {
		if err := st.SetIssueState(issue.ID, "closed"); err != nil {
			slog.Error("commit refs: closing issue", "issue", number, "err", err)
			return
		}
		st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("closed by commit %s: %s", link, subject))
		st.RecordEvent(repo.ID, actorID, "issue.closed", fmt.Sprintf(`{"number":%d,"sha":%q}`, number, sha))
		return
	}
	st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("referenced in commit %s: %s", link, subject))
}
