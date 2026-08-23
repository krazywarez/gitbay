package config

import (
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
root = "/var/lib/forge"
site_url = "https://forge.example"
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
			"system ssh with open registration",
			minimal + "\n[ssh]\nmode = \"system\"\n[registration]\nmode = \"open\"\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"forge@example\"\n",
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
			"[server]\nroot = \"/var/lib/forge\"\nsite_url = \"https://forge.example\"\nbogus = 1\n",
			"unknown config key",
		},
		{
			"missing site_url",
			"[server]\nroot = \"/var/lib/forge\"\n",
			"site_url",
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
			minimal + "\n[registration]\nmode = \"invite\"\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"forge@example\"\n",
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.body)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
