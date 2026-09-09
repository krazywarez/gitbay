package hookd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// Every push shape, and what dedupe, the path filters, the schedules and
// the reaper make of it. The table on the wiki's CI page is these rows;
// the test asserts each against the code and then against the page, so
// the two cannot drift apart (#184). #176 was a shape nobody had listed.
//
// One ci.yml for every shape, five jobs that each isolate one rule:
//
//	plain    no filter
//	onapp    paths: app/**
//	notapp   paths-ignore: app/**
//	nightly  schedule
//	release  tags: v*
//
// Every push in the table changes app/x and nothing else, so a filtered
// push queues onapp and skips notapp, and a push whose filters cannot be
// evaluated queues both.

const shapesCI = `jobs:
  plain:
    steps:
      - echo plain
  onapp:
    paths:
      - app/**
    steps:
      - echo onapp
  notapp:
    paths-ignore:
      - app/**
    steps:
      - echo notapp
  nightly:
    schedule: "0 3 * * *"
    steps:
      - echo nightly
  release:
    tags: "v*"
    steps:
      - echo release
`

var shapeJobs = []string{"plain", "onapp", "notapp", "nightly", "release"}

const zeroSHA40 = "0000000000000000000000000000000000000000"

type shapeFixture struct {
	t     *testing.T
	st    *store.Store
	repo  store.Repo
	uid   int64
	root  string
	src   string
	dir   string
	srv   *Server
	sched *ci.Scheduler
	base  string
	// Snapshot taken by mark, so the observation reports only what the
	// shape itself produced.
	markedSHA    string
	buildsBefore int
	statusBefore map[string]string
	schedBefore  map[string]bool
}

func newShapeFixture(t *testing.T) *shapeFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &shapeFixture{t: t, st: st, repo: repo, uid: uid, root: root, src: filepath.Join(root, "src")}
	f.dir = control.RepoDir(root, repo.OwnerName, repo.Name)
	cfg := config.Config{}
	cfg.Server.Root, cfg.Server.SiteURL = root, "https://x.test"
	f.srv = &Server{cfg: cfg, st: st}
	f.sched = &ci.Scheduler{St: st, SiteURL: "https://x.test",
		RepoDir: func(owner, name string) string { return control.RepoDir(root, owner, name) }}

	os.MkdirAll(filepath.Join(f.src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(f.src, "app"), 0o755)
	os.MkdirAll(filepath.Join(f.src, "docs"), 0o755)
	f.write(".gitbay/ci.yml", shapesCI)
	f.write("app/x", "1\n")
	f.write("docs/d", "d\n")
	f.git(root, "init", "-q", "-b", "main", "src")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "base")
	f.base = f.sha("HEAD")
	os.MkdirAll(filepath.Dir(f.dir), 0o755)
	f.git(root, "init", "-q", "--bare", f.dir)
	f.sync()
	return f
}

func (f *shapeFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (f *shapeFixture) write(rel, content string) {
	if err := os.WriteFile(filepath.Join(f.src, rel), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *shapeFixture) sha(ref string) string {
	return strings.TrimSpace(f.git(f.src, "rev-parse", ref))
}

// sync moves every branch and tag of the working repository into the
// served bare one, as the pushes the shapes stand for would have.
func (f *shapeFixture) sync() {
	f.git(f.src, "push", "-q", "--force", f.dir, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
}

// appCommit commits a change to app/x on the current branch.
func (f *shapeFixture) appCommit(msg string) string {
	f.write("app/x", msg+"\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", msg)
	return f.sha("HEAD")
}

func (f *shapeFixture) push(branch, old, sha string) {
	f.sync()
	control.QueueBranchBuilds(f.st, f.root, "https://x.test", f.repo, f.uid, branch, old, sha, time.Now())
}

// finish reports every pending build as a runner would, status on the
// commit included.
func (f *shapeFixture) finish(status string) {
	for {
		b, ok, err := f.st.ClaimBuild([]int64{f.repo.ID}, true)
		if err != nil {
			f.t.Fatal(err)
		}
		if !ok {
			return
		}
		if err := f.st.FinishBuild(b.ID, status); err != nil {
			f.t.Fatal(err)
		}
		f.st.SetCommitStatus(f.repo.ID, b.SHA, "ci/"+b.Job, status, "reported", "", f.uid)
	}
}

func (f *shapeFixture) statuses(sha string) map[string]string {
	out := map[string]string{}
	list, err := f.st.ListCommitStatuses(f.repo.ID, sha)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, s := range list {
		out[s.Context] = s.State + ": " + s.Description
	}
	return out
}

func (f *shapeFixture) schedules() map[string]bool {
	out := map[string]bool{}
	due, err := f.st.DueSchedules("9999-01-01T00:00:00Z")
	if err != nil {
		f.t.Fatal(err)
	}
	for _, s := range due {
		out[s.Job] = true
	}
	return out
}

// mark snapshots the state the observed action starts from.
func (f *shapeFixture) mark(sha string) {
	builds, err := f.st.ListBuilds(f.repo.ID, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	f.markedSHA, f.buildsBefore = sha, len(builds)
	f.statusBefore, f.schedBefore = f.statuses(sha), f.schedules()
}

// observe reduces what the action produced to one word per job, plus
// the ci/config status. The words are the table's vocabulary.
func (f *shapeFixture) observe() map[string]string {
	out := map[string]string{}
	builds, err := f.st.ListBuilds(f.repo.ID, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	fresh := map[string]store.Build{}
	for _, b := range builds[:len(builds)-f.buildsBefore] { // newest first
		fresh[b.Job] = b
	}
	after, sched := f.statuses(f.markedSHA), f.schedules()
	for _, j := range shapeJobs {
		switch st, changed := after["ci/"+j], after["ci/"+j] != f.statusBefore["ci/"+j]; {
		case fresh[j].Status == "pending" && !fresh[j].Trusted:
			out[j] = "queued, untrusted"
		case fresh[j].Status == "pending":
			out[j] = "queued"
		case changed && strings.HasPrefix(st, "failure: build abandoned"):
			out[j] = "abandoned"
		case changed && strings.HasPrefix(st, "skipped:"):
			out[j] = "skipped"
		case changed && strings.HasPrefix(st, "success:") && strings.Contains(st, "same tree"):
			out[j] = "reused"
		case changed:
			out[j] = st
		case sched[j] && !f.schedBefore[j]:
			out[j] = "registered"
		default:
			out[j] = "—"
		}
	}
	cfg, changed := after["ci/config"], after["ci/config"] != f.statusBefore["ci/config"]
	if changed && strings.HasPrefix(cfg, "failure:") {
		out["ci/config"] = "failure"
	} else {
		out["ci/config"] = "—"
	}
	return out
}

type pushShape struct {
	name string
	run  func(f *shapeFixture)
	want []string // plain, onapp, notapp, nightly, release, ci/config
}

var pushShapes = []pushShape{
	{"first push of the default branch", func(f *shapeFixture) {
		f.mark(f.base)
		f.push("main", zeroSHA40, f.base)
	}, []string{"queued", "queued", "queued", "registered", "—", "—"}},

	{"push to the default branch", func(f *shapeFixture) {
		c := f.appCommit("more")
		f.mark(c)
		f.push("main", f.base, c)
	}, []string{"queued", "queued", "skipped", "registered", "—", "—"}},

	{"push to another branch", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c := f.appCommit("more")
		f.mark(c)
		f.push("feat", f.base, c)
	}, []string{"queued", "queued", "skipped", "—", "—", "—"}},

	{"a new branch, no old sha", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c := f.appCommit("more")
		f.mark(c)
		f.push("feat", zeroSHA40, c)
	}, []string{"queued", "queued", "skipped", "—", "—", "—"}},

	{"rebase onto a moved default branch, old not an ancestor", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c1 := f.appCommit("more")
		f.git(f.src, "checkout", "-q", "main")
		f.write("docs/d", "moved\n")
		f.git(f.src, "add", ".")
		f.git(f.src, "commit", "-q", "-m", "docs on main")
		f.git(f.src, "checkout", "-q", "feat")
		f.git(f.src, "rebase", "-q", "main")
		c2 := f.sha("HEAD")
		f.mark(c2)
		f.push("feat", c1, c2)
	}, []string{"queued", "queued", "skipped", "—", "—", "—"}},

	{"rewritten commit, same tree as a passed build", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c1 := f.appCommit("more")
		f.push("feat", zeroSHA40, c1)
		f.finish("success")
		f.git(f.src, "commit", "-q", "--allow-empty", "-m", "rewritten")
		c2 := f.sha("HEAD")
		f.mark(c2)
		f.push("feat", c1, c2)
	}, []string{"reused", "reused", "skipped", "—", "—", "—"}},

	{"fast-forward of a commit built on another branch", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c1 := f.appCommit("more")
		f.push("feat", zeroSHA40, c1)
		f.finish("success")
		f.git(f.src, "checkout", "-q", "main")
		f.git(f.src, "merge", "-q", "--ff-only", "feat")
		f.mark(c1)
		f.push("main", f.base, c1)
	}, []string{"—", "—", "—", "registered", "—", "—"}},

	{"a commit whose earlier build failed, on a new branch", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c1 := f.appCommit("more")
		f.push("feat", zeroSHA40, c1)
		f.finish("failure")
		f.git(f.src, "branch", "-q", "again", "feat")
		f.mark(c1)
		f.push("again", zeroSHA40, c1)
	}, []string{"queued", "queued", "—", "—", "—", "—"}},

	{"merge request head from a fork", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c1 := f.appCommit("more")
		f.git(f.src, "push", "-q", f.dir, "+refs/heads/feat:refs/merge-requests/1/head")
		f.mark(c1)
		control.QueueMRBuilds(f.st, f.root, "https://x.test", f.repo, f.uid, 1, c1)
	}, []string{"queued, untrusted", "queued, untrusted", "queued, untrusted", "—", "—", "—"}},

	{"tag push", func(f *shapeFixture) {
		f.git(f.src, "tag", "v1")
		f.sync()
		f.mark(f.base)
		f.srv.queueTagBuilds(f.repo, f.uid, "v1", f.base)
	}, []string{"—", "—", "—", "—", "queued", "—"}},

	{"schedule tick on the default branch", func(f *shapeFixture) {
		f.push("main", zeroSHA40, f.base)
		f.mark(f.base)
		f.sched.RunDue(time.Now().AddDate(1, 0, 0))
	}, []string{"—", "—", "—", "queued", "—", "—"}},

	{"claimed builds whose runner vanished", func(f *shapeFixture) {
		f.git(f.src, "checkout", "-q", "-b", "feat")
		c1 := f.appCommit("more")
		f.push("feat", zeroSHA40, c1)
		for i := 0; i < 2; i++ {
			if _, ok, err := f.st.ClaimBuild(nil, true); err != nil || !ok {
				f.t.Fatalf("claim: %v ok=%v", err, ok)
			}
		}
		if _, err := f.st.DB.Exec(`UPDATE builds SET started_at = '2020-01-01T00:00:00Z'`); err != nil {
			f.t.Fatal(err)
		}
		f.mark(c1)
		f.sched.RunDue(time.Now())
	}, []string{"abandoned", "abandoned", "—", "—", "—", "—"}},

	{"push to the default branch with an old sha that cannot be diffed", func(f *shapeFixture) {
		c := f.appCommit("more")
		f.mark(c)
		f.push("main", strings.Repeat("1", 40), c)
	}, []string{"queued", "queued", "queued", "registered", "—", "—"}},

	{"push with a broken ci.yml", func(f *shapeFixture) {
		f.write(".gitbay/ci.yml", "jobs: [\n")
		c := f.appCommit("broken")
		f.mark(c)
		f.push("main", f.base, c)
	}, []string{"—", "—", "—", "—", "—", "failure"}},

	{"push with no ci.yml", func(f *shapeFixture) {
		f.git(f.src, "rm", "-q", ".gitbay/ci.yml")
		c := f.appCommit("no ci")
		f.mark(c)
		f.push("main", f.base, c)
	}, []string{"—", "—", "—", "—", "—", "—"}},
}

var shapeColumns = append([]string{"push"}, append(append([]string{}, shapeJobs...), "ci/config")...)

// tableRow renders one shape the way the wiki's table carries it.
func tableRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}

// normalizeRow strips the alignment padding org-mode adds to a table.
func normalizeRow(line string) string {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return tableRow(cells)
}

func TestPushShapes(t *testing.T) {
	for _, sh := range pushShapes {
		t.Run(sh.name, func(t *testing.T) {
			f := newShapeFixture(t)
			sh.run(f)
			got := f.observe()
			for i, j := range append(append([]string{}, shapeJobs...), "ci/config") {
				if got[j] != sh.want[i] {
					t.Errorf("%s: %s = %q, want %q", sh.name, j, got[j], sh.want[i])
				}
			}
		})
	}
}

// The wiki's table is the test's expectations rendered as rows, and the
// header names the columns; a row the page lacks or states differently
// fails here.
func TestPushShapesTableOnWiki(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".gitbay", "wiki", "CI.org"))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			have[normalizeRow(line)] = true
		}
	}
	if !have[tableRow(shapeColumns)] {
		t.Errorf("CI.org lacks the header row %s", tableRow(shapeColumns))
	}
	for _, sh := range pushShapes {
		row := tableRow(append([]string{sh.name}, sh.want...))
		if !have[row] {
			t.Errorf("CI.org lacks the row %s", row)
		}
	}
	if t.Failed() {
		fmt.Fprintln(os.Stderr, "the table as the code has it:")
		fmt.Fprintln(os.Stderr, tableRow(shapeColumns))
		for _, sh := range pushShapes {
			fmt.Fprintln(os.Stderr, tableRow(append([]string{sh.name}, sh.want...)))
		}
	}
}
