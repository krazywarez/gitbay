// gitbayd is the forge server daemon. The same binary also runs in hook mode
// (invoked by git via core.hooksPath) and hosts the host-local admin commands.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitd"
	"gitbay.org/gitbay/internal/hookd"
	"gitbay.org/gitbay/internal/httpd"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/mirror"
	"gitbay.org/gitbay/internal/notify"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/sshd"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/webhook"
)

func openStore(cfg config.Config) (*store.Store, error) {
	s, err := store.Open(filepath.Join(cfg.Server.Root, "gitbay.db"))
	if err != nil {
		return nil, err
	}
	// Say so when the schema moves. A restart migrates in silence otherwise,
	// which makes an unexpected schema version hard to attribute to the deploy
	// that caused it.
	before, err := s.Version()
	if err != nil {
		s.Close()
		return nil, err
	}
	if err := s.MigrateUp(); err != nil {
		s.Close()
		return nil, err
	}
	after, err := s.Version()
	if err != nil {
		s.Close()
		return nil, err
	}
	if after != before {
		slog.Info("schema migrated", "from", before, "to", after)
	}
	return s, nil
}

var configPath string

func main() {
	root := &cobra.Command{
		Use:           "gitbayd",
		Short:         "gitbay server daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", "/etc/gitbay/config.toml", "path to config file")

	root.AddCommand(
		checkConfigCmd(),
		serveCmd(),
		migrateCmd(),
		adminCmd(),
		hookCmd(),
		authorizedKeysCmd(),
		shellCmd(),
		versionCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gitbayd:", err)
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
			// First line of every run: the journal then says which commit is
			// serving, without rebuilding the binary to find out.
			logBuild()
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			warnIfUnmerged(cfg)
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			// Regenerate hook scripts so a moved binary self-heals, then
			// start the hook policy socket.
			self, err := os.Executable()
			if err != nil {
				return err
			}
			if err := hookd.WriteHookScripts(control.HooksDir(cfg.Server.Root), self); err != nil {
				return err
			}
			stopHookd, err := hookd.Serve(cfg, st)
			if err != nil {
				return err
			}
			defer stopHookd()

			// Outbound webhook deliveries. The retry base is overridable
			// for tests via GITBAY_WEBHOOK_RETRY_BASE.
			retryBase := 30 * time.Second
			if v := os.Getenv("GITBAY_WEBHOOK_RETRY_BASE"); v != "" {
				if d, err := time.ParseDuration(v); err == nil {
					retryBase = d
				}
			}
			whCtx, whCancel := context.WithCancel(context.Background())
			defer whCancel()
			go webhook.New(st, cfg.Webhooks.AllowLocal, retryBase).Run(whCtx)
			if cfg.Mail.SMTPHost != "" {
				go notify.New(st, cfg, retryBase).Run(whCtx)
			}
			go mirror.New(st, cfg).Run(whCtx)
			go (&ci.Scheduler{St: st, SiteURL: cfg.Server.SiteURL,
				RepoDir: func(owner, name string) string {
					return control.RepoDir(cfg.Server.Root, owner, name)
				}}).Run(whCtx)

			errCh := make(chan error, 3)
			if cfg.SSH.Mode == "embedded" {
				srv, err := sshd.New(cfg, st)
				if err != nil {
					return err
				}
				ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.SSH.Port)))
				if err != nil {
					return err
				}
				slog.Info("ssh listening", "addr", ln.Addr())
				go func() { errCh <- srv.Serve(ln) }()
			} else {
				// system mode: the host sshd owns the SSH port and invokes
				// this binary via AuthorizedKeysCommand + forced command.
				slog.Info("ssh handled by host sshd (ssh.mode = system)")
			}

			web := httpd.New(cfg, st)
			hs := &http.Server{Addr: cfg.HTTP.Addr, Handler: web.Handler()}
			go func() {
				slog.Info("http listening", "addr", cfg.HTTP.Addr, "tls", cfg.HTTP.TLS)
				switch cfg.HTTP.TLS {
				case "off":
					errCh <- hs.ListenAndServe()
				case "files":
					errCh <- hs.ListenAndServeTLS(cfg.HTTP.CertFile, cfg.HTTP.KeyFile)
				case "acme":
					host := cfg.SiteHost()
					stripPort := func(hp string) string {
						if h, _, err := net.SplitHostPort(hp); err == nil {
							return h
						}
						return hp
					}
					// Beyond the site host, allow <owner>.<pages domain>
					// for owners that exist — certs come on demand per
					// subdomain, no wildcard needed.
					hostPolicy := func(ctx context.Context, h string) error {
						if h == host {
							return nil
						}
						if pd := cfg.Pages.Domain; pd != "" {
							if h == pd {
								return nil // apex: serves a redirect to the forge
							}
							if owner, ok := strings.CutSuffix(h, "."+pd); ok &&
								!strings.Contains(owner, ".") && st.OwnerExists(owner) {
								return nil
							}
						}
						// Custom pages domains: certs only for claimed hosts.
						if _, err := st.PageDomainRepo(h); err == nil {
							return nil
						}
						return fmt.Errorf("host %q not served here", h)
					}
					m := &autocert.Manager{
						Prompt:     autocert.AcceptTOS,
						Cache:      autocert.DirCache(filepath.Join(cfg.Server.Root, "acme")),
						HostPolicy: hostPolicy,
						Email:      cfg.HTTP.ACMEEmail,
					}
					// TLS-ALPN-01 rides the HTTPS port itself. The optional
					// plain-HTTP listener adds HTTP-01 and a redirect; losing
					// it (port 80 taken, no privileges) is not fatal.
					if addr := cfg.HTTP.ACMEHTTPAddr; addr != "" && addr != "off" {
						redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							// Pages hosts redirect to themselves, not the
							// forge host.
							target := host
							if hostPolicy(r.Context(), stripPort(r.Host)) == nil {
								target = stripPort(r.Host)
							}
							http.Redirect(w, r, "https://"+target+r.URL.RequestURI(), http.StatusMovedPermanently)
						})
						go func() {
							slog.Info("acme http listening", "addr", addr)
							if err := http.ListenAndServe(addr, m.HTTPHandler(redirect)); err != nil {
								slog.Warn("acme http listener failed; continuing with TLS-ALPN only", "err", err)
							}
						}()
					}
					hs.TLSConfig = m.TLSConfig()
					errCh <- hs.ListenAndServeTLS("", "")
				}
			}()

			if cfg.GitDaemon.Enabled {
				gln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.GitDaemon.Port)))
				if err != nil {
					return err
				}
				slog.Info("git-daemon listening", "addr", gln.Addr())
				go func() { errCh <- gitd.New(cfg, st).Serve(gln) }()
			}

			return <-errCh
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
			s, err := store.Open(cfg.Server.Root + "/gitbay.db")
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
	userCmd := &cobra.Command{Use: "user", Short: "manage users"}
	userCmd.AddCommand(adminUserCreateCmd(), adminUserDisableCmd(), adminUserEnableCmd(), adminUserDeleteCmd())
	emailCmd := &cobra.Command{Use: "email", Short: "manage user emails"}
	emailCmd.AddCommand(adminEmailVerifyCmd())
	admin.AddCommand(
		userCmd,
		emailCmd,
		adminInviteCmd(),
		backupCmd(),
		gcCmd(),
		statsCmd(),
		adminAuditCmd(),
		adminMigrateCommitRefsCmd(),
		adminBackfillActivityCmd(),
	)
	return admin
}

func adminInviteCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "issue a registration invite and email its code",
		RunE: func(cmd *cobra.Command, args []string) error {
			if email == "" {
				return fmt.Errorf("--email is required")
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

			if used, err := st.EmailInUse(email); err != nil {
				return err
			} else if used {
				return fmt.Errorf("%s already belongs to an account; invites are for new users", email)
			}
			code, hash, err := store.NewToken()
			if err != nil {
				return err
			}
			if err := st.CreateInvite(hash, email); err != nil {
				return err
			}
			host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(cfg.Server.SiteURL, "https://"), "http://"), "/")
			body := fmt.Sprintf(
				"You have been invited to %s.\n\nCreate your account by running (with the SSH key you want to use):\n\n"+
					"    ssh git@%s register --username <name> --invite %s\n\n"+
					"The invite is single-use and tied to this address.\n", host, host, code)
			if cfg.Mail.SMTPHost != "" {
				if err := mail.Send(cfg, email, "your invite to "+host, body); err != nil {
					return fmt.Errorf("invite stored but mail failed: %w (code: %s)", err, code)
				}
				st.Audit(0, "admin invite.issued", map[string]any{"email": email})
				fmt.Printf("invite emailed to %s\n", email)
			} else {
				fmt.Printf("invite for %s (no SMTP configured; deliver it yourself):\n%s\n", email, code)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "address to invite (the account's verified email)")
	return cmd
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
			st.Audit(0, "admin user.created", map[string]any{"user": username})
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
			if err := st.VerifyEmail(u.ID, args[1], "admin"); err != nil {
				st.Audit(0, "admin email.verify_failed", map[string]any{"user": args[0], "email": args[1]})
				return fmt.Errorf("no address %s on user %s", args[1], args[0])
			}
			st.Audit(0, "admin email.verified", map[string]any{"user": args[0], "email": args[1]})
			fmt.Println("verified", args[1])
			return nil
		},
	}
}
