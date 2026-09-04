package control

import (
	"fmt"
	"io"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{
		Path:     []string{"explore"},
		Summary:  "list public repositories",
		Usage:    "explore [--limit <n>] [--cursor <c>]",
		ReadOnly: true,
		Run:      runExplore,
	})
	register(Command{
		Path: []string{"repo", "download"},
		// Not "repo archive": that name is taken by the read-only flag,
		// and renaming it would break every script that sets it.
		Summary:  "write a tar.gz of a ref to stdout",
		Usage:    "repo download <owner/name> [--ref <r>] > repo.tar.gz",
		ReadOnly: true,
		Run:      runRepoDownload,
	})
}

// runExplore is the public listing the web serves at /explore. Without a
// command there was no way to browse what an instance hosts without
// already knowing a name to search for.
func runExplore(c *Ctx, args []string) int {
	rest, p, code := parsePageFlags(c, args, "explore", false)
	if code >= 0 {
		return code
	}
	if len(rest) != 0 {
		return c.fail(protocol.ExitUsage, "usage: explore [--limit <n>] [--cursor <c>]")
	}
	repos, err := c.Store.ListPublicRepos()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// Public means public, but an archived repo is still worth marking,
	// and the cursor is the path since the listing is ordered by it.
	type out struct {
		Path        string   `json:"path"`
		Description string   `json:"description,omitempty"`
		Archived    bool     `json:"archived,omitempty"`
		Topics      []string `json:"topics,omitempty"`
	}
	var ds []out
	for _, repo := range repos {
		if p.key != "" && repo.Path() <= p.key {
			continue
		}
		topics, _ := c.Store.ListTopics(repo.ID)
		ds = append(ds, out{
			Path:        repo.Path(),
			Description: gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)),
			Archived:    repo.Settings.Archived,
			Topics:      topics,
		})
		if p.limit > 0 && len(ds) > p.limit {
			break
		}
	}
	ds, next := trimPage(p, ds, "explore", func(o out) string { return o.Path })
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\n", d.Path, d.Description)
		}
	})
}

// runRepoDownload writes a gzipped tarball of a ref to stdout, the way
// release asset get writes an asset. The web's /archive route is the
// same bytes with a Content-Disposition on them.
func runRepoDownload(c *Ctx, args []string) int {
	const usage = "repo download <owner/name> [--ref <r>] > repo.tar.gz"
	f, err := parseFlags(args, flagSpec{Values: []string{"--ref"}, MaxPos: -1, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	rest, ref := f.Pos, f.Value("--ref")
	if len(rest) != 1 {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	repo, code := resolveRepo(c, rest[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if ref == "" {
		ref = repo.DefaultBranch
	}
	if _, err := gitutil.ResolveRef(dir, ref); err != nil {
		return c.fail(protocol.ExitNotFound, "no ref %q in %s", ref, repo.Path())
	}
	// The prefix git puts on every path inside the archive, so unpacking
	// lands in a named directory rather than the current one.
	prefix := repo.Name + "-" + ref
	if err := gitutil.Archive(dir, ref, prefix, c.Stdout); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return protocol.ExitOK
}
