// gitbayd is the forge server daemon. The same binary also runs in hook mode
// (invoked by git via core.hooksPath) and hosts the host-local admin commands.
package main

import (
	"context"
	"fmt"
	"io"
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

	"gitbay.org/gitbay/internal/buildinfo"
	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/deps"
	"gitbay.org/gitbay/internal/gitd"
	"gitbay.org/gitbay/internal/hookd"
	"gitbay.org/gitbay/internal/httpd"
	"gitbay.org/gitbay/internal/mirror"
	"gitbay.org/gitbay/internal/notify"
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
			go deps.New(st, cfg, func(owner, name string) string {
				return control.RepoDir(cfg.Server.Root, owner, name)
			}, buildinfo.String()).Run(whCtx)

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
	userCmd.AddCommand(
		hostUserCreateCmd(),
		hostCmd("list [--state active|pending|disabled|admin] [--limit n] [--cursor c]", "list accounts", "admin", "user", "list"),
		hostCmd("show <username>", "show an account: keys, emails, orgs, tokens, sessions", "admin", "user", "show"),
		hostCmd("disable <username>", "suspend an account: keys and sessions refused until re-enabled", "admin", "user", "disable"),
		hostCmd("enable <username>", "restore a suspended account", "admin", "user", "enable"),
		hostCmd("delete <username> --yes", "delete an account that anchors nothing (keys, emails, and sessions go with it)", "admin", "user", "delete"),
		hostCmd("promote <username>", "make an account an instance admin", "admin", "user", "promote"),
		hostCmd("demote <username>", "remove instance admin from an account (never the last one)", "admin", "user", "demote"),
	)
	emailCmd := &cobra.Command{Use: "email", Short: "manage user emails"}
	emailCmd.AddCommand(hostCmd("verify <username> <address>", "mark an email verified by admin assertion", "admin", "email", "verify"))
	repoCmd := &cobra.Command{Use: "repo", Short: "any repository, for moderation (audited)"}
	repoCmd.AddCommand(
		hostCmd("list [--owner o] [--visibility public|private] [--limit n] [--cursor c]", "every repository with size and last push", "admin", "repo", "list"),
		hostCmd("archive <owner/name>", "archive a repository", "admin", "repo", "archive"),
		hostCmd("unarchive <owner/name>", "unarchive a repository", "admin", "repo", "unarchive"),
		hostCmd("visibility <owner/name> public|private", "set a repository's visibility", "admin", "repo", "visibility"),
		hostCmd("delete <owner/name> --yes", "delete a repository", "admin", "repo", "delete"),
	)
	configCmd := &cobra.Command{Use: "config", Short: "the configuration in effect"}
	configCmd.AddCommand(configShowCmd())
	admin.AddCommand(
		userCmd,
		emailCmd,
		repoCmd,
		configCmd,
		hostCmd("invite --email <address>", "issue a registration invite and email its code", "admin", "invite"),
		hostCmd("stats [--json]", "instance statistics: counts and per-repository disk usage", "admin", "stats"),
		hostCmd("runners [--json]", "runner accounts: last poll, scope, the build each holds", "admin", "runners"),
		hostCmd("audit [--limit n] [--json]", "print the security audit log, newest first", "audit"),
		backupCmd(),
		gcCmd(),
		adminMigrateCommitRefsCmd(),
		adminBackfillActivityCmd(),
	)
	return admin
}

// hostCmd runs a registry command as the host itself: an admin context
// with no account behind it, so audit rows carry no actor and the source
// "host". Arguments pass through untouched; the registry owns the flags,
// which is what keeps this surface and an admin's SSH session from
// drifting.
func hostCmd(use, short string, path ...string) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				if a == "--help" || a == "-h" {
					return cmd.Help()
				}
			}
			return runAsHost(path, args, os.Stdin)
		},
	}
}

// hostArgs pulls the root's --config out of args: with flag parsing off,
// cobra hands the persistent flag through untouched.
func hostArgs(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
		default:
			rest = append(rest, args[i])
		}
	}
	return rest
}

func runAsHost(path, args []string, stdin io.Reader) error {
	args = hostArgs(args)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	c := &control.Ctx{
		User:   store.User{Username: "host", IsAdmin: true},
		Scope:  "full",
		Store:  st,
		Cfg:    cfg,
		Stdin:  stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Source: "host",
	}
	code := control.Dispatch(c, append(append([]string{}, path...), args...))
	st.Close()
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// hostUserCreateCmd keeps --key <path>, which the registry command cannot
// take (no file paths over SSH): the file becomes the command's stdin.
func hostUserCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "create <username> [--admin] [--email <address> [--verified]] [--key <file>]",
		Short:              "create a user (host-local bootstrap; the only path in closed mode)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var stdin io.Reader = os.Stdin
			var rest []string
			args = hostArgs(args)
			for i := 0; i < len(args); i++ {
				switch {
				case args[i] == "--help" || args[i] == "-h":
					return cmd.Help()
				case args[i] == "--key" && i+1 < len(args) && args[i+1] != "-":
					f, err := os.Open(args[i+1])
					if err != nil {
						return err
					}
					defer f.Close()
					stdin = f
					rest = append(rest, "--key", "-")
					i++
				default:
					rest = append(rest, args[i])
				}
			}
			return runAsHost([]string{"admin", "user", "create"}, rest, stdin)
		},
	}
}
