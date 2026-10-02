package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"slices"
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
		Usage:   "build list <owner/name> [--ref <branch>] [--status <state>] [--job <name>] [--limit <n>] [--cursor <c>]",
		Flags: []Flag{
			{"--ref", "<branch>", "only builds on this branch", ""},
			{"--status", "<state>", "only builds in this state", ""},
			{"--job", "<name>", "only this job", ""},
			{"--limit", "<n>", "rows per page", "50"},
			{"--cursor", "<c>", "continue from the previous page", ""},
		},
		Examples: []string{"build list krz/gitbay --status failure"},
		ReadOnly: true, Run: runBuildList})
	register(Command{Path: []string{"build", "show"},
		Summary:  "show one build",
		Usage:    "build show <owner/name> <n>",
		Examples: []string{"build show krz/gitbay 431"},
		ReadOnly: true, Run: runBuildShow})
	register(Command{Path: []string{"build", "log"},
		Summary: "print a build's log, or follow it until the build ends",
		Usage:   "build log <owner/name> <n> [--follow] [--step <step>|failed] [--tail <lines>]",
		Flags: []Flag{
			{"--follow", "", "stream the log until the build ends", ""},
			{"--step", "<step>|failed", "only one step's output: 0 for the setup, a step number, or the one that failed", ""},
			{"--tail", "<lines>", "only the last lines", ""},
		},
		Examples: []string{"build log krz/gitbay 431 --follow", "build log krz/gitbay 431 --step failed --tail 40"},
		ReadOnly: true, Run: runBuildLog})

	register(Command{Path: []string{"build", "jobs"},
		Summary:  "list the jobs a trigger can name",
		Usage:    "build jobs <owner/name>",
		Examples: []string{"build jobs krz/gitbay"},
		ReadOnly: true, Run: runBuildJobs})

	register(Command{Path: []string{"build", "cancel"},
		Summary:  "withdraw a queued build before a runner claims it",
		Usage:    "build cancel <owner/name> <n>",
		Examples: []string{"build cancel krz/gitbay 431"},
		Run:      runBuildCancel})
	register(Command{Path: []string{"build", "trigger"},
		Summary:  "queue a job now (scheduled or not)",
		Usage:    "build trigger <owner/name> <job>",
		Examples: []string{"build trigger krz/gitbay vuln"},
		Run:      runBuildTrigger})
	// Secrets: set over stdin, listed by name only, injected into the
	// repo's builds as environment variables. Same discipline as mirror
	// tokens — the value never appears in argv, logs, or output.
	register(Command{Path: []string{"repo", "secret", "set"},
		NeedsRecentSignIn: true,
		Summary:           "set a build secret",
		Usage:             "repo secret set <owner/name> <NAME> (value on stdin)",
		Examples:          []string{"repo secret set krz/gitbay DEPLOY_TOKEN"},
		ReadsStdin:        true, Run: runSecretSet})
	register(Command{Path: []string{"repo", "secret", "remove"},
		Summary:  "remove a build secret",
		Usage:    "repo secret remove <owner/name> <NAME>",
		Examples: []string{"repo secret remove krz/gitbay DEPLOY_TOKEN"},
		Run:      runSecretRemove})
	register(Command{Path: []string{"repo", "secret", "list"},
		Summary:  "list build secret names",
		Usage:    "repo secret list <owner/name>",
		Examples: []string{"repo secret list krz/gitbay"},
		ReadOnly: true, Run: runSecretList})

	// Runner commands: the claim/report loop for gitbay-runner. A runner
	// executes arbitrary repo code, so handing out jobs is the instance
	// operator's call: a key added with --scope runner, which the
	// dispatcher confines to these three commands and read-only git, or
	// an admin key, which a runner host should not hold (#92).
	register(Command{Path: []string{"runner", "next"},
		Summary: "claim the oldest pending build this key may run (runner protocol)",
		Usage:   "runner next [--untrusted] [<owner/name>...]",
		Flags: []Flag{
			{"--untrusted", "", "this runner may build a fork's merge request head", ""},
		},
		Examples: []string{"runner next krz/gitbay"},
		Run:      runRunnerNext})
	register(Command{Path: []string{"runner", "log"},
		Summary:    "append a build's log from stdin",
		Usage:      "runner log <build-id>",
		Examples:   []string{"runner log 431"},
		ReadsStdin: true, Run: runRunnerLog})
	register(Command{Path: []string{"runner", "done"},
		Summary: "finish a build",
		Usage:   "runner done <build-id> success|failure [--step <n>] [--reason <text>]",
		Flags: []Flag{
			{"--step", "<n>", "the 1-based step a failed build stopped at", ""},
			{"--reason", "<text>", "how it failed, one line", ""},
		},
		Examples: []string{"runner done 431 success", "runner done 431 failure --step 3 --reason 'exit 1'"},
		Run:      runRunnerDone})
}

type BuildOut struct {
	Number     int64  `json:"number"`
	Job        string `json:"job"`
	Status     string `json:"status"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	// Subject is the first line of the commit's message, so a build
	// names what it ran on rather than only its sha (#241). It is empty
	// when the commit is no longer in the repository.
	Subject string `json:"subject,omitempty"`
	// FailedStep is the 1-based step a failed build stopped at, 0 when
	// none; FailedReason says how ("exit 1") (#266).
	FailedStep   int    `json:"failed_step,omitempty"`
	FailedReason string `json:"failed_reason,omitempty"`
	// DurationS is how long the build ran, once it has a start and a
	// finish.
	DurationS int64 `json:"duration_s,omitempty"`
	// Steps are the job's commands; build show only.
	Steps []string `json:"steps,omitempty"`
}

func buildToOut(b store.Build) BuildOut {
	return BuildOut{Number: b.Number, Job: b.Job, Status: b.Status, SHA: b.SHA,
		Ref: b.Ref, CreatedAt: b.CreatedAt, FinishedAt: b.FinishedAt,
		FailedStep: b.FailedStep, FailedReason: b.FailedReason,
		DurationS: int64(b.Elapsed() / time.Second)}
}

func buildRef(c *Ctx, args []string) (store.Repo, store.Build, int) {
	if len(args) != 2 {
		return store.Repo{}, store.Build{}, c.usageWith("expected <owner/name> <number>")
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

// buildStatuses is the vocabulary --status accepts, and what a bad value
// is told to pick from.
var buildStatuses = []string{"pending", "running", "success", "failure", "cancelled"}

// buildPage is how many builds one page of build list returns when no
// --limit is given. The cap has always been there; what it is now
// reachable past, with --cursor (#244).
const buildPage = 50

func runBuildList(c *Ctx, args []string) int {
	args, p, code := parsePageFlags(c, args, "build", true)
	if code >= 0 {
		return code
	}
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--ref", "--status", "--job"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path := f.pos(0)
	if path == "" {
		return c.usage()
	}
	status := f.Value("--status")
	if f.Has("--status") && !slices.Contains(buildStatuses, status) {
		return c.fail(protocol.ExitUsage, "--status must be one of %s", strings.Join(buildStatuses, ", "))
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	limit := p.queryLimit()
	if limit == 0 {
		limit = buildPage
	}
	filter := store.BuildFilter{Ref: f.Value("--ref"), Status: status, Job: f.Value("--job"), Before: p.keyInt()}
	builds, err := c.Store.ListBuilds(repo.ID, filter, limit)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	builds, next := trimPage(p, builds, "build", func(b store.Build) string {
		return strconv.FormatInt(b.Number, 10)
	})
	var ds []BuildOut
	for _, b := range builds {
		ds = append(ds, buildToOut(b))
	}
	subjects := buildSubjects(c, repo, ds)
	for i := range ds {
		ds[i].Subject = subjects[ds[i].SHA]
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		tb := c.table(w, "#", "JOB", "STATUS", "SHA", "REF", "TITLE")
		for _, d := range ds {
			tb.row(cLink(fmt.Sprintf("%d", d.Number), c.siteURL(repo.Path(), "builds", strconv.FormatInt(d.Number, 10))), cText(d.Job), cState(d.Status), cRef(fmt.Sprintf("%.10s", d.SHA)), cText(d.Ref), cFlex(d.Subject))
		}
		tb.flush()
	})
}

// buildSubjects reads the commit subject of each distinct sha on a page
// of builds. Several jobs of one push share a commit, so the set is
// usually far smaller than the page.
func buildSubjects(c *Ctx, repo store.Repo, ds []BuildOut) map[string]string {
	seen := map[string]bool{}
	var shas []string
	for _, d := range ds {
		if d.SHA != "" && !seen[d.SHA] {
			seen[d.SHA] = true
			shas = append(shas, d.SHA)
		}
	}
	return gitutil.Subjects(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name), shas)
}

func runBuildShow(c *Ctx, args []string) int {
	repo, b, code := buildRef(c, args)
	if code >= 0 {
		return code
	}
	d := buildToOut(b)
	json.Unmarshal([]byte(b.Steps), &d.Steps)
	var mr *store.MR
	if c.Term.Cols > 0 && !c.JSON {
		if m, ok, err := c.Store.OpenMRBySource(repo.ID, d.Ref); err == nil && ok {
			mr = &m
		}
	}
	return c.emitView(d, func(w io.Writer) {
		failedStep, failed := "", ""
		if d.FailedStep > 0 && d.FailedStep <= len(d.Steps) {
			step, _, _ := strings.Cut(d.Steps[d.FailedStep-1], "\n")
			failedStep = fmt.Sprintf("%d/%d %s", d.FailedStep, len(d.Steps), step)
			if d.FailedReason != "" {
				failedStep += " (" + d.FailedReason + ")"
			}
		} else {
			failed = d.FailedReason
		}
		duration := ""
		if d.DurationS > 0 {
			duration = (time.Duration(d.DurationS) * time.Second).String()
		}
		v := c.view(w)
		v.title(fmt.Sprintf("#%d", d.Number), d.Job, d.Status)
		v.fields(
			"sha", fmt.Sprintf("%.10s", d.SHA),
			"ref", d.Ref,
			"queued", c.when(d.CreatedAt),
			"finished", c.when(d.FinishedAt),
			"duration", duration,
			"failed step", failedStep,
			"failed", failed,
			"url", c.siteURL(repo.Path(), "builds", strconv.FormatInt(d.Number, 10)),
		)
	}, func() screen { return buildShowScreen(c, repo, d, mr) })
}

// buildShowScreen is build show at a terminal: the build's outcome, what
// it ran on, each step's outcome, and the command for its log.
func buildShowScreen(c *Ctx, repo store.Repo, d BuildOut, mr *store.MR) screen {
	n := strconv.FormatInt(d.Number, 10)
	path := repo.Path()
	var s screen
	s.fields = append(s.fields,
		field{"Build", []cell{cLink(n, c.siteURL(path, "builds", n)), cText(d.Job)}},
		field{"State", []cell{cGlyph(d.Status), cState(d.Status)}},
		field{"Commit", []cell{cRef(fmt.Sprintf("%.10s", d.SHA)), cText(d.Subject)}},
		field{"Ref", []cell{cText(d.Ref)}},
	)
	if mr != nil {
		s.fields = append(s.fields, field{"MR", []cell{cRef(fmt.Sprintf("!%d", mr.Number)), cText(mr.Title)}})
	}
	if d.DurationS > 0 {
		s.fields = append(s.fields, field{"Duration", []cell{cText(c.Term.dur(d.DurationS))}})
	}
	if !c.Term.Links {
		s.fields = append(s.fields, field{"URL", []cell{cText(c.siteURL(path, "builds", n))}})
	}
	steps := section{title: "Steps", n: len(d.Steps)}
	for i, step := range d.Steps {
		line, _, _ := strings.Cut(step, "\n")
		meta := ""
		if i+1 == d.FailedStep {
			meta = d.FailedReason
		}
		steps.rows = append(steps.rows, rowOf(cGlyph(stepState(d.Status, d.FailedStep, i+1)), cFlex(line), cMeta(meta)))
	}
	s.sections = []section{steps}
	s.actions = []action{{"Read", []string{"build", "log", path, n}}}
	return s
}

// stepState is what a finished build says about one of its steps: those
// before the failed step passed, the failed one failed, the rest never
// ran. A build still running, or one that failed outside its steps,
// says nothing per step.
func stepState(status string, failedStep, n int) string {
	switch {
	case status == "success":
		return "success"
	case status != "failure" || failedStep == 0:
		return ""
	case n < failedStep:
		return "success"
	case n == failedStep:
		return "failure"
	}
	return "skipped"
}

func runBuildLog(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Bools: []string{"--follow"}, Values: []string{"--step", "--tail"}, MaxPos: 2, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	repo, b, code := buildRef(c, f.Pos)
	if code >= 0 {
		return code
	}
	if f.Has("--follow") {
		if f.Has("--step") || f.Has("--tail") {
			return c.fail(protocol.ExitUsage, "--step and --tail read the stored log; drop --follow")
		}
		return followBuildLog(c, repo, b)
	}
	tail := 0
	if f.Has("--tail") {
		if tail, err = strconv.Atoi(f.Value("--tail")); err != nil || tail < 1 {
			return c.fail(protocol.ExitUsage, "--tail takes a number of lines, 1 or more")
		}
	}
	log, err := c.Store.BuildLog(b.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var steps []string
	json.Unmarshal([]byte(b.Steps), &steps)
	sections := SplitBuildLog(string(log), steps)
	failed := ""
	if at := FailedSection(sections, b.Status, b.FailedStep); at >= 0 {
		failed = sections[at].Step
	}
	if f.Has("--step") {
		at := -1
		if want := f.Value("--step"); want == "failed" {
			if at = FailedSection(sections, b.Status, b.FailedStep); at < 0 {
				return c.fail(protocol.ExitNotFound, "build %d did not fail", b.Number)
			}
		} else {
			n, err := strconv.Atoi(want)
			if err != nil || n < 0 || n > len(steps) {
				return c.fail(protocol.ExitUsage, "--step takes 0 (the setup) to %d, or failed", len(steps))
			}
			for i, s := range sections {
				if s.N == n {
					at = i
				}
			}
			if at < 0 {
				return c.fail(protocol.ExitNotFound, "build %d has no output for step %d", b.Number, n)
			}
		}
		log = []byte(sections[at].Text)
	}
	if tail > 0 {
		log = tailLines(log, tail)
	}
	if c.Term.Cols > 0 {
		log = []byte(c.Term.buildLog(string(log), failed))
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
		return c.usage()
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
		tb := c.table(w, "NAME", "WHEN")
		for _, j := range out {
			when := "on push"
			switch {
			case j.Schedule != "":
				when = "schedule " + j.Schedule
			case j.Tags != "":
				when = "tags " + j.Tags
			}
			tb.row(cRef(j.Name), cText(when))
		}
		tb.flush()
	})
}

func runBuildTrigger(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.usage()
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
		tree, _ := gitutil.ResolveTree(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name), sha)
		n, err := c.Store.CreateBuild(repo.ID, j.Name, sha, repo.DefaultBranch, string(steps), j.Image, tree, true)
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
		return c.usage()
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
		return c.usage()
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
		return c.usage()
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
		tb := c.table(w, "NAME")
		for _, n := range names {
			tb.row(cRef(n))
		}
		tb.flush()
	})
}

// runnerSession resolves the key behind a runner-protocol session:
// Source is the key's fingerprint. A build is claimed by a key, so a
// session without one is told so plainly rather than half-running. An
// admin key is accepted so an operator can rotate at their own pace; a
// runner host should hold a key added with --scope runner.
func runnerSession(c *Ctx) (store.SSHKey, int) {
	if c.Scope != "runner" && !c.User.IsAdmin {
		return store.SSHKey{}, c.fail(protocol.ExitDenied, "runner commands need a key added with --scope runner")
	}
	key, err := c.Store.SSHKeyByFingerprint(c.Source)
	if err != nil {
		return store.SSHKey{}, c.fail(protocol.ExitDenied, "runner commands need an SSH key session")
	}
	return key, -1
}

// runnerAdmin reports whether a session claims builds instance-wide. The
// bypass is the key, not the account: a scope-runner key is confined to
// its attachments whoever owns it, including an instance admin.
func runnerAdmin(c *Ctx) bool {
	return c.User.IsAdmin && c.Scope != "runner"
}

// runnerMayBuild reports whether a runner session may act on a
// repository's builds: an admin key may on any, a runner key on the
// repositories it is attached to (#184).
func runnerMayBuild(c *Ctx, key store.SSHKey, repoID int64) (bool, error) {
	if runnerAdmin(c) {
		return true, nil
	}
	return c.Store.RunnerAttached(key.ID, repoID)
}

// maxOrphanSkip bounds how many claimed builds runRunnerNext will find
// unreachable and cancel in one call before giving up. Only fast-forward
// merges are allowed here, so any branch whose target advances gets
// rebased and force-pushed, and a stack of branches can do that repeatedly
// in one sitting — the issue this guards saw five in an afternoon. The cap
// is well above that, so a real backlog is never cut short, while a
// repository whose queue is orphaned end to end still returns rather than
// walking it forever.
const maxOrphanSkip = 50

// publicSSH is the instance's ssh destination as anyone outside reaches
// it. A runner on the daemon's own host polls over loopback and takes
// the port from it for its builds' GITBAY_SSH, which names pasta's
// address for the host (#260). The port is added only when it is not
// 22: hutch and orgo build ssh://$GITBAY_SSH/... URLs, valid in both
// forms. Empty when site_url is not set.
func publicSSH(c *Ctx) string {
	host := c.Cfg.SiteHost()
	if host == "" {
		return ""
	}
	if p := c.Cfg.SSH.Port; p != 0 && p != 22 {
		return "git@" + net.JoinHostPort(host, strconv.Itoa(p))
	}
	return "git@" + host
}

func runRunnerNext(c *Ctx, args []string) int {
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
	f, err := c.parseArgs(args, flagSpec{Bools: []string{"--untrusted"}, MaxPos: -1,
		Usage: "runner next [--untrusted] [<owner/name>...]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	// The candidate set. An admin key claims from any repository, narrowed
	// by the names given. A runner key claims from the repositories it is
	// attached to; a name outside them is refused, not ignored, so a
	// misconfigured runner says so instead of idling.
	var repoIDs []int64
	for _, arg := range f.Pos {
		repo, code := resolveRepo(c, arg, policy.CanRead)
		if code >= 0 {
			return code
		}
		ok, err := runnerMayBuild(c, key, repo.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !ok {
			return c.fail(protocol.ExitDenied, "this key is not attached to %s; a repository admin attaches it with repo runner add", repo.Path())
		}
		repoIDs = append(repoIDs, repo.ID)
	}
	if !runnerAdmin(c) && len(repoIDs) == 0 {
		repoIDs, err = c.Store.RunnerRepoIDs(key.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if len(repoIDs) == 0 {
			// Nothing attached: nothing to claim. Still a heartbeat, so
			// admin runners shows the key polling.
			c.Store.TouchRunner(key.ID, c.User.ID, "", 0)
			return c.emit(map[string]any{}, func(w io.Writer) { fmt.Fprintln(w, "no pending builds") })
		}
	}
	untrusted := f.Has("--untrusted")
	var b store.Build
	var repo store.Repo
	var ok bool
	for attempt := 0; attempt < maxOrphanSkip; attempt++ {
		b, ok, err = c.Store.ClaimBuild(repoIDs, untrusted)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !ok {
			break
		}
		repo, err = c.Store.RepoByID(b.RepoID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		// Only fast-forward merges are allowed here, so a target that
		// advances gets rebased and force-pushed, orphaning whatever was
		// queued for the old head: the runner would clone the repo and
		// fail at checkout with a git internal error that reads exactly
		// like a real failure. Catch it here instead. A check that itself
		// fails is not evidence of anything — the build runs for real and
		// is left to fail on its own terms, never cancelled on a guess.
		reachable, err := gitutil.Reachable(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name), b.SHA)
		if err != nil || reachable {
			break
		}
		if code := cancelOrphanedBuild(c, repo, b); code >= 0 {
			return code
		}
		// Cancelled, not claimed: if the cap is hit right here, the runner
		// heartbeat below must not record this build as the one handed out.
		b, ok = store.Build{}, false
	}
	// The poll itself is the runner's heartbeat: admin runners reads it.
	c.Store.TouchRunner(key.ID, c.User.ID, strings.Join(f.Pos, ","), b.ID)
	if !ok {
		return c.emit(map[string]any{}, func(w io.Writer) { fmt.Fprintln(w, "no pending builds") })
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
		ID     int64    `json:"id"`
		Repo   string   `json:"repo"`
		Number int64    `json:"number"`
		Job    string   `json:"job"`
		SHA    string   `json:"sha"`
		Ref    string   `json:"ref"`
		Steps  []string `json:"steps"`
		Image  string   `json:"image,omitempty"`
		// Trusted is always sent: a runner decides a build's home and
		// secrets from it, and reads a missing field as untrusted (#255).
		Trusted bool `json:"trusted"`
		// SSH is the instance's public destination; a runner polling
		// over loopback takes its port for the build's GITBAY_SSH (#260).
		SSH     string            `json:"ssh,omitempty"`
		Secrets map[string]string `json:"secrets,omitempty"`
	}{ID: b.ID, Repo: repo.Path(), Number: b.Number, Job: b.Job, SHA: b.SHA, Ref: b.Ref,
		Steps: steps, Image: b.Image, Trusted: b.Trusted, SSH: publicSSH(c), Secrets: secrets}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "build %d: %s %s @ %.10s\n", d.ID, d.Repo, d.Job, d.SHA)
	})
}

func runRunnerLog(c *Ctx, args []string) int {
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
	if len(args) != 1 {
		return c.usage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad build id %q", args[0])
	}
	if b, err := c.Store.BuildByID(id); err != nil {
		return c.fail(protocol.ExitNotFound, "no build %d", id)
	} else if ok, err := runnerMayBuild(c, key, b.RepoID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if !ok {
		return c.fail(protocol.ExitDenied, "this key is not attached to the build's repository; a repository admin attaches it with repo runner add")
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
				// The stream ending is the last thing the server hears
				// from a runner that is about to die; note the time so
				// the scheduler can fail the build if no outcome follows.
				if err := c.Store.MarkBuildLogClosed(id); err != nil {
					slog.Warn("marking build log closed", "build", id, "err", err)
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
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--step", "--reason"}, MaxPos: 2, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) != 2 || (f.Pos[1] != "success" && f.Pos[1] != "failure") {
		return c.usage()
	}
	outcome := f.Pos[1]
	id, err := strconv.ParseInt(f.Pos[0], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad build id %q", f.Pos[0])
	}
	b, err := c.Store.BuildByID(id)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no build %d", id)
	}
	if ok, err := runnerMayBuild(c, key, b.RepoID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if !ok {
		return c.fail(protocol.ExitDenied, "this key is not attached to the build's repository; a repository admin attaches it with repo runner add")
	}
	// Cancelled underneath the runner: its report is late, not wrong.
	// The row, the status and the log were settled by the cancel.
	if b.Status == "cancelled" {
		c.Store.RunnerDone(key.ID)
		return c.emit(map[string]any{"build": b.Number, "status": "cancelled"}, func(w io.Writer) {
			fmt.Fprintf(w, "build %d was cancelled\n", b.Number)
		})
	}
	if outcome == "failure" {
		// A step the job does not have is recorded as none rather than
		// refused: refusing would lose the outcome over a detail (#266).
		var steps []string
		json.Unmarshal([]byte(b.Steps), &steps)
		step, _ := strconv.Atoi(f.Value("--step"))
		if step < 0 || step > len(steps) {
			step = 0
		}
		if err := c.Store.SetBuildFailure(id, step, failureReason(f.Value("--reason"))); err != nil && !errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitFailure, "recording build %d's failure: %v", id, err)
		}
	}
	if err := c.Store.FinishBuild(id, outcome); err != nil {
		return c.fail(protocol.ExitFailure, "finishing build %d: %v", id, err)
	}
	c.Store.RunnerDone(key.ID)
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), b.Number)
	desc := "build " + outcome
	if err := c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, outcome, desc, url, c.User.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "build."+outcome,
		fmt.Sprintf(`{"number":%d,"job":%q,"sha":%q}`, b.Number, b.Job, b.SHA))
	TryQueuedMergesAt(c.Store, c.Cfg, repo.ID, b.SHA)
	// A red build mails the repo's notify targets with the log tail — a
	// failed scheduled job must not wait to be noticed.
	if outcome == "failure" {
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
	return c.emit(map[string]any{"build": b.Number, "status": outcome}, func(w io.Writer) {
		fmt.Fprintf(w, "build %d %s\n", b.Number, outcome)
	})
}

// failureReason keeps a runner's reason to one line of at most 200
// bytes: it is shown on the build page and by build show.
func failureReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.ToValidUTF8(s, "")
}

// QueueBranchBuilds reads .gitbay/ci.yml at sha and creates one pending
// build per push job, with a pending commit status the runner resolves.
// A broken config surfaces as a failed "ci/config" status, not silence.
//
// Both paths that move a branch call this: post-receive for a push, and
// the merge path for a merge, which updates the ref directly and so never
// reaches a hook. old is the branch's sha before this update, the diff
// base a job's path filters run against; a new branch has no prior
// commit and sends old as empty or all zeros. queueJobs falls back to
// the merge base with the default branch in that case, so a filter
// still applies to a branch's first push — the shape most changes have,
// since branch-then-MR is the normal workflow here.
func QueueBranchBuilds(
	st *store.Store, root, siteURL string,
	repo store.Repo, userID int64, branch, old, sha string, now time.Time,
) {
	queueJobs(st, root, siteURL, repo, userID, branch, old, sha, now, true, branch == repo.DefaultBranch, true)
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
	// No old sha, and unlike QueueBranchBuilds, no merge-base fallback
	// either: this deliberately keeps failing open and running every
	// job. require_checks refuses a merge when an MR head has no
	// statuses at all (mr.go), so filtering a head down to zero jobs
	// would make it unmergeable rather than just unfiltered (#172).
	queueJobs(st, root, siteURL, repo, userID, mrHeadRef(n), "", sha, time.Now(), false, false, false)
}

// skipReason names why a job's path filters excluded this push, mirroring
// the order ci.Selected checks them in: an unmatched paths list rules a
// job out before paths-ignore is even considered.
func skipReason(j ci.Job, changed []string) string {
	if len(j.Paths) > 0 {
		hit := false
		for _, f := range changed {
			for _, p := range j.Paths {
				if ci.Match(p, f) {
					hit = true
				}
			}
		}
		if !hit {
			return "no changed file matches paths"
		}
	}
	return "every changed file matched paths-ignore"
}

func queueJobs(
	st *store.Store, root, siteURL string,
	repo store.Repo, userID int64, ref, old, sha string, now time.Time,
	trusted, syncSchedules, deriveMergeBase bool,
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
	// A job's result is a property of the tree, not the commit: a rebase
	// onto a base that touched nothing the branch did gives every commit
	// a new sha and the same tree, and re-running the suite over it
	// proves nothing it did not already prove (#177). A success recorded
	// against the tree stands for the new commit.
	tree, _ := gitutil.ResolveTree(dir, sha)
	// The changed-file list a job's path filters run against, computed
	// once and only if some job actually declares one. When the diff
	// base does not exist or the diff itself fails, filtered stays
	// false and every job runs: a filter that cannot be evaluated must
	// not silently skip CI.
	//
	// A branch's first push has no old sha, but a diff base still
	// exists: the merge base with the default branch. Without deriving
	// one, every job runs on every new branch, and since branch-then-MR
	// is the normal workflow, that is the push path filters matter most
	// for. The merge base of the default branch's tip with itself is
	// the tip, carrying no diff — that covers the default branch's own
	// first push on a fresh repository, and must fail open rather than
	// read as "nothing changed".
	filtered := false
	var changed []string
	for _, j := range jobs {
		if len(j.Paths) == 0 && len(j.PathsIgnore) == 0 {
			continue
		}
		diffOld := old
		// A force-push rewrote the branch, so the old tip is not an
		// ancestor of the new one and old..new is not "what this push
		// changed" — it is the difference between two histories. After a
		// rebase that is whatever the new base added, typically nothing
		// the branch itself touched, so every path filter concludes its
		// job is unnecessary and the branch reads as green without its
		// suite having run (#176). The merge base is the honest base:
		// the filter is deciding about the branch's relationship to its
		// target, which is what the merge base expresses.
		if ci.HasDiffBase(diffOld) && deriveMergeBase {
			if ok, err := gitutil.IsAncestor(dir, diffOld, sha); err != nil || !ok {
				diffOld = ""
			}
		}
		if !ci.HasDiffBase(diffOld) && deriveMergeBase {
			if base, err := gitutil.MergeBase(dir, "refs/heads/"+repo.DefaultBranch, sha); err == nil && base != sha {
				diffOld = base
			}
		}
		if ci.HasDiffBase(diffOld) {
			if files, err := gitutil.DiffFiles(dir, diffOld, sha); err == nil {
				changed, filtered = files, true
			}
		}
		break
	}
	var schedules []store.Schedule
	for _, j := range jobs {
		// Tag jobs run on matching tag pushes only.
		if j.Tags != "" {
			continue
		}
		// A build of this commit that passed, or is queued or running,
		// stands for it — unless this queue is trusted and that build was
		// not: a fork's head that lands on a branch is built again as the
		// repository's own (#258).
		if b, ok := built[j.Name]; ok && (b.Trusted || !trusted) &&
			(b.Status == "success" || b.Status == "pending" || b.Status == "running") {
			continue
		}
		if prev, ok, _ := st.SuccessBuildForTree(repo.ID, tree, j.Name, j.Image); ok && prev.SHA != sha {
			url := fmt.Sprintf("%s/%s/builds/%d", siteURL, repo.Path(), prev.Number)
			st.SetCommitStatus(repo.ID, sha, "ci/"+j.Name, "success",
				fmt.Sprintf("passed in build %d as %.10s, same tree", prev.Number, prev.SHA), url, userID)
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
		// A filter that excludes this push is not silence: it satisfies
		// require_checks with a skipped status instead of leaving the
		// commit with none at all, which the gate refuses outright (#172).
		if filtered && !ci.Selected(j, changed) {
			st.SetCommitStatus(repo.ID, sha, "ci/"+j.Name, "skipped", skipReason(j, changed), "", userID)
			continue
		}
		steps, _ := json.Marshal(j.Steps)
		n, err := st.CreateBuild(repo.ID, j.Name, sha, ref, string(steps), j.Image, tree, trusted)
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

// resolveCancelledCommitStatus sets the commit status for a build that was
// just cancelled: if the commit already passed this job on another ref,
// that result stands again; otherwise the context reports the
// cancellation as an error, so the queued status left behind is never
// pending forever.
func resolveCancelledCommitStatus(c *Ctx, repo store.Repo, b store.Build) {
	if prev, ok, err := c.Store.SuccessBuildFor(repo.ID, b.SHA, b.Job); err == nil && ok {
		url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), prev.Number)
		c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, "success",
			fmt.Sprintf("passed in build %d on %s", prev.Number, prev.Ref), url, c.User.ID)
		TryQueuedMergesAt(c.Store, c.Cfg, repo.ID, b.SHA)
		return
	}
	url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), b.Number)
	c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, "error", "cancelled", url, c.User.ID)
}

// cancelOrphanedBuild withdraws a build runRunnerNext claimed and then
// found unreachable. It leaves the same shape behind as a build cancel a
// person runs by hand: CancelBuild's status, a log line saying why, and
// the commit status resolved rather than left pending. Returns -1 to mean
// "handled, keep going"; anything else is the exit code to return.
func cancelOrphanedBuild(c *Ctx, repo store.Repo, b store.Build) int {
	if err := c.Store.CancelBuild(b.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.AppendBuildLog(b.ID, []byte(fmt.Sprintf(
		"cancelled: %.10s is not reachable from any ref; the sha was likely orphaned by a force-push\n", b.SHA)))
	resolveCancelledCommitStatus(c, repo, b)
	c.Store.RecordEvent(repo.ID, c.User.ID, "build.cancelled", fmt.Sprintf(`{"number":%d,"job":%q,"sha":%q}`, b.Number, b.Job, b.SHA))
	return -1
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
		return c.fail(protocol.ExitDenied, "cancelling a build needs write access to %s; ask its owner", repo.Path())
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
	// The queued status replaced whatever the commit had for this job.
	resolveCancelledCommitStatus(c, repo, b)
	c.Store.RecordEvent(repo.ID, c.User.ID, "build.cancelled", fmt.Sprintf(`{"number":%d,"job":%q,"sha":%q}`, b.Number, b.Job, b.SHA))
	return c.emit(map[string]any{"number": b.Number, "job": b.Job, "status": "cancelled", "was": b.Status}, func(w io.Writer) {
		if b.Status == "running" {
			fmt.Fprintf(w, "cancelled %s build %d (%s); the runner stops at its next check\n", repo.Path(), b.Number, b.Job)
			return
		}
		fmt.Fprintf(w, "cancelled %s build %d (%s)\n", repo.Path(), b.Number, b.Job)
	})
}
