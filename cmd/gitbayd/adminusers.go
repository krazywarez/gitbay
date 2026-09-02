package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
)

// adminMigrateCommitRefsCmd is a one-shot backfill: legacy commit-reference
// comments (author-attributed, bare sha) become system messages with a
// linked sha. Idempotent.
func adminMigrateCommitRefsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-commit-refs",
		Short: "convert legacy commit-reference comments into linked system messages",
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
			n, err := st.MigrateCommitRefComments()
			if err != nil {
				return err
			}
			fmt.Printf("converted %d commit-reference comment(s) to system messages\n", n)
			return nil
		},
	}
}

// adminBackfillActivityCmd walks every repo's default branch and records
// commit activity for commits authored by verified addresses. Idempotent:
// (repo, sha) dedup makes re-runs free. Imported history keeps its real
// author dates, so migrated repos light up their actual timeline.
func adminBackfillActivityCmd() *cobra.Command {
	var perRepo int
	cmd := &cobra.Command{
		Use:   "backfill-activity",
		Short: "record commit activity for existing default-branch history",
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
			repos, err := st.ListAllRepos()
			if err != nil {
				return err
			}
			total := 0
			for _, r := range repos {
				dir := control.RepoDir(cfg.Server.Root, r.OwnerName, r.Name)
				authors, err := gitutil.RevListAuthors(dir, "", r.DefaultBranch, perRepo)
				if err != nil {
					continue // empty repo or missing branch
				}
				n := 0
				for _, a := range authors {
					if uid, ok := st.UserIDByVerifiedEmail(a.Email); ok {
						if st.RecordCommitActivity(r.ID, a.SHA, uid, a.Day) {
							n++
						}
					}
				}
				total += n
				fmt.Printf("%s\t%d attributed\n", r.Path(), n)
			}
			fmt.Printf("total\t%d commits recorded\n", total)
			return nil
		},
	}
	cmd.Flags().IntVar(&perRepo, "per-repo", 20000, "max commits walked per repository")
	return cmd
}
