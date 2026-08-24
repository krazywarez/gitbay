package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
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
