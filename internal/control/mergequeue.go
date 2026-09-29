package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// QueuedOut is a merge request's queued merge (mr merge --when-ready):
// who queued it, the strategy it will use ("" for the default), and why
// the last attempt did not merge.
type QueuedOut struct {
	By       string `json:"by"`
	Strategy string `json:"strategy,omitempty"`
	Reason   string `json:"reason,omitempty"`
	QueuedAt string `json:"queued_at"`
}

func queuedOut(m store.MR) *QueuedOut {
	if m.QueuedAt == "" {
		return nil
	}
	return &QueuedOut{By: m.QueuedBy, Strategy: m.QueueStrategy, Reason: m.QueueReason, QueuedAt: m.QueuedAt}
}

// mergeQueueMu serialises queued merge attempts, so two triggers landing
// together cannot both merge one request.
var mergeQueueMu sync.Mutex

type queueResult struct {
	merged   bool
	dequeued bool
	reason   string
	out      map[string]any // the merge's output, when merged
}

// TryQueuedMerge attempts the queued merge of one merge request, if it
// has one. Called wherever a gate can have changed: a review, a resolved
// thread, a draft marked ready.
func TryQueuedMerge(st *store.Store, cfg config.Config, mrID int64) {
	mergeQueueMu.Lock()
	defer mergeQueueMu.Unlock()
	attemptLocked(st, cfg, mrID)
}

// TryQueuedMergesAt attempts the queued merges in a repository whose head
// is sha. Called when a status is reported on sha.
func TryQueuedMergesAt(st *store.Store, cfg config.Config, repoID int64, sha string) {
	ids, err := st.QueuedMRsAtHead(repoID, sha)
	if err != nil {
		slog.Error("merge queue: listing", "repo", repoID, "err", err)
		return
	}
	for _, id := range ids {
		TryQueuedMerge(st, cfg, id)
	}
}

// QueuedMergePushed is post-receive's call for a queued merge request
// whose source branch pusherID just pushed or deleted. A push from an
// account that cannot write to the target dequeues it: otherwise the
// queuer's authority would merge commits someone else chose. A push from
// one that can keeps it queued, and the new head has to pass on its own.
func QueuedMergePushed(st *store.Store, cfg config.Config, mrID, pusherID int64) {
	mergeQueueMu.Lock()
	defer mergeQueueMu.Unlock()
	mr, err := st.MRByID(mrID)
	if err != nil || mr.QueuedAt == "" {
		return
	}
	repo, err := st.RepoByID(mr.RepoID)
	if err != nil {
		queueInternalError(st, mr.ID, err)
		return
	}
	pusher, err := st.UserByID(pusherID)
	if err != nil {
		queueInternalError(st, mr.ID, err)
		return
	}
	grant, err := st.AccessRole(repo.ID, pusher.ID)
	if err != nil {
		queueInternalError(st, mr.ID, err)
		return
	}
	if !policy.CanWrite(pusher, repo, grant) {
		dequeueWithReason(st, mr, fmt.Sprintf("%s pushed and cannot merge into %s", pusher.Username, repo.Path()))
		return
	}
	attemptLocked(st, cfg, mrID)
}

// queueSource is Ctx.Source for a merge the queue performs.
const queueSource = "queue"

// queueInternalReason is what a queued merge says while a lookup fails.
const queueInternalReason = "internal error, will retry on the next event"

func queueInternalError(st *store.Store, mrID int64, err error) queueResult {
	slog.Error("merge queue", "mr", mrID, "err", err)
	st.SetMergeQueueReason(mrID, queueInternalReason)
	return queueResult{reason: queueInternalReason}
}

// attemptLocked merges a queued request as the user who queued it,
// checked against that user's rights and the credential it was queued
// with now. A merge refused for anything the queuer can fix (unmet
// gates, a branch behind a require-signed target, a conflict) stays
// queued with the refusal recorded; a queuer or credential that can no
// longer merge, or a source branch that is gone, dequeues it. The caller
// holds mergeQueueMu.
func attemptLocked(st *store.Store, cfg config.Config, mrID int64) queueResult {
	mr, err := st.MRByID(mrID)
	if errors.Is(err, store.ErrNotFound) {
		return queueResult{}
	}
	if err != nil {
		return queueInternalError(st, mrID, err)
	}
	if mr.QueuedAt == "" {
		return queueResult{}
	}
	if mr.State != "open" {
		return dequeueWithReason(st, mr, "the source branch was deleted")
	}
	repo, err := st.RepoByID(mr.RepoID)
	if err != nil {
		return queueInternalError(st, mr.ID, err)
	}
	user, err := st.UserByID(mr.QueuedByID)
	if err != nil {
		return queueInternalError(st, mr.ID, err)
	}
	grant, err := st.AccessRole(repo.ID, user.ID)
	if err != nil {
		return queueInternalError(st, mr.ID, err)
	}
	switch {
	case user.Disabled || user.Pending:
		return dequeueWithReason(st, mr, user.Username+"'s account is not active")
	case !policy.CanWrite(user, repo, grant):
		return dequeueWithReason(st, mr,
			fmt.Sprintf("%s no longer has write access to %s", user.Username, repo.Path()))
	}
	lapsed, err := queueCredentialLapsed(st, mr.ID, time.Now())
	if err != nil {
		return queueInternalError(st, mr.ID, err)
	}
	if lapsed != "" {
		return dequeueWithReason(st, mr, lapsed)
	}

	var out bytes.Buffer
	c := &Ctx{User: user, Scope: "full", Source: queueSource, Store: st, Cfg: cfg,
		Stdin: emptyReader{}, Stdout: &out, Stderr: io.Discard, JSON: true}
	code := mergeMR(c, repo, mr, mr.QueueStrategy)
	var env struct {
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	json.Unmarshal(out.Bytes(), &env)
	if code == protocol.ExitOK {
		st.Audit(user.ID, "cmd mr merge", map[string]any{
			"argv": []string{repo.Path(), strconv.FormatInt(mr.Number, 10), "--when-ready"}, "source": queueSource})
		return queueResult{merged: true, out: env.Data}
	}
	st.SetMergeQueueReason(mr.ID, env.Error)
	return queueResult{reason: env.Error}
}

// queueCredentialLapsed says why the key or token a merge was queued with
// can no longer carry it: removed, expired, or narrowed below full scope.
// "" means it still can, or the merge was queued from a web session and
// rests on the account alone.
func queueCredentialLapsed(st *store.Store, mrID int64, now time.Time) (string, error) {
	q, err := st.MergeQueueCredential(mrID)
	if err != nil {
		return "", err
	}
	switch q.Kind {
	case "key":
		if q.KeyID == 0 {
			return "the key it was queued with was removed", nil
		}
		k, err := st.SSHKeyByID(q.KeyID)
		if errors.Is(err, store.ErrNotFound) {
			return "the key it was queued with was removed", nil
		}
		if err != nil {
			return "", err
		}
		if k.Expired(now) {
			return "the key it was queued with has expired", nil
		}
		if k.Scope != "full" {
			return "the key it was queued with no longer has full scope", nil
		}
	case "token":
		if q.TokenID == 0 {
			return "the token it was queued with was revoked", nil
		}
		t, err := st.APITokenByID(q.TokenID)
		if errors.Is(err, store.ErrNotFound) {
			return "the token it was queued with was revoked", nil
		}
		if err != nil {
			return "", err
		}
		if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
			return "the token it was queued with has expired", nil
		}
		if t.Scope != "full" {
			return "the token it was queued with no longer has full scope", nil
		}
	}
	return "", nil
}

// dequeueWithReason takes mr off the queue and says why on its timeline,
// as the queuer, whose request it was.
func dequeueWithReason(st *store.Store, mr store.MR, reason string) queueResult {
	st.DequeueMerge(mr.ID)
	st.AddMRSystemComment(mr.ID, mr.QueuedByID, "dequeued the merge queued by "+mr.QueuedBy+": "+reason)
	return queueResult{dequeued: true, reason: reason}
}

// queueMerge is mr merge --when-ready: queue the merge as c.User and try
// it at once, so gates that already pass merge now.
func queueMerge(c *Ctx, repo store.Repo, mr store.MR, strategy string) int {
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if mr.State != "open" {
		return c.fail(protocol.ExitUsage, "MR !%d is %s", mr.Number, mr.State)
	}
	// The merge happens later on this credential's authority, which must
	// not outlive it (#257).
	if c.Expires != nil {
		return c.fail(protocol.ExitDenied,
			"--when-ready merges later on the authority of the credential it is queued with, and this one expires; queue it with a key or token without an expiry, or from the web")
	}
	// A strategy the repository refuses outright would wait forever.
	if repo.Settings.RequireSignedCommits && (strategy == "merge" || strategy == "squash") {
		return c.fail(protocol.ExitDenied,
			"%s requires signed commits, so only fast-forward merges are allowed; queue without --strategy or with --strategy ff", repo.Path())
	}
	keyID, code := queueKeyID(c)
	if code >= 0 {
		return code
	}
	if err := c.Store.QueueMerge(mr.ID, c.User.ID, strategy, keyID, c.TokenID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	mergeQueueMu.Lock()
	res := attemptLocked(c.Store, c.Cfg, mr.ID)
	mergeQueueMu.Unlock()
	switch {
	case res.merged:
		return c.emit(res.out, func(w io.Writer) {
			fmt.Fprintf(w, "merged %s!%d into %s (%v) at %.10v\n", repo.Path(), mr.Number, mr.TargetRef, res.out["strategy"], res.out["sha"])
		})
	case res.dequeued:
		return c.fail(protocol.ExitDenied, "%s", res.reason)
	}
	note := ""
	if strategy != "" {
		note = " (" + strategy + ")"
	}
	c.Store.AddMRSystemComment(mr.ID, c.User.ID, fmt.Sprintf("%s queued the merge%s for when the gates pass", c.User.Username, note))
	return c.emit(map[string]any{"number": mr.Number, "queued": true, "strategy": strategy, "reason": res.reason}, func(w io.Writer) {
		fmt.Fprintf(w, "queued %s!%d to merge when ready; waiting: %s\n", repo.Path(), mr.Number, res.reason)
	})
}

// queueKeyID is the SSH key behind c, 0 for a token, a web session, or
// a context with no credential. -1 as the code means go on.
func queueKeyID(c *Ctx) (int64, int) {
	if c.TokenID != 0 || c.Source == "" || c.Source == SourceWeb || c.Source == "api" {
		return 0, -1
	}
	k, err := c.Store.SSHKeyByFingerprint(c.Source)
	if err != nil {
		return 0, c.fail(protocol.ExitFailure, "looking up the key behind this session: %v", err)
	}
	return k.ID, -1
}

// cancelQueuedMerge is mr merge --cancel.
func cancelQueuedMerge(c *Ctx, repo store.Repo, mr store.MR) int {
	ok, err := c.Store.DequeueMerge(mr.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if !ok {
		return c.fail(protocol.ExitFailure, "!%d is not queued to merge", mr.Number)
	}
	c.Store.AddMRSystemComment(mr.ID, c.User.ID, c.User.Username+" cancelled the queued merge")
	return c.emit(map[string]any{"number": mr.Number, "queued": false}, func(w io.Writer) {
		fmt.Fprintf(w, "cancelled the queued merge of %s!%d\n", repo.Path(), mr.Number)
	})
}
