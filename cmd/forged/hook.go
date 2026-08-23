package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/krazywarez/forge/internal/gitutil"
	"github.com/krazywarez/forge/internal/hookd"
	"github.com/krazywarez/forge/internal/policy"
)

// collectIncomingCommits lists the commits this push introduces and reads
// their raw objects. It runs in the hook process, which inherits git's
// quarantine environment — the daemon cannot see these objects yet.
func collectIncomingCommits(updates []policy.RefUpdate) (hookd.CommitsPayload, error) {
	seen := map[string]bool{}
	var payload hookd.CommitsPayload
	for _, u := range updates {
		if u.IsDelete {
			continue
		}
		// Everything reachable from the new tip that no existing ref has.
		out, err := exec.Command("git", "rev-list", u.New, "--not", "--all").Output()
		if err != nil {
			return payload, fmt.Errorf("rev-list %s: %w", u.New, err)
		}
		for _, sha := range strings.Fields(string(out)) {
			if seen[sha] {
				continue
			}
			seen[sha] = true
			raw, err := exec.Command("git", "cat-file", "commit", sha).Output()
			if err != nil {
				return payload, fmt.Errorf("cat-file %s: %w", sha, err)
			}
			payload.Commits = append(payload.Commits, hookd.RawCommit{SHA: sha, Raw: raw})
		}
	}
	return payload, nil
}

// hookCmd runs inside a git hook. It computes git facts here — the hook
// process inherits git's quarantine environment, so incoming objects are
// visible — and asks the daemon for a policy decision over the unix socket.
func hookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "hook <pre-receive|post-receive>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sock := os.Getenv(hookd.EnvSocket)
			repoID, err1 := strconv.ParseInt(os.Getenv(hookd.EnvRepoID), 10, 64)
			userID, err2 := strconv.ParseInt(os.Getenv(hookd.EnvUserID), 10, 64)
			if sock == "" || err1 != nil || err2 != nil {
				return fmt.Errorf("missing FORGE_* environment; this command only runs as a git hook")
			}

			var updates []policy.RefUpdate
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) != 3 {
					continue
				}
				u := policy.RefUpdate{Old: fields[0], New: fields[1], Ref: fields[2]}
				u.IsDelete = gitutil.ZeroSHA(u.New)
				if !u.IsDelete && !gitutil.ZeroSHA(u.Old) {
					anc, err := gitutil.IsAncestor(".", u.Old, u.New)
					if err != nil {
						return fmt.Errorf("checking ancestry for %s: %w", u.Ref, err)
					}
					u.IsForce = !anc
				}
				updates = append(updates, u)
			}
			if err := scanner.Err(); err != nil {
				return err
			}

			resp, err := hookd.Ask(sock, hookd.Request{
				Hook:    args[0],
				RepoID:  repoID,
				UserID:  userID,
				Updates: updates,
			}, func() (hookd.CommitsPayload, error) {
				return collectIncomingCommits(updates)
			})
			if err != nil {
				return fmt.Errorf("forge daemon unreachable: %w", err)
			}
			if !resp.Allow {
				fmt.Fprintln(os.Stderr, resp.Message)
				os.Exit(1)
			}
			return nil
		},
	}
}
