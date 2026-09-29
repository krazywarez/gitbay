package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `
[server]
root = "/var/lib/gitbay"
site_url = "https://gitbay.example"
`

func TestLoadMinimal(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	// Defaults applied.
	if cfg.SSH.Mode != "embedded" || cfg.SSH.Port != 22 {
		t.Errorf("ssh defaults wrong: %+v", cfg.SSH)
	}
	if cfg.Web.Mode != "view_only" {
		t.Errorf("web default wrong: %+v", cfg.Web)
	}
	if cfg.Registration.Mode != "closed" {
		t.Errorf("registration default wrong: %+v", cfg.Registration)
	}
}

func TestPackLimits(t *testing.T) {
	max, per, queue, wait := Limits{}.PackLimits()
	if max != DefaultPackConcurrency || per != DefaultPackPerPrincipal || queue != DefaultPackQueue || wait != DefaultPackQueueWait {
		t.Fatalf("defaults: %d %d %d %s", max, per, queue, wait)
	}
	max, per, queue, wait = Limits{PackConcurrency: -1, PackPerPrincipal: -1, PackQueue: -1, PackQueueWait: "5s"}.PackLimits()
	if max != 0 || per != 0 || queue != math.MaxInt || wait != 5*time.Second {
		t.Fatalf("off: %d %d %d %s", max, per, queue, wait)
	}
	max, per, queue, _ = Limits{PackConcurrency: 8, PackPerPrincipal: 3, PackQueue: 64}.PackLimits()
	if max != 8 || per != 3 || queue != 64 {
		t.Fatalf("set: %d %d %d", max, per, queue)
	}
}

func TestContradictions(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			"bad pack_queue_wait",
			minimal + "\n[limits]\npack_queue_wait = \"soon\"\n",
			"limits.pack_queue_wait",
		},
		{
			"registration open without smtp",
			minimal + "\n[registration]\nmode = \"open\"\n",
			"requires [mail] smtp_host",
		},
		{
			"notify_admin without smtp",
			minimal + "\n[registration]\nnotify_admin = true\n",
			"notify_admin = true requires [mail] smtp_host",
		},
		{
			"system ssh with open registration",
			minimal + "\n[ssh]\nmode = \"system\"\n[registration]\nmode = \"open\"\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"gitbay@example\"\n",
			"requires registration.mode = \"closed\"",
		},
		{
			"unknown mail.tls",
			minimal + "\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"gitbay@example\"\ntls = \"ssl\"\n",
			"mail.tls must be starttls or implicit",
		},
		{
			"password auth in view_only",
			minimal + "\n[web]\nmode = \"view_only\"\npassword_auth = true\n",
			"password_auth",
		},
		{
			"password auth not implemented",
			minimal + "\n[web]\nmode = \"accounts\"\npassword_auth = true\n",
			"not implemented",
		},
		{
			"bad ssh mode",
			minimal + "\n[ssh]\nmode = \"tcp\"\n",
			"ssh.mode",
		},
		{
			"unknown key",
			"[server]\nroot = \"/var/lib/gitbay\"\nsite_url = \"https://gitbay.example\"\nbogus = 1\n",
			"unknown config key",
		},
		{
			"missing site_url",
			"[server]\nroot = \"/var/lib/gitbay\"\n",
			"site_url",
		},
		{
			"negative repo limit",
			minimal + "\n[limits]\nmax_repos_per_user = -1\n",
			"must not be negative",
		},
		{
			"negative snippet limit",
			minimal + "\n[limits]\nmax_snippets_per_user = -1\n",
			"max_snippets_per_user",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidCombinations(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			"invite with smtp",
			minimal + "\n[registration]\nmode = \"invite\"\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"gitbay@example\"\n",
		},
		{
			"system ssh closed registration",
			minimal + "\n[ssh]\nmode = \"system\"\n",
		},
		{
			"accounts web without password auth",
			minimal + "\n[web]\nmode = \"accounts\"\n",
		},
		{
			"closed registration, no smtp at all",
			minimal,
		},
		{
			"acme with public https host",
			"[server]\nroot = \"/var/lib/gitbay\"\nsite_url = \"https://gitbay.org\"\n[http]\ntls = \"acme\"\nacme_email = \"noreply@gitbay.org\"\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.body)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// writeP8 writes a PEM-wrapped PKCS#8 P-256 key, the shape of Apple's
// .p8 provider key, and returns its path.
func writeP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "apns.p8")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPushConfigValidation(t *testing.T) {
	keyPath := writeP8(t)
	full := `
[push]
enabled = true
key_file = "` + keyPath + `"
key_id = "KEYID"
team_id = "TEAMID"
topic = "org.gitbay.gitbay"
environment = "production"
`
	cases := []struct {
		name string
		body string
		want string // substring of the expected error; "" means valid
	}{
		{"disabled needs nothing", "\n[push]\nenabled = false\n", ""},
		{"complete is valid", full, ""},
		{"key_id required", strings.Replace(full, `key_id = "KEYID"`, "", 1), "push.key_id"},
		{"team_id required", strings.Replace(full, `team_id = "TEAMID"`, "", 1), "push.team_id"},
		{"topic required", strings.Replace(full, `topic = "org.gitbay.gitbay"`, "", 1), "push.topic"},
		{"environment must be a known name",
			strings.Replace(full, `environment = "production"`, `environment = "staging"`, 1),
			"push.environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, minimal+tc.body))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// A key_file that exists but is not a PKCS#8 EC key is refused at load,
// not at the first notice: the failure mode otherwise is a queue that
// fills and dead-letters with nobody watching.
func TestPushConfigRejectsAnUnparseableKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk.p8")
	if err := os.WriteFile(p, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `
[push]
enabled = true
key_file = "` + p + `"
key_id = "K"
team_id = "T"
topic = "org.gitbay.gitbay"
environment = "production"
`
	_, err := Load(writeConfig(t, minimal+body))
	if err == nil || !strings.Contains(err.Error(), "push.key_file") {
		t.Fatalf("want a push.key_file error, got %v", err)
	}
}

func TestPushHost(t *testing.T) {
	if got := (Push{Environment: "production"}).Host(); got != "api.push.apple.com" {
		t.Fatalf("production host = %q", got)
	}
	if got := (Push{Environment: "sandbox"}).Host(); got != "api.sandbox.push.apple.com" {
		t.Fatalf("sandbox host = %q", got)
	}
	t.Setenv("GITBAY_APNS_HOST", "127.0.0.1:1234")
	if got := (Push{Environment: "production"}).Host(); got != "127.0.0.1:1234" {
		t.Fatalf("GITBAY_APNS_HOST ignored: %q", got)
	}
}

func TestMailTLSRequired(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		m    Mail
		want bool
	}{
		{Mail{SMTPHost: "smtp.example.com:587"}, true},
		{Mail{SMTPHost: "smtp.example.com"}, true},
		{Mail{SMTPHost: "localhost:25"}, false},
		{Mail{SMTPHost: "localhost"}, false},
		{Mail{SMTPHost: "127.0.0.1:25"}, false},
		{Mail{SMTPHost: "[::1]:25"}, false},
		{Mail{SMTPHost: "smtp.example.com:587", RequireTLS: &off}, false},
		{Mail{SMTPHost: "127.0.0.1:25", RequireTLS: &on}, true},
	} {
		if got := tc.m.TLSRequired(); got != tc.want {
			t.Errorf("%+v: TLSRequired = %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestSecretKeyFile(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.SecretKeyFile != "/etc/gitbay/secret.key" {
		t.Errorf("default secret_key_file = %q", cfg.Server.SecretKeyFile)
	}
	for body, want := range map[string]string{
		minimal + "secret_key_file = \"/var/lib/gitbay/secret.key\"\n": "inside server.root",
		minimal + "secret_key_file = \"/var/lib/gitbay\"\n":            "inside server.root",
		minimal + "secret_key_file = \"\"\n":                           "server.secret_key_file is required",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error containing %q", body, err, want)
		}
	}
	if _, err := Load(writeConfig(t, minimal+"secret_key_file = \"/var/lib/gitbay-keys/secret.key\"\n")); err != nil {
		t.Errorf("a sibling directory of the root is outside it: %v", err)
	}
}

// TestSecretKeyFileSymlinks exercises resolvePath's symlink resolution: a
// key path or root reached through a symlink is still compared on its
// resolved location, not its literal spelling.
func TestSecretKeyFileSymlinks(t *testing.T) {
	valid := func(root, keyFile string) Config {
		cfg := Default()
		cfg.Server.SiteURL = "https://gitbay.example"
		cfg.Server.Root = root
		cfg.Server.SecretKeyFile = keyFile
		return cfg
	}

	t.Run("key path reaches into root through a symlink", func(t *testing.T) {
		tmp := t.TempDir()
		root := filepath.Join(tmp, "root")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(tmp, "link-into-root")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		// The key file itself need not exist yet; only the symlinked
		// directory component does.
		keyFile := filepath.Join(link, "secret.key")
		if err := valid(root, keyFile).Validate(); err == nil || !strings.Contains(err.Error(), "inside server.root") {
			t.Errorf("got %v, want an error containing %q", err, "inside server.root")
		}
	})

	t.Run("root itself is reached through a symlinked parent", func(t *testing.T) {
		tmp := t.TempDir()
		actualRoot := filepath.Join(tmp, "actual", "root")
		if err := os.MkdirAll(actualRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		rootLink := filepath.Join(tmp, "root-link")
		if err := os.Symlink(actualRoot, rootLink); err != nil {
			t.Fatal(err)
		}
		// server.root is configured as the symlink; the key file is given
		// by its real, unsymlinked path under the same directory.
		keyFile := filepath.Join(actualRoot, "secret.key")
		if err := valid(rootLink, keyFile).Validate(); err == nil || !strings.Contains(err.Error(), "inside server.root") {
			t.Errorf("got %v, want an error containing %q", err, "inside server.root")
		}
	})

	t.Run("symlink points outside root", func(t *testing.T) {
		tmp := t.TempDir()
		root := filepath.Join(tmp, "root")
		outside := filepath.Join(tmp, "outside")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		escape := filepath.Join(root, "escape")
		if err := os.Symlink(outside, escape); err != nil {
			t.Fatal(err)
		}
		keyFile := filepath.Join(escape, "secret.key")
		if err := valid(root, keyFile).Validate(); err != nil {
			t.Errorf("a symlink leading outside server.root should be accepted: %v", err)
		}
	})
}

func TestBackupRecipients(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(writeConfig(t, minimal+"[backup]\nage_recipients = [\""+id.Recipient().String()+"\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := cfg.Backup.Recipients()
	if err != nil || len(rs) != 1 {
		t.Fatalf("Recipients = %v, %v", rs, err)
	}
	if _, err := Load(writeConfig(t, minimal+"[backup]\nage_recipients = [\"age1notakey\"]\n")); err == nil || !strings.Contains(err.Error(), "backup.age_recipients") {
		t.Fatalf("a malformed recipient: %v", err)
	}
	if cfg, err := Load(writeConfig(t, minimal)); err != nil || len(cfg.Backup.AgeRecipients) != 0 {
		t.Fatalf("default: %v, %v", cfg.Backup, err)
	}
}

func TestMailInbound(t *testing.T) {
	const smtp = "\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"gitbay@example\"\n"
	const inbound = "[mail.inbound]\nenabled = true\nimap_host = \"imap.example\"\nuser = \"reply@example\"\npassword_file = \"/etc/gitbay/imap.pass\"\nreply_address = \"reply@gitbay.example\"\n"
	cfg, err := Load(writeConfig(t, minimal+smtp+inbound))
	if err != nil {
		t.Fatal(err)
	}
	in := cfg.Mail.Inbound
	if in.Addr() != "imap.example:993" || in.MailboxName() != "INBOX" || in.Poll() != DefaultInboundPoll {
		t.Fatalf("defaults: %q %q %v", in.Addr(), in.MailboxName(), in.Poll())
	}
	if in.RequireDKIM || in.Authenticated() {
		t.Fatalf("require_dkim defaults on or From counts as authenticated: %+v", in)
	}
	cfg, err = Load(writeConfig(t, minimal+smtp+inbound+"require_dkim = true\n"))
	if err != nil || !cfg.Mail.Inbound.RequireDKIM || !cfg.Mail.Inbound.Authenticated() {
		t.Fatalf("require_dkim: %+v, %v", cfg.Mail.Inbound, err)
	}
	in.TLS = "starttls"
	if in.Addr() != "imap.example:143" {
		t.Fatalf("starttls default port: %q", in.Addr())
	}
	for body, want := range map[string]string{
		minimal + inbound: "requires [mail] smtp_host",
		minimal + smtp + inbound + "tls = \"none\"\n":                                                  "IMAP in clear is not supported",
		minimal + smtp + inbound + "poll_interval = \"1s\"\n":                                          "poll_interval",
		minimal + smtp + "[mail.inbound]\nenabled = true\n":                                            "mail.inbound.password_file is required",
		minimal + smtp + strings.Replace(inbound, "reply@gitbay.example", "reply+x@gitbay.example", 1): "no + in it",
		minimal + smtp + strings.Replace(inbound, "reply@gitbay.example", "gitbay.example", 1):         "bare address",
		minimal + smtp + inbound + "trusted_authserv_id = \"mx; x\"\n":                                 "trusted_authserv_id",
		minimal + smtp + inbound + "password = \"x\"\n":                                                "unknown config key",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v\n%s", want, err, body)
		}
	}
	// Off, nothing is required.
	if _, err := Load(writeConfig(t, minimal+"\n[mail.inbound]\nenabled = false\n")); err != nil {
		t.Fatal(err)
	}
}

func TestMailInboundPassword(t *testing.T) {
	dir := t.TempDir()
	in := MailInbound{PasswordFile: dir + "/pass"}
	os.WriteFile(in.PasswordFile, []byte("hunter2\n"), 0o600)
	if p, err := in.Password(); err != nil || p != "hunter2" {
		t.Fatalf("Password = %q, %v", p, err)
	}
	os.Chmod(in.PasswordFile, 0o644)
	if _, err := in.Password(); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("group-readable file: %v", err)
	}
	os.WriteFile(in.PasswordFile, []byte("a\nb\n"), 0o600)
	os.Chmod(in.PasswordFile, 0o600)
	if _, err := in.Password(); err == nil {
		t.Fatal("two lines accepted")
	}
}
