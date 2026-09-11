package control

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// closePat matches closing keywords, with an optional owner/name before
// the number for an issue in another repository; refPat matches any bare
// same-repo reference. A cross-repo close acts only when the actor holds
// write on the target (closeTarget); a bare cross-repo reference stays
// display-only.
var (
	closePat = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[ :]+(?:([a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*))?#(\d+)\b`)
	refPat   = regexp.MustCompile(`(^|[\s([{:])#(\d+)\b`)
)

// closeRef is one closing reference: Path is "" for the same repository.
type closeRef struct {
	Path string
	N    int64
}

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
		closes := closingRefs(m.Message)
		local := map[int64]bool{}
		for _, ref := range closes {
			if ref.Path == "" {
				local[ref.N] = true
			}
		}
		refs := map[int64]bool{}
		for _, g := range refPat.FindAllStringSubmatch(m.Message, -1) {
			if n, err := strconv.ParseInt(g[2], 10, 64); err == nil && !local[n] {
				refs[n] = true
			}
		}
		subject, _, _ := strings.Cut(m.Message, "\n")
		author := authorLink(st, m.AuthorName, m.AuthorEmail)
		for _, ref := range closes {
			target, ok := closeTarget(st, repo, actorID, ref.Path)
			if !ok {
				continue
			}
			actOnIssue(st, repo, target, actorID, m.SHA, ref.N, true, subject, author)
		}
		for n := range refs {
			actOnIssue(st, repo, repo, actorID, m.SHA, n, false, subject, author)
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
	for _, ref := range closingRefs(mr.Title + "\n" + mr.Body) {
		target, ok := closeTarget(st, repo, actorID, ref.Path)
		if !ok {
			continue
		}
		issue, err := st.IssueByNumber(target.ID, ref.N)
		if err != nil || issue.State != "open" {
			continue // no such issue, or a commit already closed it
		}
		fresh, err := st.TryRecordCommitRef(issue.ID, mrRefKey(mr.Number))
		if err != nil || !fresh {
			continue // this merge request already acted on this issue
		}
		if err := st.SetIssueState(issue.ID, "closed"); err != nil {
			slog.Error("mr refs: closing issue", "issue", ref.N, "err", err)
			continue
		}
		link := fmt.Sprintf("[!%d](/%s/mrs/%d)", mr.Number, repo.Path(), mr.Number)
		st.AddIssueSystemComment(issue.ID, actorID,
			fmt.Sprintf("closed by merge request %s: %s", link, mr.Title))
		st.RecordEvent(target.ID, actorID, "issue.closed",
			fmt.Sprintf(`{"number":%d,"mr":%d}`, ref.N, mr.Number))
	}
}

// mrRefKey namespaces a merge request's dedup record so it cannot
// collide with a commit sha.
func mrRefKey(number int64) string {
	return fmt.Sprintf("mr-%d", number)
}

// closingRefs returns the references a text closes, in no order.
func closingRefs(text string) []closeRef {
	seen := map[closeRef]bool{}
	var out []closeRef
	for _, g := range closePat.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseInt(g[2], 10, 64)
		if err != nil {
			continue
		}
		ref := closeRef{Path: strings.ToLower(g[1]), N: n}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// closeTarget resolves where a closing reference acts: the source
// repository for a bare #N, or the named repository when the actor holds
// write there. false means the reference stays text; nothing is logged
// above debug, since a refusal must not confirm the target exists.
func closeTarget(st *store.Store, source store.Repo, actorID int64, path string) (store.Repo, bool) {
	if path == "" {
		return source, true
	}
	target, err := st.RepoByPath(path)
	if err != nil {
		return store.Repo{}, false
	}
	actor, err := st.UserByID(actorID)
	if err != nil {
		return store.Repo{}, false
	}
	grant, err := st.AccessRole(target.ID, actorID)
	if err != nil {
		return store.Repo{}, false
	}
	if !policy.CanWrite(actor, target, grant) {
		slog.Debug("commit refs: cross-repo close refused", "source", source.Path(), "target", path)
		return store.Repo{}, false
	}
	return target, true
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

func actOnIssue(st *store.Store, source, target store.Repo, actorID int64, sha string, number int64, close bool, subject, author string) {
	issue, err := st.IssueByNumber(target.ID, number)
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
	link := fmt.Sprintf("[%s](/%s/commit/%s)", short, source.Path(), sha)
	if close && issue.State == "open" {
		if err := st.SetIssueState(issue.ID, "closed"); err != nil {
			slog.Error("commit refs: closing issue", "issue", number, "err", err)
			return
		}
		st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("closed by commit %s by %s: %s", link, author, subject))
		st.RecordEvent(target.ID, actorID, "issue.closed", fmt.Sprintf(`{"number":%d,"sha":%q}`, number, sha))
		return
	}
	st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("referenced in commit %s by %s: %s", link, author, subject))
}
