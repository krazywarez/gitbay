package control

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// RefsUpdated is the work that follows a ref update in repoID by userID,
// with a key or token of scope: post-receive runs it for every push, and
// a server-side write to a branch (an applied suggestion) runs it after
// its own ref update, so nothing a push triggers is skipped. A push to a
// source branch refreshes refs/merge-requests/N/head in every target
// repo, by fetching — the target owns the objects, so the MR outlives the
// fork. This is the only place a push writes outside its own repository.
func RefsUpdated(st *store.Store, cfg config.Config, repoID, userID int64, scope string, updates []policy.RefUpdate) {
	pushedRepo, pushedRepoErr := st.RepoByID(repoID)
	if pushedRepoErr == nil {
		adoptDefaultBranch(st, cfg, &pushedRepo, updates)
	}
	for _, u := range updates {
		// Every ref update is an event webhooks can subscribe to.
		st.RecordEvent(repoID, userID, "push", fmt.Sprintf(
			`{"ref":%q,"old":%q,"new":%q,"forced":%v,"deleted":%v}`,
			u.Ref, u.Old, u.New, u.IsForce, u.IsDelete))

		// Any ref update — branch or tag — schedules the push mirrors.
		st.MarkMirrorsDirty(repoID, "push")

		// Tag pushes run the tag-triggered CI jobs.
		if tag, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok && !u.IsDelete && pushedRepoErr == nil {
			QueueTagBuilds(st, cfg, pushedRepo, userID, tag, u.New)
		}

		branch, ok := cutHeads(u.Ref)
		if !ok {
			continue
		}
		// Commits landing on the default branch act on issue references
		// in their messages (closes #N, plain #N).
		if pushedRepoErr == nil && branch == pushedRepo.DefaultBranch && !u.IsDelete {
			dir := RepoDir(cfg.Server.Root, pushedRepo.OwnerName, pushedRepo.Name)
			ProcessCommitMessages(st, dir, pushedRepo, userID, scope, u.Old, u.New)
			RecordLandedCommits(st, dir, pushedRepo, u.Old, u.New)
		}
		// A branch push with a .gitbay/ci.yml queues one build per job.
		if pushedRepoErr == nil && !u.IsDelete {
			QueueBranchBuilds(st, cfg.Server.Root, cfg.Server.SiteURL,
				pushedRepo, userID, branch, u.Old, u.New, time.Now())
		}
		if u.IsForce {
			st.Audit(userID, "push.forced", map[string]any{
				"repo": repoID, "ref": u.Ref, "old": u.Old, "new": u.New})
		}
		mrs, err := st.OpenMRsBySource(repoID, branch)
		if err != nil {
			slog.Error("post-receive: listing MRs", "err", err)
			continue
		}
		srcRepo, err := st.RepoByID(repoID)
		if err != nil {
			continue
		}
		srcDir := RepoDir(cfg.Server.Root, srcRepo.OwnerName, srcRepo.Name)
		for _, mr := range mrs {
			target, err := st.RepoByID(mr.RepoID)
			if err != nil {
				continue
			}
			if u.IsDelete {
				if mr.State == "open" {
					st.SetMRState(mr.ID, "source_gone")
				}
				if mr.QueuedAt != "" {
					TryQueuedMerge(st, cfg, mr.ID) // dequeues: the source is gone
				}
				continue // head ref retained: the diff stays viewable
			}
			dstDir := RepoDir(cfg.Server.Root, target.OwnerName, target.Name)
			headRef := fmt.Sprintf("refs/merge-requests/%d/head", mr.Number)
			if err := gitutil.FetchInto(dstDir, srcDir, u.New, headRef); err != nil {
				slog.Error("post-receive: refreshing MR head", "mr", mr.Number, "err", err)
				continue
			}
			// The merge base as it stands now, so a later range-diff
			// compares each revision against the target it was written
			// on rather than against today's. Best-effort: a base that
			// cannot be worked out costs precision, not the record.
			base, err := gitutil.MergeBase(dstDir, "refs/heads/"+mr.TargetRef, headRef)
			if err != nil {
				base = ""
			}
			if err := st.UpdateMRHead(mr.ID, u.New, base, sameChange(dstDir, mr, base, u.New)); err != nil {
				slog.Error("post-receive: recording MR head", "mr", mr.Number, "err", err)
			}
			if srcRepo.ID != target.ID {
				QueueMRBuilds(st, cfg.Server.Root, cfg.Server.SiteURL,
					target, userID, mr.Number, u.New)
			}
			if mr.State == "source_gone" {
				st.SetMRState(mr.ID, "open") // branch came back
			}
			// A queued merge stays queued across a push by someone who can
			// merge it, and the new head has to pass the gates on its own.
			if mr.QueuedAt != "" {
				QueuedMergePushed(st, cfg, mr.ID, userID, scope)
			}
		}
	}
}

// adoptDefaultBranch moves an unborn HEAD to the first branch a push
// creates. A repository is initialised with HEAD at the stored default,
// and a first push of master or trunk left HEAD naming a branch that did
// not exist: clones checked out nothing and every surface asked git for
// a branch that was not there (#189). A push that includes the default
// branch itself needs nothing.
func adoptDefaultBranch(st *store.Store, cfg config.Config, repo *store.Repo, updates []policy.RefUpdate) {
	dir := RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch); err == nil {
		return
	}
	for _, u := range updates {
		branch, ok := cutHeads(u.Ref)
		if !ok || u.IsDelete || !gitutil.ZeroSHA(u.Old) {
			continue
		}
		if err := gitutil.SetHead(dir, branch); err != nil {
			slog.Error("post-receive: moving HEAD", "repo", repo.Path(), "err", err)
			return
		}
		if err := st.UpdateDefaultBranch(repo.ID, branch); err != nil {
			slog.Error("post-receive: recording default branch", "repo", repo.Path(), "err", err)
			return
		}
		repo.DefaultBranch = branch
		return
	}
}

// sameChange reports whether the new head proposes the diff the old one
// did: the patch-id of each revision against its own merge base. A
// rebase onto a moved target changes every sha and nothing about the
// change, and the reviews of it should not go stale for that (#198).
// Any doubt answers false, which is the old behaviour.
func sameChange(dir string, mr store.MR, newBase, newHead string) bool {
	if mr.HeadSHA == "" || newBase == "" || mr.HeadSHA == newHead {
		return false
	}
	oldBase, err := gitutil.MergeBase(dir, "refs/heads/"+mr.TargetRef, mr.HeadSHA)
	if err != nil {
		return false
	}
	oldID, err := gitutil.PatchID(dir, oldBase, mr.HeadSHA)
	if err != nil || oldID == "" {
		return false
	}
	newID, err := gitutil.PatchID(dir, newBase, newHead)
	return err == nil && newID == oldID
}

// QueueTagBuilds runs the jobs whose tag pattern matches a pushed tag.
// The build records the tag as its ref and the peeled commit as its sha,
// so statuses land on the commit, not an annotated tag object.
func QueueTagBuilds(st *store.Store, cfg config.Config, repo store.Repo, userID int64, tag, pushed string) {
	dir := RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name)
	sha, err := gitutil.PeelToCommit(dir, pushed)
	if err != nil {
		return
	}
	raw, err := gitutil.ReadBlob(dir, sha, ci.ConfigPath, 1<<16)
	if err != nil {
		return
	}
	jobs, err := ci.Parse(raw)
	if err != nil {
		return // the branch push already reported ci/config
	}
	for _, j := range jobs {
		if j.Tags == "" {
			continue
		}
		if ok, _ := path.Match(j.Tags, tag); !ok {
			continue
		}
		steps, _ := json.Marshal(j.Steps)
		n, err := st.CreateBuild(repo.ID, j.Name, sha, tag, string(steps), j.Image, "", true)
		if err != nil {
			slog.Error("queueing tag build", "repo", repo.Path(), "job", j.Name, "err", err)
			continue
		}
		url := fmt.Sprintf("%s/%s/builds/%d", cfg.Server.SiteURL, repo.Path(), n)
		st.SetCommitStatus(repo.ID, sha, "ci/"+j.Name, "pending", "tag "+tag, url, userID)
	}
}

func cutHeads(ref string) (string, bool) {
	const p = "refs/heads/"
	if len(ref) > len(p) && ref[:len(p)] == p {
		return ref[len(p):], true
	}
	return "", false
}
