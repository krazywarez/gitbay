package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

func gcCmd() *cobra.Command {
	var repoPath string
	var aggressive bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "repack and prune repositories (git gc)",
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
				if out, err := exec.Command("git", gcArgs...).CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "%s: gc failed: %v\n%s", r.Path(), err, out)
					continue
				}
				a := gitutil.DirSize(dir)
				before, after = before+b, after+a
				fmt.Printf("%s\t%s -> %s\n", r.Path(), human(b), human(a))
			}
			fmt.Printf("total\t%s -> %s (freed %s)\n", human(before), human(after), human(before-after))
			return nil
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "one repository (owner/name) instead of all")
	cmd.Flags().BoolVar(&aggressive, "aggressive", false, "more thorough repack (slow; rarely needed)")
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
