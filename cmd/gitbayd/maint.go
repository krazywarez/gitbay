package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/lfs"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/toolpath"
)

func gcCmd() *cobra.Command {
	var repoPath string
	var aggressive, withLFS bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "repack and prune repositories (git gc); --lfs removes unreferenced LFS objects",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			var repos []store.Repo
			if repoPath != "" {
				r, err := st.RepoByPath(repoPath)
				if err != nil {
					return fmt.Errorf("no repository %q", repoPath)
				}
				repos = []store.Repo{r}
			} else if repos, err = st.ListAllRepos(); err != nil {
				return err
			}

			var before, after int64
			for _, r := range repos {
				dir := control.RepoDir(cfg.Server.Root, r.OwnerName, r.Name)
				b := gitutil.DirSize(dir)
				gcArgs := []string{"-C", dir, "gc", "--quiet"}
				if aggressive {
					gcArgs = append(gcArgs, "--aggressive")
				}
				if out, err := exec.Command(toolpath.Look("git"), gcArgs...).CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "%s: gc failed: %v\n%s", r.Path(), err, out)
					continue
				}
				a := gitutil.DirSize(dir)
				before, after = before+b, after+a
				fmt.Printf("%s\t%s -> %s\n", r.Path(), human(b), human(a))
			}
			fmt.Printf("total\t%s -> %s (freed %s)\n", human(before), human(after), human(before-after))
			if !withLFS {
				return nil
			}
			return gcLFS(cfg, st)
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "one repository (owner/name) instead of all")
	cmd.Flags().BoolVar(&aggressive, "aggressive", false, "more thorough repack (slow; rarely needed)")
	cmd.Flags().BoolVar(&withLFS, "lfs", false, "also remove LFS objects no repository references (older than a day)")
	return cmd
}

func human(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// lfsOrphanAge keeps an object uploaded ahead of the push that references
// it: git lfs uploads first and pushes second.
const lfsOrphanAge = 24 * time.Hour

// gcLFS removes LFS objects no repository's pointers name. Every
// repository is scanned, whatever --repo said: an object is shared across
// repositories by content, so only the whole set says it is unreferenced.
func gcLFS(cfg config.Config, st *store.Store) error {
	repos, err := st.ListAllRepos()
	if err != nil {
		return err
	}
	referenced := map[string]bool{}
	for _, r := range repos {
		oids, err := gitutil.LFSPointerOIDs(control.RepoDir(cfg.Server.Root, r.OwnerName, r.Name))
		if err != nil {
			return fmt.Errorf("%s: scanning for LFS pointers: %w", r.Path(), err)
		}
		for _, o := range oids {
			referenced[o] = true
		}
	}
	blobs := lfs.LocalStore{Root: lfs.RootFor(cfg.LFS.Root, cfg.Server.Root)}
	orphans, err := blobs.Orphans(referenced, lfsOrphanAge)
	if err != nil {
		return err
	}
	var freed int64
	for _, o := range orphans {
		if err := blobs.Delete(o.OID); err != nil {
			fmt.Fprintf(os.Stderr, "lfs %s: %v\n", o.OID, err)
			continue
		}
		freed += o.Size
	}
	fmt.Printf("lfs\t%d referenced, removed %d orphans (%s)\n", len(referenced), len(orphans), human(freed))
	return nil
}
