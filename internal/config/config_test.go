package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestContradictions(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
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
