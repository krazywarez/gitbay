// Package config loads and validates the gitbayd server configuration.
package config

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/BurntSushi/toml"
)

// DefaultWriteRate is the per-account write budget when the config leaves
// write_rate at zero: generous for a person at a terminal, and a bound on
// what one account can enqueue — every write also queues notification mail
// and webhook deliveries.
const DefaultWriteRate = 60

// Pack generation defaults for a four-core host: a full clone of a large
// repository runs git at about 1.5 cores (Performance wiki page).
const (
	DefaultPackConcurrency  = 3
	DefaultPackPerPrincipal = 2
	DefaultPackQueue        = 32
	DefaultPackQueueWait    = time.Minute
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
	Retention    Retention    `toml:"retention"`
	Push         Push         `toml:"push"`
	Backup       Backup       `toml:"backup"`
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

	// SecretKeyFile holds the keys that seal the secret columns of the
	// database (internal/seal). It lives outside Root, so neither a
	// backup archive nor a snapshot of Root carries it.
	SecretKeyFile string `toml:"secret_key_file"`
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
	// TrustedProxies are the addresses or CIDRs of reverse proxies in front
	// of this process. A request from one of them is attributed to the
	// last X-Forwarded-For hop that is not itself a trusted proxy; from
	// anyone else the peer address is the client and the header is
	// ignored. Empty means no proxy, which is how gitbayd is deployed by
	// default: it terminates TLS itself.
	TrustedProxies []string `toml:"trusted_proxies,omitempty"`
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
	// NotifyAdmin mails the instance's admins when an account becomes
	// active: an invite redeemed, or an open-mode signup that verified
	// its address. The unverified row an open signup creates is not
	// reported — anyone can post the form, so mailing on that would
	// aim a flood at the admins (#234).
	NotifyAdmin bool `toml:"notify_admin"`
}

// PendingExpiryDuration parses PendingExpiry; zero means never.
func (r Registration) PendingExpiryDuration() time.Duration {
	d, _ := time.ParseDuration(r.PendingExpiry)
	return d
}

// Retention is how long the append-only tables keep a row. Each is a
// duration string ("2160h"); empty or zero keeps forever, which is what
// an instance that has never configured this gets. Expired sessions and
// tokens are swept regardless: they are dead weight the moment they
// expire and no setting makes them worth keeping.
type Retention struct {
	Audit             string `toml:"audit"`
	Events            string `toml:"events"`
	WebhookDeliveries string `toml:"webhook_deliveries"`
	// Mail is the outbound queue: rows already sent or given up on.
	Mail string `toml:"mail"`
	// Push is the outbound device queue: rows already sent or given up on.
	Push string `toml:"push"`
}

// Durations parses the five, mapping each to zero when unset or bad.
func (r Retention) Durations() (audit, events, deliveries, mail, push time.Duration) {
	parse := func(s string) time.Duration {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return 0
		}
		return d
	}
	return parse(r.Audit), parse(r.Events), parse(r.WebhookDeliveries), parse(r.Mail), parse(r.Push)
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
	MaxAssetBytes   int64 `toml:"max_asset_bytes"`   // per release asset
	MaxSnippetBytes int64 `toml:"max_snippet_bytes"` // per snippet file
	// MaxSnippetsPerUser caps snippets an account may own. 0 means
	// unlimited, like MaxReposPerUser.
	MaxSnippetsPerUser int `toml:"max_snippets_per_user"`
	CloneTimeoutSec    int `toml:"clone_timeout"`
	SSHAuthRate        int `toml:"ssh_auth_rate"`
	// APIRate is sustained JSON-API requests per minute per caller; writes
	// draw on a tenth of it. 0 uses the default.
	APIRate int `toml:"api_rate"`
	// WriteRate is sustained mutating commands per minute per account,
	// counted in the dispatcher so every surface shares one budget. 0 uses
	// the default; a negative value turns the limit off.
	WriteRate int `toml:"write_rate"`
	// Per-account quotas on what a user owns directly (organizations are
	// not capped). 0 means unlimited; admin user limits overrides per
	// account.
	MaxReposPerUser int   `toml:"max_repos_per_user"`
	MaxBytesPerUser int64 `toml:"max_bytes_per_user"`
	// PackConcurrency caps git pack generation (upload-pack and
	// upload-archive) running at once across SSH, smart HTTP and git://.
	// PackPerPrincipal caps it per account, or per client address on the
	// anonymous transports. PackQueue is how many may wait for a slot,
	// for at most PackQueueWait ("60s"). For the three counts 0 takes the
	// default and a negative value turns that bound off.
	PackConcurrency  int    `toml:"pack_concurrency"`
	PackPerPrincipal int    `toml:"pack_per_principal"`
	PackQueue        int    `toml:"pack_queue"`
	PackQueueWait    string `toml:"pack_queue_wait"`
}

// PackLimits resolves the pack_* settings for packlimit.New. A zero
// max or per is no bound; an unbounded queue is math.MaxInt, since
// packlimit reads a zero queue as no queue at all.
func (l Limits) PackLimits() (max, per, queue int, wait time.Duration) {
	pick := func(v, def int) int {
		switch {
		case v == 0:
			return def
		case v < 0:
			return 0
		}
		return v
	}
	queue = pick(l.PackQueue, DefaultPackQueue)
	if l.PackQueue < 0 {
		queue = math.MaxInt
	}
	wait = DefaultPackQueueWait
	if d, err := time.ParseDuration(l.PackQueueWait); err == nil && d > 0 {
		wait = d
	}
	return pick(l.PackConcurrency, DefaultPackConcurrency),
		pick(l.PackPerPrincipal, DefaultPackPerPrincipal), queue, wait
}

type Mail struct {
	SMTPHost string `toml:"smtp_host"` // host:port (port defaults to 587, 465 with tls = "implicit")
	From     string `toml:"from"`
	SMTPUser string `toml:"smtp_user,omitempty"`
	SMTPPass string `toml:"smtp_pass,omitempty"`
	// RequireTLS fails delivery when the relay does not offer STARTTLS,
	// instead of sending in clear. Unset, it is on for any relay but
	// localhost or a loopback address (TLSRequired).
	RequireTLS *bool `toml:"require_tls,omitempty"`
	// TLS is "starttls" (the default, also when empty) or "implicit":
	// TLS from the first byte, as relays on port 465 expect.
	TLS string `toml:"tls,omitempty"`
	// Inbound polls a mailbox for replies to notification mail (#295).
	Inbound MailInbound `toml:"inbound"`
}

// MailInbound is the IMAP mailbox replies to notification mail arrive
// in. Off unless enabled. The connection is always encrypted; there is
// no setting for plaintext.
type MailInbound struct {
	Enabled bool `toml:"enabled"`
	// IMAPHost is host:port; the port defaults to 993 with tls =
	// "implicit" (the default) and 143 with tls = "starttls".
	IMAPHost string `toml:"imap_host"`
	TLS      string `toml:"tls"`
	User     string `toml:"user"`
	// PasswordFile holds the mailbox password, read at each connection.
	// Never inline in this file.
	PasswordFile string `toml:"password_file"`
	Mailbox      string `toml:"mailbox"`       // default INBOX
	PollInterval string `toml:"poll_interval"` // default 1m
	// ReplyAddress is the address a notification's Reply-To is built
	// from: reply@example.org becomes reply+<token>@example.org, so the
	// mailbox must receive plus-addressed mail for it (or a catch-all).
	ReplyAddress string `toml:"reply_address"`
	// TrustedAuthservID is the authserv-id the mail host writes in its
	// Authentication-Results header. When set, a reply must carry DMARC
	// pass, or an aligned DKIM pass, in the topmost such header. Only
	// safe when the mail host removes incoming headers claiming its id.
	TrustedAuthservID string `toml:"trusted_authserv_id"`
}

// DefaultInboundPoll is the poll interval when poll_interval is unset.
const DefaultInboundPoll = time.Minute

// Poll is the configured poll interval.
func (m MailInbound) Poll() time.Duration {
	if d, err := time.ParseDuration(m.PollInterval); err == nil && d > 0 {
		return d
	}
	return DefaultInboundPoll
}

// Addr is IMAPHost with the default port filled in.
func (m MailInbound) Addr() string {
	if _, _, err := net.SplitHostPort(m.IMAPHost); err == nil {
		return m.IMAPHost
	}
	if m.TLS == "starttls" {
		return net.JoinHostPort(m.IMAPHost, "143")
	}
	return net.JoinHostPort(m.IMAPHost, "993")
}

// MailboxName is Mailbox, INBOX when unset.
func (m MailInbound) MailboxName() string {
	if m.Mailbox == "" {
		return "INBOX"
	}
	return m.Mailbox
}

// Password reads PasswordFile: its first line, which must be all it
// holds. The file must be readable by its owner alone.
func (m MailInbound) Password() (string, error) {
	f, err := os.Open(m.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("mail.inbound.password_file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("mail.inbound.password_file: %w", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("mail.inbound.password_file %s is mode %04o; it must be readable by its owner alone (0600)", m.PasswordFile, perm)
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", fmt.Errorf("mail.inbound.password_file: %w", err)
	}
	pass := strings.TrimRight(string(raw), "\r\n")
	if pass == "" || len(raw) > 4096 || strings.ContainsAny(pass, "\r\n") {
		return "", fmt.Errorf("mail.inbound.password_file %s must hold the password on one line", m.PasswordFile)
	}
	return pass, nil
}

func (m MailInbound) validate() []error {
	if !m.Enabled {
		return nil
	}
	var errs []error
	for _, f := range []struct{ name, val string }{
		{"mail.inbound.imap_host", m.IMAPHost},
		{"mail.inbound.user", m.User},
		{"mail.inbound.password_file", m.PasswordFile},
		{"mail.inbound.reply_address", m.ReplyAddress},
	} {
		if f.val == "" {
			errs = append(errs, fmt.Errorf("%s is required when mail.inbound.enabled", f.name))
		}
	}
	if t := m.TLS; t != "" && t != "implicit" && t != "starttls" {
		errs = append(errs, fmt.Errorf("mail.inbound.tls must be implicit or starttls, got %q: IMAP in clear is not supported", t))
	}
	if m.PollInterval != "" {
		if d, err := time.ParseDuration(m.PollInterval); err != nil || d < 10*time.Second {
			errs = append(errs, fmt.Errorf("mail.inbound.poll_interval %q must be a duration of at least 10s", m.PollInterval))
		}
	}
	if id := m.TrustedAuthservID; id != "" && strings.ContainsAny(id, " \t;()\"\r\n") {
		errs = append(errs, fmt.Errorf("mail.inbound.trusted_authserv_id %q must be a bare host name such as mx.google.com", id))
	}
	if a := m.ReplyAddress; a != "" {
		local, domain, ok := strings.Cut(a, "@")
		if !ok || local == "" || domain == "" || strings.ContainsAny(a, "+ <>\"\r\n") || strings.Contains(domain, "@") {
			errs = append(errs, fmt.Errorf("mail.inbound.reply_address %q must be a bare address such as reply@example.org, with no + in it", a))
		}
	}
	return errs
}

// TLSRequired reports whether mail must not go to the relay in clear.
func (m Mail) TLSRequired() bool {
	if m.RequireTLS != nil {
		return *m.RequireTLS
	}
	host := m.SMTPHost
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// Push is APNs delivery to registered Apple devices. A key belongs to a
// bundle ID, so an instance pushes to the app built under the topic named
// here and no other; a self-hoster points this at their own key and their
// own build.
type Push struct {
	Enabled bool   `toml:"enabled"`
	KeyFile string `toml:"key_file"`
	KeyID   string `toml:"key_id"`
	TeamID  string `toml:"team_id"`
	Topic   string `toml:"topic"` // the app's bundle identifier
	// Environment is a name rather than a URL so a typo cannot aim the
	// key at a host that is not Apple's.
	Environment string `toml:"environment"` // production | sandbox
}

// Host is the APNs endpoint for the configured environment.
// GITBAY_APNS_HOST overrides it for tests, as GITBAY_SWEEP_TICK does for
// the retention sweep.
func (p Push) Host() string {
	if h := os.Getenv("GITBAY_APNS_HOST"); h != "" {
		return h
	}
	if p.Environment == "sandbox" {
		return "api.sandbox.push.apple.com"
	}
	return "api.push.apple.com"
}

// LoadAPNSKey reads Apple's .p8 provider key: a PEM-wrapped PKCS#8
// P-256 private key. Read at startup and validated there, so a
// misconfigured [push] refuses to start rather than filling a queue
// nobody is watching.
func LoadAPNSKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := any.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC private key")
	}
	return key, nil
}

// Backup configures gitbayd admin backup.
type Backup struct {
	// AgeRecipients, when set, encrypts every archive to these age
	// public keys (age1...). The matching identities stay off the host,
	// so the host writes archives it cannot read.
	AgeRecipients []string `toml:"age_recipients"`
}

// Recipients parses AgeRecipients.
func (b Backup) Recipients() ([]age.Recipient, error) {
	var rs []age.Recipient
	for _, s := range b.AgeRecipients {
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return nil, fmt.Errorf("backup.age_recipients: %q: %w", s, err)
		}
		rs = append(rs, r)
	}
	return rs, nil
}

// Default returns the configuration used when a key is absent from the file.
func Default() Config {
	return Config{
		Server: Server{Root: "/var/lib/gitbay", SecretKeyFile: "/etc/gitbay/secret.key"},
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
			MaxSnippetBytes: 1 << 20,
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

// Within reports whether path is dir or below it. Both are compared as
// cleaned absolute paths (a relative path resolves against the working
// directory, same as every other path in this config), with symlinks
// resolved where the path exists on disk, so a path that reaches into dir
// through a symlink, or through "..", is still reported as inside.
func Within(dir, path string) bool {
	dir, path = resolvePath(dir), resolvePath(path)
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath returns path as a cleaned absolute path, resolving symlinks in
// it. The secret key file commonly does not exist yet (it is created by
// `gitbayd admin secrets init`), and on this platform /var itself is a
// symlink, so a whole-path resolution is tried first and, failing that, each
// ancestor directory in turn, walking up to the nearest one that exists and
// reattaching the missing suffix — a symlinked ancestor still resolves even
// though the leaf, or several levels above it, does not exist.
func resolvePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	dir := abs
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		suffix = append(suffix, filepath.Base(dir))
		dir = parent
	}
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
	switch {
	case c.Server.SecretKeyFile == "":
		errs = append(errs, errors.New("server.secret_key_file is required"))
	case Within(c.Server.Root, c.Server.SecretKeyFile):
		errs = append(errs, fmt.Errorf("server.secret_key_file %q is inside server.root: backups of the root would carry the key beside the values it seals", c.Server.SecretKeyFile))
	}
	if err := oneOf("ssh.mode", c.SSH.Mode, "embedded", "system"); err != nil {
		errs = append(errs, err)
	}
	if c.Registration.PendingExpiry != "" {
		if d, err := time.ParseDuration(c.Registration.PendingExpiry); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("registration.pending_expiry %q must be a positive duration such as 168h", c.Registration.PendingExpiry))
		}
	}
	if c.Limits.MaxReposPerUser < 0 || c.Limits.MaxBytesPerUser < 0 || c.Limits.MaxSnippetsPerUser < 0 {
		errs = append(errs, errors.New("limits.max_repos_per_user, max_bytes_per_user and max_snippets_per_user must not be negative"))
	}
	if w := c.Limits.PackQueueWait; w != "" {
		if d, err := time.ParseDuration(w); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("limits.pack_queue_wait %q must be a positive duration such as 60s", w))
		}
	}
	if c.Push.Enabled {
		for _, f := range []struct{ name, val string }{
			{"push.key_file", c.Push.KeyFile},
			{"push.key_id", c.Push.KeyID},
			{"push.team_id", c.Push.TeamID},
			{"push.topic", c.Push.Topic},
		} {
			if f.val == "" {
				errs = append(errs, fmt.Errorf("%s is required when push.enabled", f.name))
			}
		}
		if err := oneOf("push.environment", c.Push.Environment, "production", "sandbox"); err != nil {
			errs = append(errs, err)
		}
		if c.Push.KeyFile != "" {
			if _, err := LoadAPNSKey(c.Push.KeyFile); err != nil {
				errs = append(errs, fmt.Errorf("push.key_file: %w", err))
			}
		}
	}
	if c.SSH.Port < 1 || c.SSH.Port > 65535 {
		errs = append(errs, fmt.Errorf("ssh.port %d out of range", c.SSH.Port))
	}
	if err := oneOf("http.tls", c.HTTP.TLS, "acme", "files", "off"); err != nil {
		errs = append(errs, err)
	}
	if _, err := c.HTTP.TrustedProxyNets(); err != nil {
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

	if _, err := c.Backup.Recipients(); err != nil {
		errs = append(errs, err)
	}

	// Contradictions.
	if c.Mail.SMTPHost != "" && c.Mail.From == "" {
		errs = append(errs, errors.New("[mail] from is required when smtp_host is set"))
	}
	if t := c.Mail.TLS; t != "" && t != "starttls" && t != "implicit" {
		errs = append(errs, fmt.Errorf("mail.tls must be starttls or implicit, got %q", t))
	}
	errs = append(errs, c.Mail.Inbound.validate()...)
	if c.Mail.Inbound.Enabled && c.Mail.SMTPHost == "" {
		errs = append(errs, errors.New("mail.inbound.enabled requires [mail] smtp_host: replies answer notification mail, which is not sent without SMTP"))
	}
	if c.Registration.Mode != "closed" && c.Mail.SMTPHost == "" {
		errs = append(errs, fmt.Errorf(
			"registration.mode = %q requires [mail] smtp_host: email verification cannot run without SMTP",
			c.Registration.Mode))
	}
	if c.Registration.NotifyAdmin && c.Mail.SMTPHost == "" {
		errs = append(errs, errors.New(
			"registration.notify_admin = true requires [mail] smtp_host: there is nowhere to send the notice"))
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

// TrustedProxyNets parses http.trusted_proxies; a bare address is a /32
// or /128.
func (h HTTP) TrustedProxyNets() ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, p := range h.TrustedProxies {
		if _, n, err := net.ParseCIDR(p); err == nil {
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(p)
		if ip == nil {
			return nil, fmt.Errorf("http.trusted_proxies: %q is not an address or CIDR", p)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets, nil
}
