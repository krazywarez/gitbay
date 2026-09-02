// Package config loads and validates the gitbayd server configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server       Server       `toml:"server"`
	SSH          SSH          `toml:"ssh"`
	HTTP         HTTP         `toml:"http"`
	GitDaemon    GitDaemon    `toml:"git_daemon"`
	Web          Web          `toml:"web"`
	Registration Registration `toml:"registration"`
	API          API          `toml:"api"`
	Webhooks     Webhooks     `toml:"webhooks"`
	Pages        Pages        `toml:"pages"`
	LFS          LFS          `toml:"lfs"`
	Limits       Limits       `toml:"limits"`
	Mail         Mail         `toml:"mail"`
	Mirrors      Mirrors      `toml:"mirrors"`
	Deps         Deps         `toml:"deps"`
	// GoImport maps vanity Go module paths to repositories, e.g.
	// "gitbay.org/gitbay" = "krz/gitbay". Requests carrying ?go-get=1
	// under a mapped path get a go-import meta tag.
	GoImport map[string]string `toml:"go_import"`
}

type Server struct {
	Root    string `toml:"root"`
	SiteURL string `toml:"site_url"`

	// SourceRepo names the repository this instance develops itself in, as
	// "owner/name". When set, startup warns if the running build's commit is
	// not on that repository's default branch. Empty disables the check, which
	// is right for any instance that does not host its own source.
	SourceRepo string `toml:"source_repo"`
}

type SSH struct {
	Mode     string   `toml:"mode"` // embedded | system
	Port     int      `toml:"port"`
	HostKeys []string `toml:"host_keys"`
}

type HTTP struct {
	Addr     string `toml:"addr"`
	TLS      string `toml:"tls"` // acme | files | off
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
	// ACME (Let's Encrypt by default). Certificates are cached under
	// server.root/acme. acme_http_addr serves HTTP-01 challenges and
	// redirects to HTTPS; "off" disables it (TLS-ALPN-01 on the HTTPS
	// port still works).
	ACMEEmail    string `toml:"acme_email"`
	ACMEHTTPAddr string `toml:"acme_http_addr"`
}

type GitDaemon struct {
	Enabled bool `toml:"enabled"`
	Port    int  `toml:"port"`
}

type Web struct {
	Mode         string `toml:"mode"` // view_only | accounts
	PasswordAuth bool   `toml:"password_auth"`
	// Title is the instance's display name in the header and page titles.
	// Empty falls back to the site host.
	Title string `toml:"title"`
	// PrivacyNotice is operator-provided text shown on /privacy under the
	// fixed project-level statement. Plain text; blank paragraphs split.
	PrivacyNotice string `toml:"privacy_notice"`
}

type Registration struct {
	Mode string `toml:"mode"` // closed | invite | open
	// PendingExpiry is how long a self-registered account may stay
	// unverified before it is removed, as a duration ("168h"). Empty
	// keeps such accounts forever.
	PendingExpiry string `toml:"pending_expiry"`
}

// PendingExpiryDuration parses PendingExpiry; zero means never.
func (r Registration) PendingExpiryDuration() time.Duration {
	d, _ := time.ParseDuration(r.PendingExpiry)
	return d
}

// LFS stores large-file objects content-addressed under Root (default
// <server.root>/lfs). MaxObjectBytes caps a single object; 0 means the
// 512MB default.
type LFS struct {
	Root           string `toml:"root"`
	MaxObjectBytes int64  `toml:"max_object_bytes"`
}

// Pages serves each public repo's `pages` branch as a static site on
// <owner>.<domain> — a separate origin, so page-authored scripts never run
// on the forge's own host. Empty domain disables the feature.
type Pages struct {
	Domain string `toml:"domain"`
}

// API controls the HTTPS/JSON control-plane API (bearer tokens minted over
// SSH). Off by default: an instance that never enables it has no
// credential-bearing HTTP surface at all.
type API struct {
	Enabled bool `toml:"enabled"`
}

// Webhooks controls outbound delivery. AllowLocal permits endpoints on
// loopback/private addresses (off by default: SSRF).
type Webhooks struct {
	AllowLocal bool `toml:"allow_local"`
}

type Mirrors struct {
	PullIntervalMinutes int `toml:"pull_interval_minutes"`
}

// Deps configures the dependency-update sweep. It runs only for repos that
// have opted in with `repo deps enable`, because checking a private repo
// tells a public registry what it depends on.
type Deps struct {
	CheckIntervalHours int `toml:"check_interval_hours"`
}

type Limits struct {
	MaxPackBytes    int64 `toml:"max_pack_bytes"`
	MaxBlobBytes    int64 `toml:"max_blob_bytes"`
	MaxAssetBytes   int64 `toml:"max_asset_bytes"` // per release asset
	CloneTimeoutSec int   `toml:"clone_timeout"`
	SSHAuthRate     int   `toml:"ssh_auth_rate"`
	// APIRate is sustained JSON-API requests per minute per caller; writes
	// draw on a tenth of it. 0 uses the default.
	APIRate int `toml:"api_rate"`
	// Per-account quotas on what a user owns directly (organizations are
	// not capped). 0 means unlimited; admin user limits overrides per
	// account.
	MaxReposPerUser int   `toml:"max_repos_per_user"`
	MaxBytesPerUser int64 `toml:"max_bytes_per_user"`
}

type Mail struct {
	SMTPHost string `toml:"smtp_host"` // host:port (port defaults to 587)
	From     string `toml:"from"`
	SMTPUser string `toml:"smtp_user,omitempty"`
	SMTPPass string `toml:"smtp_pass,omitempty"`
}

// Default returns the configuration used when a key is absent from the file.
func Default() Config {
	return Config{
		Server: Server{Root: "/var/lib/gitbay"},
		SSH:    SSH{Mode: "embedded", Port: 22},
		HTTP:   HTTP{Addr: ":443", TLS: "acme", ACMEHTTPAddr: ":80"},
		Web:    Web{Mode: "view_only"},
		Registration: Registration{
			Mode: "closed",
		},
		GitDaemon: GitDaemon{Port: 9418},
		Mirrors:   Mirrors{PullIntervalMinutes: 15},
		Deps:      Deps{CheckIntervalHours: 24},
		Limits: Limits{
			MaxPackBytes:    2 << 30, // 2 GiB
			MaxBlobBytes:    100 << 20,
			MaxAssetBytes:   512 << 20,
			CloneTimeoutSec: 3600,
			SSHAuthRate:     10,
			APIRate:         120,
		},
	}
}

// Load reads path, applies defaults, and validates. It does not probe the
// host (see CheckHost) so it is safe in tests and on non-target machines.
func Load(path string) (Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		return cfg, fmt.Errorf("unknown config key %q", u[0].String())
	}
	return cfg, cfg.Validate()
}

func oneOf(field, val string, allowed ...string) error {
	for _, a := range allowed {
		if val == a {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %v, got %q", field, allowed, val)
}

// Validate applies the static contradiction checks from the plan.
func (c Config) Validate() error {
	var errs []error

	if c.Server.Root == "" {
		errs = append(errs, errors.New("server.root is required"))
	}
	if d := c.Pages.Domain; d != "" {
		if d == c.SiteHost() {
			errs = append(errs, errors.New("pages.domain must differ from the site host: pages serve repo-authored scripts, which must not run on the forge's origin"))
		}
		if strings.HasSuffix(c.SiteHost(), "."+d) {
			errs = append(errs, errors.New("pages.domain must not be a parent of the site host"))
		}
	}
	if c.Server.SiteURL == "" {
		errs = append(errs, errors.New("server.site_url is required"))
	}
	if err := oneOf("ssh.mode", c.SSH.Mode, "embedded", "system"); err != nil {
		errs = append(errs, err)
	}
	if c.Registration.PendingExpiry != "" {
		if d, err := time.ParseDuration(c.Registration.PendingExpiry); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("registration.pending_expiry %q must be a positive duration such as 168h", c.Registration.PendingExpiry))
		}
	}
	if c.Limits.MaxReposPerUser < 0 || c.Limits.MaxBytesPerUser < 0 {
		errs = append(errs, errors.New("limits.max_repos_per_user and max_bytes_per_user must not be negative"))
	}
	if c.SSH.Port < 1 || c.SSH.Port > 65535 {
		errs = append(errs, fmt.Errorf("ssh.port %d out of range", c.SSH.Port))
	}
	if err := oneOf("http.tls", c.HTTP.TLS, "acme", "files", "off"); err != nil {
		errs = append(errs, err)
	}
	if c.HTTP.TLS == "files" && (c.HTTP.CertFile == "" || c.HTTP.KeyFile == "") {
		errs = append(errs, errors.New("http.tls = \"files\" requires cert_file and key_file"))
	}
	if c.HTTP.TLS == "acme" {
		host := c.SiteHost()
		switch {
		case !strings.HasPrefix(c.Server.SiteURL, "https://"):
			errs = append(errs, errors.New("http.tls = \"acme\" requires an https:// site_url: certificates are issued for that host"))
		case host == "" || host == "localhost" || net.ParseIP(host) != nil:
			errs = append(errs, fmt.Errorf("http.tls = \"acme\" cannot issue a certificate for %q: use a public DNS name in site_url", host))
		}
	}
	if err := oneOf("web.mode", c.Web.Mode, "view_only", "accounts"); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("registration.mode", c.Registration.Mode, "closed", "invite", "open"); err != nil {
		errs = append(errs, err)
	}

	for module, repo := range c.GoImport {
		host, _, ok := strings.Cut(module, "/")
		if !ok || host == "" || !strings.Contains(host, ".") {
			errs = append(errs, fmt.Errorf("go_import key %q must be host/path (e.g. gitbay.org/gitbay)", module))
		}
		if parts := strings.Split(repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			errs = append(errs, fmt.Errorf("go_import value %q must be owner/name", repo))
		}
	}

	// Contradictions.
	if c.Mail.SMTPHost != "" && c.Mail.From == "" {
		errs = append(errs, errors.New("[mail] from is required when smtp_host is set"))
	}
	if c.Registration.Mode != "closed" && c.Mail.SMTPHost == "" {
		errs = append(errs, fmt.Errorf(
			"registration.mode = %q requires [mail] smtp_host: email verification cannot run without SMTP",
			c.Registration.Mode))
	}
	if c.SSH.Mode == "system" && c.Registration.Mode != "closed" {
		errs = append(errs, fmt.Errorf(
			"ssh.mode = \"system\" requires registration.mode = \"closed\": host sshd rejects unknown keys before the dispatcher runs, so registration by unknown key is impossible"))
	}
	if c.Web.PasswordAuth && c.Web.Mode == "view_only" {
		errs = append(errs, errors.New(
			"web.password_auth = true is meaningless with web.mode = \"view_only\": no login route exists"))
	}
	if c.Web.PasswordAuth && c.Web.Mode == "accounts" {
		errs = append(errs, errors.New(
			"web.password_auth is not implemented yet; browser sessions are minted over SSH (gitbay web login)"))
	}

	return errors.Join(errs...)
}

// SiteHost returns the bare hostname from site_url (no scheme, port, path).
func (c Config) SiteHost() string {
	h := strings.TrimPrefix(strings.TrimPrefix(c.Server.SiteURL, "https://"), "http://")
	h = strings.TrimSuffix(h, "/")
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// CheckHost performs environment probes that only make sense on the target
// machine: port availability for the embedded listener and root existence.
func (c Config) CheckHost() error {
	var errs []error

	if st, err := os.Stat(c.Server.Root); err != nil {
		errs = append(errs, fmt.Errorf("server.root: %w", err))
	} else if !st.IsDir() {
		errs = append(errs, fmt.Errorf("server.root %q is not a directory", c.Server.Root))
	}

	if c.SSH.Mode == "embedded" {
		addr := net.JoinHostPort("", strconv.Itoa(c.SSH.Port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errs = append(errs, fmt.Errorf("ssh.port %d is not bindable (already in use by another daemon?): %w", c.SSH.Port, err))
		} else {
			ln.Close()
		}
	}

	return errors.Join(errs...)
}
