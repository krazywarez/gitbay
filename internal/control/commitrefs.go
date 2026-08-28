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
		for _, n := range closingRefs(m.Message) {
			closes[n] = true
		}
		refs := map[int64]bool{}
		for _, g := range refPat.FindAllStringSubmatch(m.Message, -1) {
			if n, err := strconv.ParseInt(g[2], 10, 64); err == nil && !closes[n] {
				refs[n] = true
			}
		}
		subject, _, _ := strings.Cut(m.Message, "\n")
		author := authorLink(st, m.AuthorName, m.AuthorEmail)
		for n := range closes {
			actOnIssue(st, repo, actorID, m.SHA, n, true, subject, author)
		}
		for n := range refs {
			actOnIssue(st, repo, actorID, m.SHA, n, false, subject, author)
		}
	}
}

// ProcessMRDescription acts on closing keywords in a merged merge
// request's title and body. Commit messages remain the primary record —
// they are what lands — but the intent is written in the merge request
// just as often, and a "Closes #N" there used to close nothing.
//
// Acting once is guaranteed by the state check, not by the dedup key: a
// commit that closed the issue leaves it closed, and this skips it. The
// key is per merge request rather than the merged sha, because sharing
// the sha let a bare "#N" in a commit message claim it first and silently
// suppress the close.
func ProcessMRDescription(st *store.Store, repo store.Repo, mr store.MR, actorID int64) {
	for _, n := range closingRefs(mr.Title + "\n" + mr.Body) {
		issue, err := st.IssueByNumber(repo.ID, n)
		if err != nil || issue.State != "open" {
			continue // no such issue, or a commit already closed it
		}
		fresh, err := st.TryRecordCommitRef(issue.ID, mrRefKey(mr.Number))
		if err != nil || !fresh {
			continue // this merge request already acted on this issue
		}
		if err := st.SetIssueState(issue.ID, "closed"); err != nil {
			slog.Error("mr refs: closing issue", "issue", n, "err", err)
			continue
		}
		link := fmt.Sprintf("[!%d](/%s/mrs/%d)", mr.Number, repo.Path(), mr.Number)
		st.AddIssueSystemComment(issue.ID, actorID,
			fmt.Sprintf("closed by merge request %s: %s", link, mr.Title))
		st.RecordEvent(repo.ID, actorID, "issue.closed",
			fmt.Sprintf(`{"number":%d,"mr":%d}`, n, mr.Number))
	}
}

// mrRefKey namespaces a merge request's dedup record so it cannot
// collide with a commit sha.
func mrRefKey(number int64) string {
	return fmt.Sprintf("mr-%d", number)
}

// closingRefs returns the issue numbers a text closes, in no order.
func closingRefs(text string) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, g := range closePat.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseInt(g[1], 10, 64)
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
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

// authorLink renders the commit's author for a system comment: a link to
// their profile when the author email is verified on an account here, and
// the name git recorded otherwise.
func authorLink(st *store.Store, name, email string) string {
	if user, ok := st.UsernameByVerifiedEmail(email); ok {
		return fmt.Sprintf("[%s](/%s)", user, user)
	}
	if name == "" {
		return email
	}
	return name
}

func actOnIssue(st *store.Store, repo store.Repo, actorID int64, sha string, number int64, close bool, subject, author string) {
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
		st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("closed by commit %s by %s: %s", link, author, subject))
		st.RecordEvent(repo.ID, actorID, "issue.closed", fmt.Sprintf(`{"number":%d,"sha":%q}`, number, sha))
		return
	}
	st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("referenced in commit %s by %s: %s", link, author, subject))
}
