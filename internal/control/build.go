package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"build", "list"},
		Summary: "list recent builds",
		Usage:   "build list <owner/name>", ReadOnly: true, Run: runBuildList})
	register(Command{Path: []string{"build", "show"},
		Summary: "show one build",
		Usage:   "build show <owner/name> <n>", ReadOnly: true, Run: runBuildShow})
	register(Command{Path: []string{"build", "log"},
		Summary: "print a build's log",
		Usage:   "build log <owner/name> <n>", ReadOnly: true, Run: runBuildLog})

	register(Command{Path: []string{"build", "jobs"},
		Summary: "list the jobs a trigger can name",
		Usage:   "build jobs <owner/name>", ReadOnly: true, Run: runBuildJobs})

	register(Command{Path: []string{"build", "cancel"},
		Summary: "withdraw a queued build before a runner claims it",
		Usage:   "build cancel <owner/name> <n>", Run: runBuildCancel})
	register(Command{Path: []string{"build", "trigger"},
		Summary: "queue a job now (scheduled or not)",
		Usage:   "build trigger <owner/name> <job>", Run: runBuildTrigger})
	// Secrets: set over stdin, listed by name only, injected into the
	// repo's builds as environment variables. Same discipline as mirror
	// tokens — the value never appears in argv, logs, or output.
	register(Command{Path: []string{"repo", "secret", "set"},
		Summary:    "set a build secret",
		Usage:      "repo secret set <owner/name> <NAME> (value on stdin)",
		ReadsStdin: true, SSHOnly: true, Run: runSecretSet})
	register(Command{Path: []string{"repo", "secret", "remove"},
		Summary: "remove a build secret",
		Usage:   "repo secret remove <owner/name> <NAME>", Run: runSecretRemove})
	register(Command{Path: []string{"repo", "secret", "list"},
		Summary: "list build secret names",
		Usage:   "repo secret list <owner/name>", ReadOnly: true, Run: runSecretList})

	// Runner commands: the claim/report loop for gitbay-runner. A runner
	// executes arbitrary repo code, so handing out jobs is the instance
	// operator's call: a key added with --scope runner, which the
	// dispatcher confines to these three commands and read-only git, or
	// an admin key, which a runner host should not hold (#92).
	register(Command{Path: []string{"runner", "next"},
		Summary: "claim the oldest pending build (runner protocol)",
		Usage:   "runner next [<owner/name>...]", SSHOnly: true, Run: runRunnerNext})
	register(Command{Path: []string{"runner", "log"},
		Summary: "append a build's log from stdin",
		Usage:   "runner log <build-id>", SSHOnly: true, ReadsStdin: true, Run: runRunnerLog})
	register(Command{Path: []string{"runner", "done"},
		Summary: "finish a build",
		Usage:   "runner done <build-id> success|failure", SSHOnly: true, Run: runRunnerDone})
}

type BuildOut struct {
	Number     int64  `json:"number"`
	Job        string `json:"job"`
	Status     string `json:"status"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

func buildToOut(b store.Build) BuildOut {
	return BuildOut{b.Number, b.Job, b.Status, b.SHA, b.Ref, b.CreatedAt, b.FinishedAt}
}

func buildRef(c *Ctx, args []string) (store.Repo, store.Build, int) {
	if len(args) != 2 {
		return store.Repo{}, store.Build{}, c.fail(protocol.ExitUsage, "expected <owner/name> <number>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return repo, store.Build{}, code
	}
	n, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return repo, store.Build{}, c.fail(protocol.ExitUsage, "bad build number %q", args[1])
	}
	b, err := c.Store.BuildByNumber(repo.ID, n)
	if err != nil {
		return repo, b, c.fail(protocol.ExitNotFound, "no build %d on %s", n, repo.Path())
	}
	return repo, b, -1
}

func runBuildList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: build list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	builds, err := c.Store.ListBuilds(repo.ID, 50)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var ds []BuildOut
	for _, b := range builds {
		ds = append(ds, buildToOut(b))
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%d\t%s\t%s\t%.10s\t%s\n", d.Number, d.Job, d.Status, d.SHA, d.Ref)
		}
	})
}

func runBuildShow(c *Ctx, args []string) int {
	_, b, code := buildRef(c, args)
	if code >= 0 {
		return code
	}
	d := buildToOut(b)
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "build %d\t%s\t%s\n%.10s on %s\nqueued %s", d.Number, d.Job, d.Status, d.SHA, d.Ref, d.CreatedAt)
		if d.FinishedAt != "" {
			fmt.Fprintf(w, ", finished %s", d.FinishedAt)
		}
		fmt.Fprintln(w)
	})
}

func runBuildLog(c *Ctx, args []string) int {
	_, b, code := buildRef(c, args)
	if code >= 0 {
		return code
	}
	log, err := c.Store.BuildLog(b.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Stdout.Write(log)
	return protocol.ExitOK
}

type JobOut struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule,omitempty"`
	Tags     string `json:"tags,omitempty"`
}

// repoJobs reads the CI config on the default branch — the same file the
// scheduler reads — and returns its jobs with the sha they came from.
func repoJobs(c *Ctx, repo store.Repo) ([]ci.Job, string, int) {
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	sha, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return nil, "", c.fail(protocol.ExitFailure, "resolving %s: %v", repo.DefaultBranch, err)
	}
	raw, err := gitutil.ReadBlob(dir, sha, ci.ConfigPath, 1<<16)
	if err != nil {
		return nil, "", c.fail(protocol.ExitNotFound, "%s has no %s on %s", repo.Path(), ci.ConfigPath, repo.DefaultBranch)
	}
	jobs, err := ci.Parse(raw)
	if err != nil {
		return nil, "", c.failErr(err)
	}
	return jobs, sha, -1
}

// runBuildJobs answers "what can I trigger?". Without it only a surface
// that can read the repository's git could offer the choice.
func runBuildJobs(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: build jobs <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	jobs, _, code := repoJobs(c, repo)
	if code >= 0 {
		return code
	}
	out := make([]JobOut, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, JobOut{Name: j.Name, Schedule: j.Schedule, Tags: j.Tags})
	}
	return c.emit(out, func(w io.Writer) {
		for _, j := range out {
			switch {
			case j.Schedule != "":
				fmt.Fprintf(w, "%s\tschedule %s\n", j.Name, j.Schedule)
			case j.Tags != "":
				fmt.Fprintf(w, "%s\ttags %s\n", j.Name, j.Tags)
			default:
				fmt.Fprintf(w, "%s\ton push\n", j.Name)
			}
		}
	})
}

func runBuildTrigger(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: build trigger <owner/name> <job>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanWrite)
	if code >= 0 {
		return code
	}
	jobs, sha, code := repoJobs(c, repo)
	if code >= 0 {
		return code
	}
	for _, j := range jobs {
		if j.Name != args[1] {
			continue
		}
		steps, _ := json.Marshal(j.Steps)
		n, err := c.Store.CreateBuild(repo.ID, j.Name, sha, repo.DefaultBranch, string(steps), true)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), n)
		c.Store.SetCommitStatus(repo.ID, sha, "ci/"+j.Name, "pending", "triggered", url, c.User.ID)
		return c.emit(map[string]any{"build": n, "job": j.Name, "sha": sha}, func(w io.Writer) {
			fmt.Fprintf(w, "queued build %d (%s @ %.10s)\n", n, j.Name, sha)
		})
	}
	return c.fail(protocol.ExitNotFound, "no job %q in %s", args[1], ci.ConfigPath)
}

// secretName is env-var shaped: the value lands in the build environment.
var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

func runSecretSet(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo secret set <owner/name> <NAME> (value on stdin)")
	}
	if !secretName.MatchString(args[1]) {
		return c.fail(protocol.ExitUsage, "secret names are env-var shaped: uppercase letters, digits, _")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading secret: %v", err)
	}
	value := strings.TrimRight(string(raw), "\n")
	if value == "" {
		return c.fail(protocol.ExitUsage, "no value on stdin (pipe it: printf %%s TOKEN | ...)")
	}
	if err := c.Store.SetBuildSecret(repo.ID, args[1], value); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"secret": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "secret %s set on %s\n", args[1], repo.Path())
	})
}

func runSecretRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo secret remove <owner/name> <NAME>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.RemoveBuildSecret(repo.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no secret %s on %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", args[1])
	})
}

func runSecretList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo secret list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	names, err := c.Store.ListBuildSecretNames(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(names, func(w io.Writer) {
		for _, n := range names {
			fmt.Fprintln(w, n)
		}
	})
}

func requireRunner(c *Ctx) int {
	if c.Scope != "runner" && !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "runner commands need a key added with --scope runner")
	}
	return -1
}

func runRunnerNext(c *Ctx, args []string) int {
	if code := requireRunner(c); code >= 0 {
		return code
	}
	// A runner may limit itself to named repositories. The operator chooses
	// what a given runner executes by how they start it; this is scoping the
	// runner asks for, not an ACL the server holds over it.
	var repoIDs []int64
	for _, arg := range args {
		repo, code := resolveRepo(c, arg, policy.CanRead)
		if code >= 0 {
			return code
		}
		repoIDs = append(repoIDs, repo.ID)
	}
	b, ok, err := c.Store.ClaimBuild(repoIDs)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// The poll itself is the runner's heartbeat: admin runners reads it.
	c.Store.TouchRunner(c.User.ID, strings.Join(args, ","), b.ID)
	if !ok {
		return c.emit(map[string]any{}, func(w io.Writer) { fmt.Fprintln(w, "no pending builds") })
	}
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var steps []string
	json.Unmarshal([]byte(b.Steps), &steps)
	// Secrets ride the claim: this channel is admin-only and the values
	// land in the build's environment, nowhere else.
	var secrets map[string]string
	if b.Trusted {
		secrets, err = c.Store.BuildSecrets(b.RepoID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	d := struct {
		ID      int64             `json:"id"`
		Repo    string            `json:"repo"`
		Number  int64             `json:"number"`
		Job     string            `json:"job"`
		SHA     string            `json:"sha"`
		Ref     string            `json:"ref"`
		Steps   []string          `json:"steps"`
		Secrets map[string]string `json:"secrets,omitempty"`
	}{b.ID, repo.Path(), b.Number, b.Job, b.SHA, b.Ref, steps, secrets}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "build %d: %s %s @ %.10s\n", d.ID, d.Repo, d.Job, d.SHA)
	})
}

func runRunnerLog(c *Ctx, args []string) int {
	if code := requireRunner(c); code >= 0 {
		return code
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: runner log <build-id> (chunk on stdin)")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad build id %q", args[0])
	}
	// Stream stdin into the log in chunks so long builds appear live. An
	// append that fails drops its chunk and the loop keeps draining: ending
	// the session here breaks the runner's pipe, and a broken pipe is how a
	// transient SQLITE_BUSY used to fail the build the log belonged to.
	//
	// The session is also how a running build is cancelled: while it is
	// open the build's row is watched, and when the row stops saying
	// running the session ends with ExitNotFound, which the runner reads as
	// "stop this build". Any other end of the session is a lost stream.
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 4)
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, rerr := c.Stdin.Read(buf)
			if n > 0 {
				chunks <- chunk{data: append([]byte(nil), buf[:n]...)}
			}
			if rerr != nil {
				chunks <- chunk{err: rerr}
				return
			}
		}
	}()
	watch := time.NewTicker(2 * time.Second)
	defer watch.Stop()
	dropped := 0
	for {
		select {
		case ch := <-chunks:
			if len(ch.data) > 0 {
				if err := c.Store.AppendBuildLog(id, ch.data); err != nil {
					dropped++
					slog.Warn("appending build log", "build", id, "err", err)
				}
			}
			if ch.err != nil {
				if dropped > 0 {
					slog.Warn("build log incomplete", "build", id, "dropped_chunks", dropped)
				}
				return c.emit(map[string]string{"log": "ok"}, func(w io.Writer) {})
			}
		case <-watch.C:
			if b, err := c.Store.BuildByID(id); err == nil && b.Status != "running" {
				return c.fail(protocol.ExitNotFound, "build %d is %s; stop", id, b.Status)
			}
		}
	}
}

func runRunnerDone(c *Ctx, args []string) int {
	if code := requireRunner(c); code >= 0 {
		return code
	}
	if len(args) != 2 || (args[1] != "success" && args[1] != "failure") {
		return c.fail(protocol.ExitUsage, "usage: runner done <build-id> success|failure")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad build id %q", args[0])
	}
	b, err := c.Store.BuildByID(id)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no build %d", id)
	}
	// Cancelled underneath the runner: its report is late, not wrong.
	// The row, the status and the log were settled by the cancel.
	if b.Status == "cancelled" {
		c.Store.RunnerDone(c.User.ID)
		return c.emit(map[string]any{"build": b.Number, "status": "cancelled"}, func(w io.Writer) {
			fmt.Fprintf(w, "build %d was cancelled\n", b.Number)
		})
	}
	if err := c.Store.FinishBuild(id, args[1]); err != nil {
		return c.fail(protocol.ExitFailure, "finishing build %d: %v", id, err)
	}
	c.Store.RunnerDone(c.User.ID)
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), b.Number)
	desc := "build " + args[1]
	if err := c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, args[1], desc, url, c.User.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "build."+args[1],
		fmt.Sprintf(`{"number":%d,"job":%q}`, b.Number, b.Job))
	// A red build mails the repo's notify targets with the log tail — a
	// failed scheduled job must not wait to be noticed.
	if args[1] == "failure" {
		if targets, err := c.Store.RepoNotifyTargets(repo); err == nil {
			tail := ""
			if log, err := c.Store.BuildLog(id); err == nil && len(log) > 0 {
				if len(log) > 2000 {
					log = log[len(log)-2000:]
				}
				tail = string(log)
			}
			notify(c, targets, notice{repo: repo, kind: "build",
				subject: fmt.Sprintf("[%s] build %d failed: %s on %s", repo.Path(), b.Number, b.Job, b.Ref),
				action:  fmt.Sprintf("build %d failed: %s on %s", b.Number, b.Job, b.Ref),
				body:    fmt.Sprintf("job %s failed at %.10s.\n\n…%s\n\n%s\n", b.Job, b.SHA, tail, url),
				path:    fmt.Sprintf("%s/builds/%d", repo.Path(), b.Number)})
		}
	}
	return c.emit(map[string]any{"build": b.Number, "status": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "build %d %s\n", b.Number, args[1])
	})
}

// QueueBranchBuilds reads .gitbay/ci.yml at sha and creates one pending
// build per push job, with a pending commit status the runner resolves.
// A broken config surfaces as a failed "ci/config" status, not silence.
//
// Both paths that move a branch call this: post-receive for a push, and
// the merge path for a merge, which updates the ref directly and so never
// reaches a hook.
func QueueBranchBuilds(
	st *store.Store, root, siteURL string,
	repo store.Repo, userID int64, branch, sha string, now time.Time,
) {
	queueJobs(st, root, siteURL, repo, userID, branch, sha, now, true, branch == repo.DefaultBranch)
}

// QueueMRBuilds queues the push jobs for a merge request head fetched
// from another repository, which the target holds at
// refs/merge-requests/<n>/head, so a fork's merge request has ci/<job>
// statuses for require-checks to gate on (#98). The head is untrusted:
// its build runs without the target's secrets. A same-repository head is
// the branch push's job and is not queued here; a failed one is rebuilt
// when it lands, not when it is proposed.
func QueueMRBuilds(
	st *store.Store, root, siteURL string,
	repo store.Repo, userID, n int64, sha string,
) {
	queueJobs(st, root, siteURL, repo, userID, mrHeadRef(n), sha, time.Now(), false, false)
}

func queueJobs(
	st *store.Store, root, siteURL string,
	repo store.Repo, userID int64, ref, sha string, now time.Time,
	trusted, syncSchedules bool,
) {
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	raw, err := gitutil.ReadBlob(dir, sha, ci.ConfigPath, 1<<16)
	if err != nil {
		return // no CI config at this commit
	}
	jobs, err := ci.Parse(raw)
	if err != nil {
		st.SetCommitStatus(repo.ID, sha, "ci/config", "failure", err.Error(), "", userID)
		return
	}
	// A build is a fact about a commit, not a ref: a job has no branch
	// filter, so a commit that already passed a job on another branch has
	// nothing left to prove when a fast-forward lands it here, and one
	// still queued or running there will say soon enough. A failed,
	// abandoned or cancelled build does not count; that commit runs again.
	built, err := st.BuildsForCommit(repo.ID, sha)
	if err != nil {
		built = nil
	}
	var schedules []store.Schedule
	for _, j := range jobs {
		// Tag jobs run on matching tag pushes only.
		if j.Tags != "" {
			continue
		}
		if b, ok := built[j.Name]; ok && (b.Status == "success" || b.Status == "pending" || b.Status == "running") {
			continue
		}
		// Scheduled jobs run on their cron, not on push; a default-branch
		// push (re)registers them.
		if j.Schedule != "" {
			if syncSchedules {
				schedules = append(schedules, store.Schedule{
					RepoID: repo.ID, Job: j.Name, Cron: j.Schedule,
					NextRun: ci.NextRun(j.Schedule, now),
				})
			}
			continue
		}
		steps, _ := json.Marshal(j.Steps)
		n, err := st.CreateBuild(repo.ID, j.Name, sha, ref, string(steps), trusted)
		if err != nil {
			slog.Error("queueing build", "repo", repo.Path(), "job", j.Name, "err", err)
			continue
		}
		url := fmt.Sprintf("%s/%s/builds/%d", siteURL, repo.Path(), n)
		st.SetCommitStatus(repo.ID, sha, "ci/"+j.Name, "pending", "queued", url, userID)
	}
	if syncSchedules {
		if err := st.SyncSchedules(repo.ID, schedules); err != nil {
			slog.Error("syncing schedules", "repo", repo.Path(), "err", err)
		}
	}
}

func runBuildCancel(c *Ctx, args []string) int {
	repo, b, code := buildRef(c, args)
	if code >= 0 {
		return code
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "cancelling a build needs write access to %s", repo.Path())
	}
	if b.Status != "pending" && b.Status != "running" {
		return c.fail(protocol.ExitUsage, "build %d is %s; only a queued or running build can be cancelled", b.Number, b.Status)
	}
	if err := c.Store.CancelBuild(b.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if b.Status == "running" {
		c.Store.AppendBuildLog(b.ID, []byte(fmt.Sprintf("\ncancelled by %s while running; the runner stops at its next check\n", c.User.Username)))
	} else {
		c.Store.AppendBuildLog(b.ID, []byte(fmt.Sprintf("cancelled by %s before a runner claimed it\n", c.User.Username)))
	}
	// The queued status replaced whatever the commit had for this job. If
	// the commit passed the job on another ref, that result stands again;
	// otherwise the context says it was withdrawn.
	if prev, ok, err := c.Store.SuccessBuildFor(repo.ID, b.SHA, b.Job); err == nil && ok {
		url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), prev.Number)
		c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, "success",
			fmt.Sprintf("passed in build %d on %s", prev.Number, prev.Ref), url, c.User.ID)
	} else {
		url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), b.Number)
		c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, "error", "cancelled", url, c.User.ID)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "build.cancelled", fmt.Sprintf(`{"number":%d,"job":%q}`, b.Number, b.Job))
	return c.emit(map[string]any{"number": b.Number, "job": b.Job, "status": "cancelled", "was": b.Status}, func(w io.Writer) {
		if b.Status == "running" {
			fmt.Fprintf(w, "cancelled %s build %d (%s); the runner stops at its next check\n", repo.Path(), b.Number, b.Job)
			return
		}
		fmt.Fprintf(w, "cancelled %s build %d (%s)\n", repo.Path(), b.Number, b.Job)
	})
}
