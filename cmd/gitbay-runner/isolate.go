package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/toolpath"
)

// Isolation modes. podman runs a job's steps in a container; none runs
// them on the host as the runner's user, which is what the runner did
// before #144 and what a private instance may still choose.
const (
	isolationPodman = "podman"
	isolationNone   = "none"
)

// Images are provisioned, never pulled at build time.
//
// The service runs with RestrictSUIDSGID=yes, so podman cannot unpack an
// image layer containing a setuid or setgid file — which is almost every
// distribution image (chage, passwd, su). A pull from inside the service
// fails deep in the unpack with "operation not permitted" on some file
// nobody has heard of.
//
// Keeping that flag and provisioning images deliberately is the better
// half of the trade, and not only because it is one less hardening
// concession: on an instance where anyone can push a ci.yml, `image:`
// would otherwise be "fetch and run this arbitrary image from the
// internet". An operator pulls or builds what they will allow, and a
// build chooses among those. --pull=never makes that explicit rather
// than leaving it to whether a pull happens to fail (#144).

// checkIsolation fails the runner at start-up rather than at the first
// build, and refuses anything it does not recognise. There is no silent
// fallback from podman to the host: dropping isolation without saying so
// is the failure mode this whole change exists to prevent (#144).
func (r *runner) checkIsolation() error {
	switch r.isolation {
	case isolationNone:
		log.Printf("WARNING: -isolation none: build steps run on this host as %s, "+
			"with no container. Only do this where every repository is trusted.", currentUser())
		return nil
	case isolationPodman:
		// Configuration before environment: a missing -image is the
		// operator's to fix whatever the host looks like, and saying so
		// first means the message does not depend on which machine this
		// is.
		//
		// No built-in default image: one that is not provisioned here
		// would fail every build with --pull=never, and guessing which
		// image an operator has is worse than asking.
		if r.image == "" {
			return fmt.Errorf("-isolation podman needs -image <ref>, the image a job runs in " +
				"when it names none. It must already be present on this host: " +
				"pull or build it as the runner's user, since the service cannot unpack images")
		}
		bin := toolpath.Look("podman")
		out, err := exec.Command(bin, "info", "--format", "{{.Host.Security.Rootless}}").CombinedOutput()
		if err != nil {
			return fmt.Errorf("podman is required by -isolation podman but does not work here: %v\n%s\n"+
				"prepare the host with deploy/runner-podman-setup.sh, or pass -isolation none "+
				"if every repository on this instance is trusted", err, strings.TrimSpace(string(out)))
		}
		log.Printf("isolation: podman (rootless=%s), default image %s, images must be provisioned locally",
			strings.TrimSpace(string(out)), r.image)
		return nil
	default:
		return fmt.Errorf("unknown -isolation %q: podman or none", r.isolation)
	}
}

// runSteps executes a job's steps and reports whether all succeeded. The
// clone has already happened, outside any container and with the runner's
// key: the container never sees GIT_SSH_COMMAND, the key, or the runner's
// environment — it gets the workspace and nothing else.
type stepRunner func(cmd *exec.Cmd, deadline time.Time) (bool, string)

func (r *runner) runSteps(j job, dir string, env []string, sink io.Writer, deadline time.Time, runStep stepRunner) bool {
	if r.isolation == isolationNone {
		for _, step := range j.Steps {
			fmt.Fprintf(sink, "$ %s\n", step)
			cmd := exec.Command(toolpath.Look("sh"), "-c", step)
			cmd.Dir, cmd.Env = dir, env
			cmd.Stdout, cmd.Stderr = sink, sink
			if ok, why := runStep(cmd, deadline); !ok {
				fmt.Fprintf(sink, "%s\n", why)
				return false
			}
		}
		return true
	}
	return r.runStepsPodman(j, dir, env, sink, deadline, runStep)
}

// runStepsPodman starts one container for the whole job and runs each
// step in it with `podman exec`. One container per job, not per step,
// because steps share state — a build step writes what a test step reads
// — and per-step containers would break that.
func (r *runner) runStepsPodman(j job, dir string, env []string, sink io.Writer, deadline time.Time, runStep stepRunner) bool {
	podman := toolpath.Look("podman")
	image := j.Image
	if image == "" {
		image = r.image
	}

	// Secrets must not reach argv: /proc is world-readable, and this
	// codebase keeps them on stdin or in files everywhere else. An env
	// file outside the workspace holds them instead — outside because the
	// workspace is bind mounted, and a file of secrets sitting in the
	// checkout is one `cat` from a build's own log.
	envFile := filepath.Join(r.workdir, fmt.Sprintf("env-%d", j.ID))
	if err := writeEnvFile(envFile, env); err != nil {
		fmt.Fprintf(sink, "preparing the build environment: %v\n", err)
		return false
	}
	defer os.Remove(envFile)

	name := fmt.Sprintf("gitbay-build-%d", j.ID)
	// --rm so a container cannot outlive its build; the explicit rm below
	// covers the case where the daemon-less run itself fails.
	start := exec.Command(podman, append(r.podmanGlobal(), "run", "--detach", "--rm",
		"--pull=never",
		"--name", name,
		"--env-file", envFile,
		"--volume", dir+":/workspace:rw",
		// The build home holds the tool caches (Go modules, the sonar
		// scanner) that must outlive a build; HOME in env points at it.
		// Mounted at the same path so HOME resolves identically with and
		// without a container. Only this directory — never the workdir
		// above it, which holds other builds' workspaces.
		"--volume", envHome(env)+":"+envHome(env)+":rw",
		"--workdir", "/workspace",
		"--entrypoint", "sh",
		image, "-c", "sleep infinity")...)
	start.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.podmanHome()}
	if out, err := start.CombinedOutput(); err != nil {
		// A missing image lands here, and it is the common case worth
		// explaining: this runner never pulls, so an image it does not
		// have is an operator's job to provision, not a transient error
		// to retry.
		msg := strings.TrimSpace(string(out))
		fmt.Fprintf(sink, "starting the build container from %s failed:\n%s\n", image, msg)
		if strings.Contains(msg, "no such image") || strings.Contains(msg, "image not known") ||
			strings.Contains(msg, "unable to find") {
			fmt.Fprintf(sink, "\nThis runner does not pull images. Ask an operator to provision %s "+
				"on the runner host (podman pull, or podman build) before a job names it.\n", image)
		}
		return false
	}
	defer exec.Command(podman, append(r.podmanGlobal(), "rm", "--force", name)...).Run()

	for _, step := range j.Steps {
		fmt.Fprintf(sink, "$ %s\n", step)
		cmd := exec.Command(podman, append(r.podmanGlobal(), "exec", "--workdir", "/workspace", name, "sh", "-c", step)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.podmanHome()}
		cmd.Stdout, cmd.Stderr = sink, sink
		if ok, why := runStep(cmd, deadline); !ok {
			fmt.Fprintf(sink, "%s\n", why)
			return false
		}
	}
	return true
}

// podmanGlobal are the flags every podman invocation needs, before the
// subcommand.
//
// The cgroup manager is cgroupfs, not systemd: the runner is a *system*
// service, so there is no user session and no user@<uid>.service slice
// for podman to create a scope under. With the systemd manager crun
// fails with "create directory .../libpod-<id>.scope/container: No such
// file or directory". The service's own cgroup is delegated
// (Delegate=yes in the drop-in), which is what cgroupfs needs (#144).
func (r *runner) podmanGlobal() []string {
	// Storage paths are left to podman. They are recorded in its
	// database at first use, so passing --root or --runroot later fails
	// with "database configuration mismatch" — as does introducing an
	// XDG_RUNTIME_DIR the database was not initialised with. Changing
	// either means `podman system reset` and rebuilding the images.
	return []string{"--cgroup-manager=cgroupfs"}
}

// env_home returns the HOME the step environment carries.
func envHome(env []string) string {
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			return strings.TrimPrefix(e, "HOME=")
		}
	}
	return ""
}

// podmanHome is where podman keeps its own storage: the runner's home,
// not a build's. The container store is the runner's business, and a
// build never sees this path.
func (r *runner) podmanHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/var/lib/gitbay-runner"
}

// writeEnvFile writes KEY=VALUE lines for podman --env-file, readable
// only by this user. Values containing a newline are refused rather than
// silently truncated: the format has no escape for one, and a secret that
// half-arrives is worse than a failed build.
func writeEnvFile(path string, env []string) error {
	var b strings.Builder
	for _, e := range env {
		if strings.ContainsAny(e, "\n\r") {
			name, _, _ := strings.Cut(e, "=")
			return fmt.Errorf("%s contains a newline, which an env file cannot carry", name)
		}
		b.WriteString(e)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return fmt.Sprintf("uid %d", os.Getuid())
}
