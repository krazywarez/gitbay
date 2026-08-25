package control

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"build", "list"},
		Summary: "list recent builds: build list <owner/name>", ReadOnly: true, Run: runBuildList})
	register(Command{Path: []string{"build", "show"},
		Summary: "show one build: build show <owner/name> <n>", ReadOnly: true, Run: runBuildShow})
	register(Command{Path: []string{"build", "log"},
		Summary: "print a build's log: build log <owner/name> <n>", ReadOnly: true, Run: runBuildLog})

	// Runner commands: the claim/report loop for gitbay-runner. Admin-only —
	// a runner executes arbitrary repo code, so handing out jobs is the
	// instance operator's call.
	register(Command{Path: []string{"runner", "next"},
		Summary: "claim the oldest pending build (runner protocol)", SSHOnly: true, Run: runRunnerNext})
	register(Command{Path: []string{"runner", "log"},
		Summary: "append a build's log from stdin: runner log <build-id>", SSHOnly: true, ReadsStdin: true, Run: runRunnerLog})
	register(Command{Path: []string{"runner", "done"},
		Summary: "finish a build: runner done <build-id> success|failure", SSHOnly: true, Run: runRunnerDone})
}

type buildOut struct {
	Number     int64  `json:"number"`
	Job        string `json:"job"`
	Status     string `json:"status"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

func buildToOut(b store.Build) buildOut {
	return buildOut{b.Number, b.Job, b.Status, b.SHA, b.Ref, b.CreatedAt, b.FinishedAt}
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
	var ds []buildOut
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

func requireRunner(c *Ctx) int {
	if !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "runner commands are for instance-admin runner accounts")
	}
	return -1
}

func runRunnerNext(c *Ctx, args []string) int {
	if code := requireRunner(c); code >= 0 {
		return code
	}
	b, ok, err := c.Store.ClaimBuild()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if !ok {
		return c.emit(map[string]any{}, func(w io.Writer) { fmt.Fprintln(w, "no pending builds") })
	}
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var steps []string
	json.Unmarshal([]byte(b.Steps), &steps)
	d := struct {
		ID     int64    `json:"id"`
		Repo   string   `json:"repo"`
		Number int64    `json:"number"`
		Job    string   `json:"job"`
		SHA    string   `json:"sha"`
		Ref    string   `json:"ref"`
		Steps  []string `json:"steps"`
	}{b.ID, repo.Path(), b.Number, b.Job, b.SHA, b.Ref, steps}
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
	// Stream stdin into the log in chunks so long builds appear live.
	buf := make([]byte, 64<<10)
	for {
		n, rerr := c.Stdin.Read(buf)
		if n > 0 {
			if err := c.Store.AppendBuildLog(id, buf[:n]); err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
		}
		if rerr != nil {
			break
		}
	}
	return c.emit(map[string]string{"log": "ok"}, func(w io.Writer) {})
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
	if err := c.Store.FinishBuild(id, args[1]); err != nil {
		return c.fail(protocol.ExitFailure, "finishing build %d: %v", id, err)
	}
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
	return c.emit(map[string]any{"build": b.Number, "status": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "build %d %s\n", b.Number, args[1])
	})
}
