// gitbay-runner executes CI builds queued by a gitbay server. It polls over
// SSH — the same authenticated channel everything else uses — claims one
// build at a time, clones the repo, runs each step with `sh -c`, streams the
// combined output back, and reports success or failure.
//
// The account behind the runner's key must be an instance admin: a runner
// executes arbitrary repo code, so handing out jobs is the operator's call.
// v1 runs steps directly on the host under this process's user; run it as a
// dedicated unprivileged user.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gitbay.org/gitbay/internal/buildinfo"
	"gitbay.org/gitbay/internal/toolpath"
)

type job struct {
	ID      int64             `json:"id"`
	Repo    string            `json:"repo"`
	Number  int64             `json:"number"`
	Job     string            `json:"job"`
	SHA     string            `json:"sha"`
	Ref     string            `json:"ref"`
	Steps   []string          `json:"steps"`
	Image   string            `json:"image"`
	Secrets map[string]string `json:"secrets"`
}

type runner struct {
	remote    string // ssh destination, e.g. git@gitbay.org
	sshOpts   []string
	cloneBase string // e.g. ssh://git@gitbay.org
	workdir   string
	timeout   time.Duration
	// image is the container image for a job that names none, and
	// isolation selects how steps run: "podman" or "none".
	image     string
	isolation string
	// memory and cpus cap one build's cgroup; empty means no cap.
	memory string
	cpus   string
	// cgroups is the runner's build cgroup subtree, nil where the unit
	// is not delegated and builds run unconfined in the service cgroup.
	cgroups *buildCgroups
	// stepFn is step, replaceable by tests.
	stepFn func() (bool, error)
	// repos limits which repositories this runner claims builds for. Empty
	// means any, which is what a runner on the server itself wants; a runner
	// somewhere that should not execute every repository's steps names them.
	repos []string
	// untrusted also claims merge request heads from forks.
	untrusted bool
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		os.Exit(runInit(os.Args[2:]))
	}
	var (
		configPath = flag.String("config", defaultConfigPath(), "config file; keys are these flag names, flags override it")
		identity   = flag.String("identity", "", "ssh private key to poll and clone with (default: the key gitbay-runner init generated, if present)")
		untrusted  = flag.Bool("untrusted", false, "also claim untrusted builds: merge request heads from forks (needs -isolation podman to be safe)")
		remote     = flag.String("remote", "git@gitbay.org", "ssh destination of the gitbay server")
		sshOpts    = flag.String("ssh-opts", "", "extra ssh options, space-separated (also used for git clone)")
		cloneBase  = flag.String("clone-base", "", "clone URL prefix (default ssh://<remote>)")
		workdir    = flag.String("workdir", defaultWorkdir(), "build workspace root")
		poll       = flag.Duration("poll", 5*time.Second, "idle poll interval")
		timeout    = flag.Duration("timeout", 30*time.Minute, "per-build time limit")
		repos      = flag.String("repos", "", "only claim builds for these repositories, comma-separated owner/name (default: any)")
		once       = flag.Bool("once", false, "process at most one build, then exit")
		jobs       = flag.Int("jobs", 1, "builds to run at once")
		image      = flag.String("image", "", "default container image for jobs that name none")
		isolation  = flag.String("isolation", "podman", "how steps run: podman, or none for no container")
		memory     = flag.String("memory", "", "memory limit per build, e.g. 4g (podman only, needs a delegated cgroup; default unlimited)")
		cpus       = flag.String("cpus", "", "CPU limit per build, e.g. 2 (podman only, needs a delegated cgroup; default unlimited)")
		version    = flag.Bool("version", false, "print the commit this binary was built from, then exit")
	)
	path := configPathFromArgs(os.Args[1:], *configPath)
	if values, found, err := loadConfig(path); err != nil {
		log.Fatal(err)
	} else if found {
		if err := applyConfig(flag.CommandLine, values); err != nil {
			log.Fatal(err)
		}
		log.Printf("config: %s", path)
	}
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.String())
		return
	}
	// The runner links internal/store, so it goes stale on changes that never
	// touch cmd/gitbay-runner. Say which commit is running.
	log.Printf("gitbay-runner %s", buildinfo.String())
	r := &runner{
		remote:    *remote,
		cloneBase: *cloneBase,
		workdir:   *workdir,
		timeout:   *timeout,
		image:     *image,
		isolation: *isolation,
		memory:    *memory,
		cpus:      *cpus,
	}
	if r.isolation == isolationPodman {
		// Before podman runs anything: its pause process lands in the
		// cgroup of the first invocation, and that must be the runner's
		// leaf, not a build's.
		cg, err := prepareBuildCgroups()
		switch {
		case err == nil:
			r.cgroups = cg
		case r.memory != "" || r.cpus != "":
			// Limits that cannot be applied are refused, not dropped:
			// a runner that accepted -memory and ran uncapped is what
			// #188 was.
			log.Fatalf("-memory/-cpus: build cgroups unavailable: %v", err)
		default:
			log.Printf("build cgroups unavailable (%v); builds run unconfined in the service cgroup", err)
		}
	}
	if err := r.checkIsolation(); err != nil {
		// Refusing to start is the point. A runner that quietly fell back
		// to running repository code on the host would drop isolation
		// with nothing to surface it, which is worse than a stopped
		// runner: the operator sees a failed unit either way, but only
		// one of them is honest about why (#144).
		log.Fatalf("isolation: %v", err)
	}
	if *sshOpts != "" {
		r.sshOpts = strings.Fields(*sshOpts)
	}
	if *identity == "" {
		if p := filepath.Join(configDir(), "id_ed25519"); fileExists(p) {
			*identity = p
		}
	}
	r.sshOpts = append(identityOpts(*identity), r.sshOpts...)
	r.untrusted = *untrusted
	for _, name := range strings.Split(*repos, ",") {
		if name = strings.TrimSpace(name); name != "" {
			r.repos = append(r.repos, name)
		}
	}
	if r.cloneBase == "" {
		r.cloneBase = "ssh://" + *remote
	}
	// 0o700, not 0o755: a build's checkout and its secrets-bearing
	// environment are this user's business alone, and the default sits
	// beside other users' data on a shared host.
	if err := os.MkdirAll(r.workdir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := checkWorkdir(r.workdir); err != nil {
		log.Fatal(err)
	}
	n := *jobs
	if n < 1 {
		log.Fatal("-jobs must be at least 1")
	}
	if *once {
		// "at most one build" is one build, whatever -jobs says.
		n = 1
	}
	// `runner next` claims inside one transaction, so several workers
	// claiming at once is already safe; the runner just never used that.
	// Each build works in its own build-<id> directory, so they do not
	// meet on disk either.
	// A stop signal drains: no build is claimed after it, and each build
	// already in flight runs to completion and is reported. The old
	// behaviour was to die mid-build, which left the build "running" on
	// the server with nothing executing (#179). The unit's
	// TimeoutStopSec bounds the drain; a second signal ends it now.
	stop := make(chan struct{})
	go func() {
		sigs := make(chan os.Signal, 2)
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
		<-sigs
		log.Printf("draining: finishing builds in flight, claiming no more")
		close(stop)
		<-sigs
		log.Printf("second signal: exiting without draining")
		os.Exit(1)
	}()
	r.serve(n, *once, *poll, stop)
}

// serve runs n workers until stop closes. A worker checks stop only
// between builds, so closing it never interrupts one.
func (r *runner) serve(n int, once bool, poll time.Duration, stop <-chan struct{}) {
	if r.stepFn == nil {
		r.stepFn = r.step
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if n > 1 {
				time.Sleep(time.Duration(i) * poll / time.Duration(n))
			}
			for {
				select {
				case <-stop:
					return
				default:
				}
				ran, err := r.stepFn()
				if err != nil {
					log.Printf("runner: %v", err)
				}
				if once {
					return
				}
				if !ran {
					select {
					case <-stop:
						return
					case <-time.After(poll):
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

// step claims and executes at most one build. ran reports whether there was
// one, so the caller knows when to idle.
func (r *runner) step() (bool, error) {
	args := []string{"runner", "next"}
	if r.untrusted {
		args = append(args, "--untrusted")
	}
	args = append(append(args, r.repos...), "--json")
	out, err := r.ssh(nil, args...)
	if err != nil {
		return false, fmt.Errorf("claiming build: %w (%s)", err, out)
	}
	var env struct {
		Data job `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return false, fmt.Errorf("parsing job: %w", err)
	}
	if env.Data.ID == 0 {
		return false, nil
	}
	j := env.Data
	log.Printf("build %d: %s %s @ %.10s", j.ID, j.Repo, j.Job, j.SHA)
	status := "failure"
	if r.run(j) {
		status = "success"
	}
	if err := r.reportDone(j.ID, status); err != nil {
		return true, err
	}
	log.Printf("build %d: %s", j.ID, status)
	return true, nil
}

// logSink forwards a build's output to the server and swallows any error
// doing so. os/exec surfaces a write failure on a step's stdout through
// cmd.Wait(), so a sink that can fail is a sink that can fail the build it
// was only recording — a restart or a dropped session used to turn a green
// suite red, with the explaining line written to the same dead pipe. Losing
// log lines is the acceptable failure here; losing the build is not.
type logSink struct {
	mu sync.Mutex
	w  io.Writer // nil once a write has failed
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		if _, err := s.w.Write(p); err != nil {
			s.w = nil
		}
	}
	return len(p), nil
}

// broken reports whether the stream was lost, so a build can say its log is
// incomplete rather than appear to have simply stopped.
func (s *logSink) broken() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w == nil
}

// run clones, checks out, and executes the steps, streaming output to the
// server. Returns whether every step succeeded.
func (r *runner) run(j job) bool {
	dir := filepath.Join(r.workdir, fmt.Sprintf("build-%d", j.ID))
	defer os.RemoveAll(dir)

	// A build's HOME. Not the workspace, which is removed after every
	// build: the Go module cache, the sonar scanner and every other tool
	// cache live under HOME, so a per-build one re-downloads the world
	// each time. Not the runner's own home either, where its SSH key and
	// credential dotfiles are. A directory beside the workspaces is
	// neither.
	//
	// One per repository: shared across repositories, a step could poison
	// the module cache or plant a .gitconfig that another repository's
	// build would honour, and the container mounts the home read-write
	// (#184).
	buildHome, err := buildHomeFor(r.workdir, j.Repo)
	if err != nil {
		log.Printf("build %d: build home: %v", j.ID, err)
		return false
	}

	// One long-lived `runner log` session receives the whole stream.
	logCmd := exec.Command(toolpath.Look("ssh"), append(r.sshOpts, r.remote, "runner", "log", fmt.Sprint(j.ID))...)
	pipe, err := logCmd.StdinPipe()
	if err != nil {
		log.Printf("build %d: log pipe: %v", j.ID, err)
		return false
	}
	sink := &logSink{w: pipe}
	logCmd.Stdout, logCmd.Stderr = io.Discard, io.Discard
	if err := logCmd.Start(); err != nil {
		log.Printf("build %d: log stream: %v", j.ID, err)
		return false
	}
	// The server ends the log session with exit 3 when the build is
	// cancelled; any other end is a lost stream, which the sink absorbs.
	cancelled := make(chan struct{})
	logExited := make(chan struct{})
	go func() {
		defer close(logExited)
		err := logCmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
			close(cancelled)
			return
		}
		if err != nil {
			log.Printf("build %d: log session ended: %v", j.ID, err)
		}
	}()
	// runStep starts cmd and waits for it, the cancel signal, or the
	// deadline. Every phase goes through it, so a cancel during the clone
	// lands as fast as one during a step.
	runStep := func(cmd *exec.Cmd, deadline time.Time) (bool, string) {
		select {
		case <-cancelled:
			return false, "cancelled"
		default:
		}
		ownProcessGroup(cmd)
		if err := cmd.Start(); err != nil {
			return false, fmt.Sprintf("start: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		// After a kill, Wait returns once every holder of the log pipe is
		// gone; the group kill makes that prompt, and the cap makes sure a
		// straggler cannot hold the build open.
		reap := func() {
			killTree(cmd)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
		select {
		case err := <-done:
			if err != nil {
				return false, fmt.Sprintf("step failed: %v", err)
			}
			return true, ""
		case <-cancelled:
			reap()
			return false, "cancelled"
		case <-time.After(time.Until(deadline)):
			reap()
			return false, fmt.Sprintf("build timed out after %s", r.timeout)
		}
	}
	defer func() {
		select {
		case <-cancelled:
			log.Printf("build %d: cancelled", j.ID)
		default:
			if sink.broken() {
				log.Printf("build %d: log stream lost; stored log is incomplete", j.ID)
			}
		}
		pipe.Close()
		<-logExited
	}()

	gitSSH := strings.TrimSpace("ssh " + strings.Join(r.sshOpts, " "))
	cloneURL := r.cloneBase + "/" + j.Repo + ".git"
	deadline := time.Now().Add(r.timeout)
	fmt.Fprintf(sink, "$ git clone %s (%.10s)\n", cloneURL, j.SHA)
	// A merge request head lives under refs/merge-requests/, which a
	// clone does not fetch; ask for the ref before checking out.
	steps := [][]string{{"clone", "-q", cloneURL, dir}}
	if strings.HasPrefix(j.Ref, "refs/") {
		steps = append(steps, []string{"-C", dir, "fetch", "-q", "origin", j.Ref})
	}
	steps = append(steps, []string{"-C", dir, "checkout", "-q", j.SHA})
	for _, args := range steps {
		cmd := exec.Command(toolpath.Look("git"), args...)
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+gitSSH, "GIT_TERMINAL_PROMPT=0")
		cmd.Stdout, cmd.Stderr = sink, sink
		if ok, why := runStep(cmd, deadline); !ok {
			fmt.Fprintf(sink, "git %s: %s\n", args[0], why)
			return false
		}
	}

	env := stepEnv(j, buildHome)
	return r.runSteps(j, dir, env, sink, deadline, runStep)
}

// stepEnv builds the environment a build step runs with. It is
// constructed, not inherited: os.Environ() would hand repository content
// the runner's entire environment, including anything an operator set on
// the service (#144).
//
// HOME is the repository's build home, not the runner's own: tools read
// credentials out of dotfiles — .netrc, .npmrc, .gitconfig — and a build
// has no business finding the runner's. It is not the workspace either,
// because the workspace is deleted after every build and every tool
// cache lives under HOME.
//
// PATH is the one thing carried over: without it a step cannot find the
// tools the host was provisioned with.
// buildHomeFor is the build home for one repository: <workdir>/home/<owner>/<name>,
// created on first use. The repository path comes from the server, but a
// home must still never resolve outside the home root.
func buildHomeFor(workdir, repo string) (string, error) {
	root := filepath.Join(workdir, "home")
	dir := filepath.Join(root, filepath.FromSlash(repo))
	if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("repository path %q escapes the build home root", repo)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func stepEnv(j job, home string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + home,
		"LANG=C.UTF-8",
		"CI=true",
		"GITBAY_REPO=" + j.Repo,
		"GITBAY_SHA=" + j.SHA,
		"GITBAY_REF=" + j.Ref,
		"GITBAY_JOB=" + j.Job,
	}
	// The server sends secrets only for a trusted build — a merge request
	// head from a fork arrives with none — so this loop is empty exactly
	// when it should be.
	for name, value := range j.Secrets {
		env = append(env, name+"="+value)
	}
	return env
}

// ssh runs one control command against the server and returns stdout.// ssh runs one control command against the server and returns stdout.
func (r *runner) ssh(stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command(toolpath.Look("ssh"), append(append(r.sshOpts, r.remote), args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String() + errOut.String(), err
	}
	return out.String(), nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// defaultWorkdir picks a build workspace that another local user cannot
// have created first.
//
// The default used to be <tmp>/gitbay-runner: a fixed name inside a
// world-writable directory, created with MkdirAll, which succeeds against
// an existing directory whoever owns it. On a shared host another user
// could have made it — or symlinked it — before the runner started, and
// this is the process that clones repositories and exports build secrets
// into step environments (go:S5445, #153).
//
// The user's cache directory is not world-writable and is per-user by
// construction. Falling back to tmp keeps a runner working where HOME is
// unset, and checkWorkdir refuses the unsafe cases there.
func defaultWorkdir() string {
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		return filepath.Join(cache, "gitbay-runner")
	}
	return filepath.Join(os.TempDir(), "gitbay-runner")
}

// checkWorkdir makes sure the workspace is a directory this user owns
// privately. MkdirAll is happy with one that already exists, so being
// able to create it proves nothing about who made it.
//
// A directory we own that is merely too permissive is tightened rather
// than refused: every runner before this one created its workspace 0755,
// so refusing would take the runner down on upgrade to fix a permission
// we are entitled to change. What cannot be repaired — a symlink, or
// something owned by someone else — is refused, because those are what an
// attacker leaves behind and neither is ours to correct.
func checkWorkdir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workdir %s is a symlink; point -workdir at a real directory", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("workdir %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("workdir %s is owned by uid %d, not this process's %d", dir, st.Uid, os.Getuid())
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		log.Printf("workdir %s was mode %04o; tightening to 0700 (builds and their secrets are this user's alone)", dir, perm)
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("tightening workdir %s: %w", dir, err)
		}
	}
	return nil
}
