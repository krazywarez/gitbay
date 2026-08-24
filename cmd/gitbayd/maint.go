package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
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
				b := duDir(dir)
				gcArgs := []string{"-C", dir, "gc", "--quiet"}
				if aggressive {
					gcArgs = append(gcArgs, "--aggressive")
				}
				if out, err := exec.Command("git", gcArgs...).CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "%s: gc failed: %v\n%s", r.Path(), err, out)
					continue
				}
				a := duDir(dir)
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

func statsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "instance statistics: counts and per-repository disk usage",
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

			counts, err := st.InstanceCounts()
			if err != nil {
				return err
			}
			repos, err := st.ListAllRepos()
			if err != nil {
				return err
			}
			type repoDisk struct {
				Path  string `json:"path"`
				Bytes int64  `json:"bytes"`
			}
			var disks []repoDisk
			var totalDisk int64
			for _, r := range repos {
				b := duDir(control.RepoDir(cfg.Server.Root, r.OwnerName, r.Name))
				disks = append(disks, repoDisk{r.Path(), b})
				totalDisk += b
			}
			var dbBytes int64
			if fi, err := os.Stat(cfg.Server.Root + "/gitbay.db"); err == nil {
				dbBytes = fi.Size()
			}

			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"counts": counts, "db_bytes": dbBytes,
					"repo_bytes": totalDisk, "repos": disks,
				})
			}
			fmt.Printf("users %d · orgs %d · repos %d · issues %d (%d open) · MRs %d (%d open)\n",
				counts.Users, counts.Orgs, counts.Repos,
				counts.Issues, counts.OpenIssues, counts.MRs, counts.OpenMRs)
			fmt.Printf("database %s · repositories %s\n\n", human(dbBytes), human(totalDisk))
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			for _, d := range disks {
				fmt.Fprintf(w, "%s\t%s\n", d.Path, human(d.Bytes))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// duDir sums file sizes under dir; errors count as zero.
func duDir(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
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
