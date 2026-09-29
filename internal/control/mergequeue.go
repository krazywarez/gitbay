package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"

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
// thread, a draft marked ready, a push to the source branch.
func TryQueuedMerge(st *store.Store, cfg config.Config, mrID int64) {
	attemptQueuedMerge(st, cfg, mrID)
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
		attemptQueuedMerge(st, cfg, id)
	}
}

// attemptQueuedMerge merges a queued request as the user who queued it,
// checked against that user's rights now. A merge refused for anything
// the queuer can fix (unmet gates, a branch behind a require-signed
// target, a conflict) stays queued with the refusal recorded; a queuer
// who can no longer merge is dequeued.
func attemptQueuedMerge(st *store.Store, cfg config.Config, mrID int64) queueResult {
	mergeQueueMu.Lock()
	defer mergeQueueMu.Unlock()
	mr, err := st.MRByID(mrID)
	if err != nil || mr.QueuedAt == "" {
		return queueResult{}
	}
	repo, err := st.RepoByID(mr.RepoID)
	if err != nil {
		return queueResult{}
	}
	user, err := st.UserByID(mr.QueuedByID)
	if err != nil {
		return queueResult{}
	}
	grant, err := st.AccessRole(repo.ID, user.ID)
	if err != nil {
		return queueResult{}
	}
	switch {
	case user.Disabled || user.Pending:
		return dequeueWithReason(st, repo, mr, user, user.Username+"'s account is not active")
	case !policy.CanWrite(user, repo, grant):
		return dequeueWithReason(st, repo, mr, user,
			fmt.Sprintf("%s no longer has write access to %s", user.Username, repo.Path()))
	}

	var out bytes.Buffer
	c := &Ctx{User: user, Scope: "full", Source: "queue", Store: st, Cfg: cfg,
		Stdin: emptyReader{}, Stdout: &out, Stderr: io.Discard, JSON: true}
	code := mergeMR(c, repo, mr, mr.QueueStrategy)
	var env struct {
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	json.Unmarshal(out.Bytes(), &env)
	if code == protocol.ExitOK {
		st.Audit(user.ID, "cmd mr merge", map[string]any{
			"argv": []string{repo.Path(), strconv.FormatInt(mr.Number, 10), "--when-ready"}, "source": "queue"})
		return queueResult{merged: true, out: env.Data}
	}
	st.SetMergeQueueReason(mr.ID, env.Error)
	return queueResult{reason: env.Error}
}

func dequeueWithReason(st *store.Store, repo store.Repo, mr store.MR, user store.User, reason string) queueResult {
	st.DequeueMerge(mr.ID)
	st.AddMRSystemComment(mr.ID, user.ID, "queued merge by "+user.Username+" cancelled: "+reason)
	return queueResult{dequeued: true, reason: reason}
}

// queueMerge is mr merge --when-ready: queue the merge as c.User and try
// it at once, so gates that already pass merge now.
func queueMerge(c *Ctx, repo store.Repo, mr store.MR, strategy string) int {
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if mr.State != "open" && mr.State != "source_gone" {
		return c.fail(protocol.ExitUsage, "MR !%d is %s", mr.Number, mr.State)
	}
	// A strategy the repository refuses outright would wait forever.
	if repo.Settings.RequireSignedCommits && (strategy == "merge" || strategy == "squash") {
		return c.fail(protocol.ExitDenied,
			"%s requires signed commits, so only fast-forward merges are allowed; queue without --strategy or with --strategy ff", repo.Path())
	}
	if err := c.Store.QueueMerge(mr.ID, c.User.ID, strategy); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	res := attemptQueuedMerge(c.Store, c.Cfg, mr.ID)
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
