// forged is the forge server daemon. The same binary also runs in hook mode
// (invoked by git via core.hooksPath) and hosts the host-local admin commands.
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/krazywarez/forge/internal/config"
	"github.com/krazywarez/forge/internal/policy"
	"github.com/krazywarez/forge/internal/sshd"
	"github.com/krazywarez/forge/internal/store"
)

func openStore(cfg config.Config) (*store.Store, error) {
	s, err := store.Open(filepath.Join(cfg.Server.Root, "forge.db"))
	if err != nil {
		return nil, err
	}
	if err := s.MigrateUp(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

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
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			if cfg.SSH.Mode != "embedded" {
				return fmt.Errorf("ssh.mode = %q not implemented (M9)", cfg.SSH.Mode)
			}
			srv, err := sshd.New(cfg, st)
			if err != nil {
				return err
			}
			ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.SSH.Port)))
			if err != nil {
				return err
			}
			slog.Info("ssh listening", "addr", ln.Addr())
			return srv.Serve(ln)
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
				return fmt.Errorf("not implemented")
			},
		}
	}
	userCmd := &cobra.Command{Use: "user", Short: "manage users"}
	userCmd.AddCommand(adminUserCreateCmd())
	emailCmd := &cobra.Command{Use: "email", Short: "manage user emails"}
	emailCmd.AddCommand(adminEmailVerifyCmd())
	admin.AddCommand(
		userCmd,
		emailCmd,
		notImplemented("invite", "issue registration invites"),
		notImplemented("backup", "consistent backup: repos first, then database"),
		notImplemented("gc", "run git gc across repositories"),
		notImplemented("stats", "instance statistics"),
	)
	return admin
}

func adminUserCreateCmd() *cobra.Command {
	var keyPath, email string
	var verified, isAdmin bool
	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "create a user (host-local bootstrap; the only path in closed mode)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			if err := policy.ValidateOwnerName(username); err != nil {
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			uid, err := st.CreateUser(username, isAdmin)
			if err != nil {
				return err
			}
			if email != "" {
				verifiedBy := ""
				if verified {
					verifiedBy = "admin"
				}
				if err := st.AddEmail(uid, email, verifiedBy, true); err != nil {
					return err
				}
			}
			if keyPath != "" {
				raw, err := os.ReadFile(keyPath)
				if err != nil {
					return err
				}
				pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
				if err != nil {
					return fmt.Errorf("%s: not a public key in authorized_keys format: %w", keyPath, err)
				}
				fp := ssh.FingerprintSHA256(pub)
				if err := st.AddSSHKey(uid, fp, pub.Type(), pub.Marshal(), "full"); err != nil {
					return err
				}
				fmt.Println("key", fp)
			}
			fmt.Println("created user", username)
			return nil
		},
	}
	cmd.Flags().StringVar(&keyPath, "key", "", "path to an SSH public key to register")
	cmd.Flags().StringVar(&email, "email", "", "primary email address")
	cmd.Flags().BoolVar(&verified, "verified", false, "mark the email verified (admin assertion)")
	cmd.Flags().BoolVar(&isAdmin, "admin", false, "grant instance admin")
	return cmd
}

func adminEmailVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <username> <address>",
		Short: "mark an email verified by admin assertion",
		Args:  cobra.ExactArgs(2),
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
				return fmt.Errorf("user %s: %w", args[0], err)
			}
			res, err := st.DB.Exec(
				`UPDATE emails SET verified_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), verified_by = 'admin'
				 WHERE user_id = ? AND address = ?`, u.ID, args[1])
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("no address %s on user %s", args[1], args[0])
			}
			fmt.Println("verified", args[1])
			return nil
		},
	}
}
