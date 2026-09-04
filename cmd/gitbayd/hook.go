package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/hookd"
	"gitbay.org/gitbay/internal/policy"
)

// incomingSHAs lists the commits this push introduces, in order, without
// duplicates. It runs in the hook process, which inherits git's quarantine
// environment — the daemon cannot see these objects yet.
func incomingSHAs(updates []policy.RefUpdate) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, u := range updates {
		if u.IsDelete {
			continue
		}
		// Everything reachable from the new tip that no existing ref has.
		raw, err := exec.Command("git", "rev-list", u.New, "--not", "--all").Output()
		if err != nil {
			return nil, fmt.Errorf("rev-list %s: %w", u.New, err)
		}
		for _, sha := range strings.Fields(string(raw)) {
			if seen[sha] {
				continue
			}
			seen[sha] = true
			out = append(out, sha)
		}
	}
	return out, nil
}

// streamIncomingCommits reads every incoming commit through one
// `cat-file --batch` and hands each to emit as it arrives.
//
// This used to fork a cat-file per commit and build the whole payload in
// memory before sending it: a 50k-commit first push to a protected branch
// forked 50k processes and held 50k raw commits at once (#100). One
// subprocess now serves the whole push, and nothing is accumulated.
func streamIncomingCommits(updates []policy.RefUpdate, emit func(hookd.RawCommit) error) error {
	shas, err := incomingSHAs(updates)
	if err != nil {
		return err
	}
	if len(shas) == 0 {
		return nil
	}
	// Cancelling kills git on an early return. Without it, bailing out
	// part-way through a large push leaves git blocked writing into a
	// pipe nobody is reading and Wait blocked on git.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("cat-file --batch: %w", err)
	}
	defer cmd.Wait() // second: reaps the process cancel just signalled
	defer cancel()
	// Feeding stdin from another goroutine: the pipe buffer is smaller
	// than 50k object names, so writing them all before reading would
	// block against a git that is blocked writing its own output.
	writeErr := make(chan error, 1)
	go func() {
		defer stdin.Close()
		w := bufio.NewWriter(stdin)
		for _, sha := range shas {
			if _, err := fmt.Fprintln(w, sha); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- w.Flush()
	}()

	r := bufio.NewReader(stdout)
	for range shas {
		// Each record is "<oid> <type> <size>\n", then size bytes, then
		// a newline.
		header, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("cat-file --batch: %w", err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return fmt.Errorf("cat-file --batch: unexpected %q", strings.TrimSpace(header))
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return fmt.Errorf("cat-file --batch: bad size in %q", strings.TrimSpace(header))
		}
		raw := make([]byte, size)
		if _, err := io.ReadFull(r, raw); err != nil {
			return fmt.Errorf("cat-file %s: %w", fields[0], err)
		}
		if _, err := r.Discard(1); err != nil {
			return fmt.Errorf("cat-file %s: %w", fields[0], err)
		}
		if err := emit(hookd.RawCommit{SHA: fields[0], Raw: raw}); err != nil {
			return err
		}
	}
	if err := <-writeErr; err != nil {
		return fmt.Errorf("cat-file --batch: %w", err)
	}
	return nil
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
				return fmt.Errorf("missing GITBAY_* environment; this command only runs as a git hook")
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
			}, func(emit func(hookd.RawCommit) error) error {
				return streamIncomingCommits(updates, emit)
			})
			if err != nil {
				return fmt.Errorf("gitbay daemon unreachable: %w", err)
			}
			if !resp.Allow {
				fmt.Fprintln(os.Stderr, resp.Message)
				os.Exit(1)
			}
			return nil
		},
	}
}
