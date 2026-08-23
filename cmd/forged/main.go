// forged is the forge server daemon. The same binary also runs in hook mode
// (invoked by git via core.hooksPath) and hosts the host-local admin commands.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/krazywarez/forge/internal/config"
	"github.com/krazywarez/forge/internal/store"
)

var configPath string

func main() {
	root := &cobra.Command{
		Use:           "forged",
		Short:         "forge server daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", "/etc/forge/config.toml", "path to config file")

	root.AddCommand(
		checkConfigCmd(),
		serveCmd(),
		migrateCmd(),
		adminCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "forged:", err)
		os.Exit(1)
	}
}

func checkConfigCmd() *cobra.Command {
	var noHost bool
	cmd := &cobra.Command{
		Use:   "check-config",
		Short: "validate the configuration and exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if !noHost {
				if err := cfg.CheckHost(); err != nil {
					return err
				}
			}
			fmt.Println("config ok")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noHost, "no-host-checks", false, "skip host environment probes (port binding, paths)")
	return cmd
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "run the ssh, http, and git listeners",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not implemented (M1)")
		},
	}
}

func migrateCmd() *cobra.Command {
	var to int
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "apply schema migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.Server.Root + "/forge.db")
			if err != nil {
				return err
			}
			defer s.Close()
			if err := s.MigrateTo(to); err != nil {
				return err
			}
			v, err := s.Version()
			if err != nil {
				return err
			}
			fmt.Println("schema version", v)
			return nil
		},
	}
	cmd.Flags().IntVar(&to, "to", -1, "target schema version (-1 = latest)")
	return cmd
}

func adminCmd() *cobra.Command {
	admin := &cobra.Command{
		Use:   "admin",
		Short: "host-local administration",
	}
	notImplemented := func(use, short string) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			RunE: func(cmd *cobra.Command, args []string) error {
				return fmt.Errorf("not implemented (M1)")
			},
		}
	}
	admin.AddCommand(
		notImplemented("user", "create and manage users"),
		notImplemented("invite", "issue registration invites"),
		notImplemented("email", "verify user emails"),
		notImplemented("backup", "consistent backup: repos first, then database"),
		notImplemented("gc", "run git gc across repositories"),
		notImplemented("stats", "instance statistics"),
	)
	return admin
}
