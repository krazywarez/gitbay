package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/buildinfo"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the commit this binary was built from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(buildinfo.String())
			return nil
		},
	}
}

// logBuild announces the running build. An unidentified one is a warning
// rather than a fact: it was built from a tree that was never committed, so
// the source it came from no longer exists anywhere.
func logBuild() {
	if !buildinfo.Identified() {
		slog.Warn("gitbayd starting from an uncommitted build", "commit", buildinfo.String())
		return
	}
	slog.Info("gitbayd starting", "commit", buildinfo.String())
}

// warnIfUnmerged says so when the running build is not on the default branch
// of the repository this instance develops itself in. Such a build serves
// perfectly well, which is exactly why it can sit unnoticed for days.
//
// Silent unless server.source_repo is set, since an instance that does not
// host its own source has nothing to check against.
func warnIfUnmerged(cfg config.Config) {
	repo := cfg.Server.SourceRepo
	if repo == "" || !buildinfo.Identified() {
		return
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		slog.Warn("server.source_repo is not owner/name; skipping the build check", "source_repo", repo)
		return
	}
	// HEAD in a bare repository is the default branch, so there is no branch
	// name to resolve or configure.
	dir := control.RepoDir(cfg.Server.Root, owner, name)
	onBranch, err := gitutil.IsAncestor(dir, buildinfo.String(), "HEAD")
	if err != nil {
		// An unpushed commit lands here too, and is worth the same warning:
		// it cannot be checked, so it cannot be vouched for.
		slog.Warn("cannot check this build against the source repository",
			"commit", buildinfo.String(), "repo", repo, "err", err)
		return
	}
	if !onBranch {
		slog.Warn("this build is not on the source repository's default branch",
			"commit", buildinfo.String(), "repo", repo)
	}
}
