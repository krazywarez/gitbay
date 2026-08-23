package control

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/krazywarez/forge/internal/gitutil"
	"github.com/krazywarez/forge/internal/policy"
	"github.com/krazywarez/forge/internal/protocol"
)

func init() {
	register(Command{Path: []string{"repo", "import"},
		Summary:    "server-side mirror of a foreign repository: repo import <owner/name> --from <url> [--private] [--token-stdin]",
		ReadsStdin: true, Run: runRepoImport})
}

// askpassScript answers git's credential prompts from the environment, so
// the token never appears on a command line or in a URL. Username prompts
// get a placeholder (GitHub and GitLab ignore it for token auth).
const askpassScript = `#!/bin/sh
case "$1" in
  Username*) echo "x-access-token" ;;
  *)         echo "${FORGE_IMPORT_TOKEN}" ;;
esac
`

func runRepoImport(c *Ctx, args []string) int {
	var path, from string
	private := false
	tokenStdin := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--from requires a URL")
			}
			from = args[i+1]
			i++
		case "--private":
			private = true
		case "--token-stdin":
			tokenStdin = true
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
			path = args[i]
		}
	}
	if path == "" || from == "" {
		return c.fail(protocol.ExitUsage, "usage: repo import <owner/name> --from <url> [--private] [--token-stdin]")
	}
	owner, name, ok := strings.Cut(path, "/")
	if !ok || owner != c.User.Username {
		return c.fail(protocol.ExitDenied, "imports land under your own account: %s/<name>", c.User.Username)
	}
	if err := policy.ValidateName(name); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}

	// Scheme allowlist. file:// (and anything else local) would read the
	// server's filesystem; ssh:// would use the server's own keys.
	switch {
	case strings.HasPrefix(from, "https://"), strings.HasPrefix(from, "http://"), strings.HasPrefix(from, "git://"):
	default:
		return c.fail(protocol.ExitUsage, "import supports https://, http://, and git:// URLs only")
	}
	if strings.ContainsAny(from, "@") {
		// Credentials belong on stdin, not in the URL where they would
		// land in process listings and logs.
		return c.fail(protocol.ExitUsage, "do not embed credentials in the URL; use --token-stdin")
	}

	// The token is read from stdin and handed to git via GIT_ASKPASS and
	// the environment — never argv, never the database, never a log line.
	var env []string
	if tokenStdin {
		token, err := bufio.NewReader(io.LimitReader(c.Stdin, 4096)).ReadString('\n')
		if err != nil && err != io.EOF {
			return c.fail(protocol.ExitFailure, "reading token: %v", err)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return c.fail(protocol.ExitUsage, "--token-stdin given but stdin held no token")
		}
		askpass := filepath.Join(c.Cfg.Server.Root, "askpass.sh")
		if err := os.WriteFile(askpass, []byte(askpassScript), 0o700); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		env = []string{
			"GIT_ASKPASS=" + askpass,
			"FORGE_IMPORT_TOKEN=" + token,
			"GIT_TERMINAL_PROMPT=0",
		}
	} else {
		env = []string{"GIT_TERMINAL_PROMPT=0"}
	}

	visibility := "public"
	if private {
		visibility = "private"
	}
	id, err := c.Store.CreateRepo("user", c.User.ID, name, visibility)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	dir := RepoDir(c.Cfg.Server.Root, owner, name)
	cleanup := func() {
		c.Store.DeleteRepo(id)
		os.RemoveAll(dir)
	}
	if err := gitutil.InitBare(dir, "main", HooksDir(c.Cfg.Server.Root)); err != nil {
		cleanup()
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	timeout := time.Duration(c.Cfg.Limits.CloneTimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Fprintf(c.Stderr, "importing %s into %s ...\n", from, path)
	if err := gitutil.FetchMirror(ctx, dir, from, c.Stderr, env); err != nil {
		cleanup()
		return c.fail(protocol.ExitFailure, "import failed: %v", err)
	}

	branch, err := gitutil.RemoteDefaultBranch(ctx, from, env)
	if err != nil {
		branch = "main" // remote gone quiet after the fetch; keep the default
	}
	if _, rerr := gitutil.ResolveRef(dir, "refs/heads/"+branch); rerr == nil {
		gitutil.SetHead(dir, branch)
		c.Store.UpdateDefaultBranch(id, branch)
	}

	c.Store.RecordEvent(id, c.User.ID, "repo.imported", fmt.Sprintf(`{"from":%q}`, from))
	type out struct {
		Path          string `json:"path"`
		Visibility    string `json:"visibility"`
		DefaultBranch string `json:"default_branch"`
	}
	d := out{path, visibility, branch}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "imported %s (%s, default %s)\nnote: git data only — issues and pull requests do not transfer\n",
			d.Path, d.Visibility, d.DefaultBranch)
	})
}
