package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/protocol"
)

func migrateCmd() *cobra.Command {
	var from string
	var fromPort int
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "move your account here from another gitbay instance: gitbay migrate --from <host>",
		Long: `Migrate authenticates to the source instance with your own SSH key,
exports your account bundle (profile, emails, repos with settings,
issues, MRs, comments), replays it on this instance, then mirrors each
repository's git data client-side: clone from the source, push here.
Keys are never transferred and emails arrive unverified — trust is
per-instance. Re-running resumes; nothing imports twice.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			os.Exit(runMigrate(from, fromPort))
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source instance host (required)")
	cmd.Flags().IntVar(&fromPort, "from-port", 22, "source instance SSH port")
	return cmd
}

func sourceSSH(host string, port int, extra ...string) *exec.Cmd {
	args := []string{}
	if port != 0 && port != 22 {
		args = append(args, "-p", fmt.Sprint(port))
	}
	args = append(args, "git@"+host, "--")
	args = append(args, extra...)
	return exec.Command("ssh", args...)
}

func runMigrate(from string, fromPort int) int {
	if from == "" {
		fmt.Fprintln(os.Stderr, "gitbay: --from <host> is required")
		return protocol.ExitUsage
	}
	t, err := resolveTarget()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	if t.inst.Host == from {
		fmt.Fprintln(os.Stderr, "gitbay: --from is this instance; migrate runs on the TARGET with the source in --from")
		return protocol.ExitUsage
	}

	fmt.Fprintf(os.Stderr, "exporting account bundle from %s ...\n", from)
	exp := sourceSSH(from, fromPort, "account", "export")
	var bundleBuf, expErr bytes.Buffer
	exp.Stdout, exp.Stderr = &bundleBuf, &expErr
	if err := exp.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "gitbay: export from %s failed: %v\n%s", from, err, expErr.String())
		return protocol.ExitProtocol
	}

	var b struct {
		Username string `json:"username"`
		Repos    []struct {
			Name string `json:"name"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(bundleBuf.Bytes(), &b); err != nil {
		fmt.Fprintf(os.Stderr, "gitbay: bundle from %s does not parse: %v\n", from, err)
		return protocol.ExitProtocol
	}

	fmt.Fprintf(os.Stderr, "replaying metadata on %s ...\n", t.inst.Host)
	if code := runSSH(t, []string{"account", "import-bundle", "--source", from}, &bundleBuf); code != 0 {
		return code
	}

	// Git data travels client-side: your key authenticates both ends.
	for _, r := range b.Repos {
		repoPath := b.Username + "/" + r.Name
		fmt.Fprintf(os.Stderr, "mirroring %s ...\n", repoPath)
		tmp, err := os.MkdirTemp("", "gitbay-migrate-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "gitbay:", err)
			return protocol.ExitFailure
		}
		srcURL := fmt.Sprintf("ssh://git@%s/%s.git", from, repoPath)
		if fromPort != 0 && fromPort != 22 {
			srcURL = fmt.Sprintf("ssh://git@%s:%d/%s.git", from, fromPort, repoPath)
		}
		clone := exec.Command("git", "clone", "--quiet", "--mirror", srcURL, tmp+"/r")
		clone.Stderr = os.Stderr
		if err := clone.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "gitbay: cloning %s failed; fix and re-run migrate (it resumes)\n", repoPath)
			os.RemoveAll(tmp)
			return protocol.ExitFailure
		}
		push := exec.Command("git", "-C", tmp+"/r", "push", "--quiet", t.inst.CloneURL(repoPath),
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		if len(t.inst.SSHOptions) > 0 {
			push.Env = append(os.Environ(), "GIT_SSH_COMMAND=ssh "+strings.Join(quoteAll(t.inst.SSHOptions), " "))
		}
		push.Stderr = os.Stderr
		err = push.Run()
		os.RemoveAll(tmp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gitbay: pushing %s failed; fix and re-run migrate (it resumes)\n", repoPath)
			return protocol.ExitFailure
		}
	}
	fmt.Fprintf(os.Stderr, "migration complete: %d repositories. Re-register extra keys, re-verify emails,\nand re-apply any deferred policies printed above.\n", len(b.Repos))
	return protocol.ExitOK
}
