package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

func withUser(use, short string, run func(st *store.Store, u store.User) error) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <username>",
		Short: short,
		Args:  cobra.ExactArgs(1),
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
			u, err := st.UserByUsername(args[0])
			if err != nil {
				return fmt.Errorf("no user %q", args[0])
			}
			return run(st, u)
		},
	}
}

func adminUserDisableCmd() *cobra.Command {
	return withUser("disable", "suspend an account: keys and sessions refused until re-enabled",
		func(st *store.Store, u store.User) error {
			if err := st.SetUserDisabled(u.ID, true); err != nil {
				return err
			}
			st.Audit(0, "admin user.disabled", map[string]any{"user": u.Username})
			fmt.Printf("disabled %s: SSH, web sessions, and API tokens are refused; nothing was deleted\n", u.Username)
			return nil
		})
}

func adminUserDeleteCmd() *cobra.Command {
	var yes bool
	cmd := withUser("delete", "delete an account that anchors nothing (keys, emails, and sessions go with it)",
		func(st *store.Store, u store.User) error {
			if !yes {
				return fmt.Errorf("deletion is permanent; pass --yes")
			}
			if err := st.DeleteUser(u.ID); err != nil {
				return err
			}
			st.Audit(0, "admin user.deleted", map[string]any{"user": u.Username})
			fmt.Printf("deleted %s\n", u.Username)
			return nil
		})
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm permanent deletion")
	return cmd
}

func adminUserEnableCmd() *cobra.Command {
	return withUser("enable", "restore a suspended account",
		func(st *store.Store, u store.User) error {
			if err := st.SetUserDisabled(u.ID, false); err != nil {
				return err
			}
			st.Audit(0, "admin user.enabled", map[string]any{"user": u.Username})
			fmt.Printf("enabled %s\n", u.Username)
			return nil
		})
}

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

func adminAuditCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "print the security audit log, newest first",
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
			entries, err := st.AuditEntries(limit)
			if err != nil {
				return err
			}
			for _, e := range entries {
				actor := e.Actor
				if actor == "" {
					actor = "-"
				}
				fmt.Printf("%s\t%s\t%s\t%s\n", e.CreatedAt, actor, e.Action, e.Data)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "entries to print")
	return cmd
}
