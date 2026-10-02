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

	"gitbay.org/gitbay/internal/gitpin"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{Path: []string{"repo", "import"},
		Summary: "server-side mirror of a foreign repository",
		Usage:   "repo import <owner/name> --from <url> [--private] [--token-stdin]",
		Flags: []Flag{
			{"--from", "<url>", "the repository to import", ""},
			{"--private", "", "create it private", ""},
			{"--token-stdin", "", "read a credential token from stdin", ""},
		},
		Examples:   []string{"repo import krz/imported --from https://github.com/krz/old.git"},
		ReadsStdin: true, Run: runRepoImport})
}

// askpassScript answers git's credential prompts from the environment, so
// the token never appears on a command line or in a URL. Username prompts
// get a placeholder (GitHub and GitLab ignore it for token auth).
const askpassScript = `#!/bin/sh
case "$1" in
  Username*) echo "x-access-token" ;;
  *)         echo "${GITBAY_IMPORT_TOKEN}" ;;
esac
`

// importLookup resolves the hosts repo import and repo import-issues
// connect to; tests replace it.
var importLookup gitpin.Lookup = gitpin.LookupIP

func runRepoImport(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--from"}, Bools: []string{"--private", "--token-stdin"}, MaxPos: 1,
		Usage: "repo import <owner/name> --from <url> [--private] [--token-stdin]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, from, private, tokenStdin := f.pos(0), f.Value("--from"), f.Has("--private"), f.Has("--token-stdin")
	if path == "" || from == "" {
		return c.usage()
	}
	owner, name, ok := strings.Cut(path, "/")
	if !ok {
		return c.usage()
	}
	if err := policy.ValidateName(name); err != nil {
		return c.failInput(err)
	}
	// Same ownership rule as repo create: yourself, or an org you admin.
	ownerKind, ownerID := "user", c.User.ID
	if owner != c.User.Username {
		org, err := c.Store.OrgByName(owner)
		if err != nil {
			return c.fail(protocol.ExitDenied, "cannot import under %q: not you and not an organization you can see", owner)
		}
		role, err := c.Store.OrgRole(org.ID, c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if role != "admin" {
			return c.fail(protocol.ExitDenied, "only admins of %s can import repositories there", owner)
		}
		ownerKind, ownerID = "org", org.ID
	}
	if code := checkRepoQuota(c, ownerKind, ownerID); code >= 0 {
		return code
	}

	// http and https only. git:// has no equivalent of curl's resolve
	// list, so its connection cannot be held to a checked address;
	// file:// would read the server's filesystem, and ssh:// would use
	// the server's own keys.
	if !strings.HasPrefix(from, "https://") && !strings.HasPrefix(from, "http://") {
		return c.fail(protocol.ExitUsage, "import fetches over http:// and https:// only; use the repository's https:// URL")
	}
	if strings.ContainsAny(from, "@") {
		// Credentials belong on stdin, not in the URL where they would
		// land in process listings and logs.
		return c.fail(protocol.ExitUsage, "do not embed credentials in the URL; use --token-stdin")
	}
	if strings.ContainsAny(from, "?#") {
		// The URL is logged and recorded; a query could carry a token.
		return c.fail(protocol.ExitUsage, "use the plain clone URL, without a query or fragment; credentials go on stdin with --token-stdin, never in the URL")
	}

	// Resolve and check the host now and hold git to those addresses,
	// as mirror sync does (#298).
	timeout := time.Duration(c.Cfg.Limits.CloneTimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := gitpin.CheckGit(ctx); err != nil {
		return c.fail(protocol.ExitFailure, "import unavailable: %v", err)
	}
	remote, err := gitpin.Resolve(ctx, importLookup, from, c.Cfg.Webhooks.AllowLocal)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	// The token is read from stdin and handed to git via GIT_ASKPASS and
	// the environment — never argv, never the database, never a log line.
	env := gitpin.Env(c.Cfg.Server.Root)
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
		env = append(env, "GIT_ASKPASS="+askpass, "GITBAY_IMPORT_TOKEN="+token)
	}

	visibility := "public"
	if private {
		visibility = "private"
	}
	// The early check above fails fast; this one holds the lock across
	// the insert so a concurrent create cannot slip past the count.
	repoCreateMu.Lock()
	if code := checkRepoQuota(c, ownerKind, ownerID); code >= 0 {
		repoCreateMu.Unlock()
		return code
	}
	id, err := c.Store.CreateRepo(ownerKind, ownerID, name, visibility)
	repoCreateMu.Unlock()
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

	fmt.Fprintf(c.Stderr, "importing %s into %s ...\n", from, path)
	if err := gitutil.FetchMirror(ctx, dir, from, c.Stderr, remote.Args(), env); err != nil {
		cleanup()
		return c.fail(protocol.ExitFailure, "import failed: %v", err)
	}

	branch, err := gitutil.RemoteDefaultBranch(ctx, dir, from, remote.Args(), env)
	if err != nil {
		branch = "main" // remote gone quiet after the fetch; keep the default
	}
	if _, rerr := gitutil.ResolveRef(dir, "refs/heads/"+branch); rerr == nil {
		gitutil.SetHead(dir, branch)
		c.Store.UpdateDefaultBranch(id, branch)
	}
	c.Store.RequestSymbolIndex(id, false)

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
