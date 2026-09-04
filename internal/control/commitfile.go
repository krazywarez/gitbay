package control

import (
	"fmt"
	"io"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{
		Path:    []string{"repo", "commit-file"},
		Summary: "write a file and commit it",
		Usage: "repo commit-file <owner/name> <path> " +
			"--ref <branch> [--message <m>] [--file -]",
		ReadsStdin: true,
		Run:        runCommitFile,
	})
}

// maxCommitFileBytes bounds one edit. Large content belongs in a push,
// not a single-file commit over the control plane.
const maxCommitFileBytes = 1 << 20

// runCommitFile commits one file's contents to a branch. It exists so the
// capability is reachable from every surface: the web's editor dispatches
// this rather than calling git itself, which is what kept editing off the
// CLI and the API.
//
// Commits made here are unsigned, because the server is authoring them.
// A repository that requires verified signatures therefore refuses the
// command rather than writing a commit its own policy would reject.
func runCommitFile(c *Ctx, args []string) int {
	const usage = "repo commit-file <owner/name> <path> --ref <branch> [--message <m>] [--file -]"
	f, err := parseFlags(args, flagSpec{Values: []string{"--ref", "--message", "--file"}, MaxPos: -1, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	rest := f.Pos
	ref, message, file := f.Value("--ref"), f.Value("--message"), f.Value("--file")
	if len(rest) != 2 || ref == "" {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	repo, code := resolveRepo(c, rest[0], policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	filePath, ok := cleanRepoPath(rest[1])
	if !ok || filePath == "" {
		return c.fail(protocol.ExitUsage, "path must stay inside the repository")
	}
	// The server authors this commit, so it cannot sign it.
	if repo.Settings.RequireSignedCommits {
		return c.fail(protocol.ExitDenied,
			"%s requires signed commits; this writes an unsigned one — push a signed commit instead",
			repo.Path())
	}
	// A commit carries an identity, and an unverified address is not one.
	email, err := c.Store.PrimaryVerifiedEmail(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if email == "" {
		return c.fail(protocol.ExitDenied,
			"commits carry your identity: your account needs a verified primary email")
	}

	var content []byte
	if file != "" {
		if file != "-" {
			return c.fail(protocol.ExitUsage, "--file only supports - (stdin)")
		}
		content, err = io.ReadAll(io.LimitReader(c.Stdin, maxCommitFileBytes))
		if err != nil {
			return c.fail(protocol.ExitFailure, "reading content: %v", err)
		}
	}
	if message = strings.TrimSpace(message); message == "" {
		message = "edit " + filePath
	}

	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	sha, err := gitutil.CommitFileChange(dir, ref, filePath, content,
		c.User.Username, email, message)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.MarkMirrorsDirty(repo.ID, "push")

	d := struct {
		Path string `json:"path"`
		Ref  string `json:"ref"`
		File string `json:"file"`
		SHA  string `json:"sha"`
	}{repo.Path(), ref, filePath, sha}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "committed %s on %s: %.10s\n", filePath, ref, sha)
	})
}
