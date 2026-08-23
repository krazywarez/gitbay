// Package config loads and validates the forged server configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server       Server       `toml:"server"`
	SSH          SSH          `toml:"ssh"`
	HTTP         HTTP         `toml:"http"`
	GitDaemon    GitDaemon    `toml:"git_daemon"`
	Web          Web          `toml:"web"`
	Registration Registration `toml:"registration"`
	Limits       Limits       `toml:"limits"`
	Mail         Mail         `toml:"mail"`
}

type Server struct {
	Root    string `toml:"root"`
	SiteURL string `toml:"site_url"`
}

type SSH struct {
	Mode     string   `toml:"mode"` // embedded | system
	Port     int      `toml:"port"`
	HostKeys []string `toml:"host_keys"`
}

type HTTP struct {
	Addr string `toml:"addr"`
	TLS  string `toml:"tls"` // acme | files | off
}

type GitDaemon struct {
	Enabled bool `toml:"enabled"`
	Port    int  `toml:"port"`
}

type Web struct {
	Mode         string `toml:"mode"` // view_only | accounts
	PasswordAuth bool   `toml:"password_auth"`
}

type Registration struct {
	Mode string `toml:"mode"` // closed | invite | open
}

type Limits struct {
	MaxPackBytes    int64 `toml:"max_pack_bytes"`
	MaxBlobBytes    int64 `toml:"max_blob_bytes"`
	CloneTimeoutSec int   `toml:"clone_timeout"`
	SSHAuthRate     int   `toml:"ssh_auth_rate"`
}

type Mail struct {
	SMTPHost string `toml:"smtp_host"`
	From     string `toml:"from"`
}

// Default returns the configuration used when a key is absent from the file.
func Default() Config {
	return Config{
		Server: Server{Root: "/var/lib/forge"},
		SSH:    SSH{Mode: "embedded", Port: 22},
		HTTP:   HTTP{Addr: ":443", TLS: "acme"},
		Web:    Web{Mode: "view_only"},
		Registration: Registration{
			Mode: "closed",
		},
		GitDaemon: GitDaemon{Port: 9418},
		Limits: Limits{
			MaxPackBytes:    2 << 30, // 2 GiB
			MaxBlobBytes:    100 << 20,
			CloneTimeoutSec: 3600,
			SSHAuthRate:     10,
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
	if c.Server.SiteURL == "" {
		errs = append(errs, errors.New("server.site_url is required"))
	}
	if err := oneOf("ssh.mode", c.SSH.Mode, "embedded", "system"); err != nil {
		errs = append(errs, err)
	}
	if c.SSH.Port < 1 || c.SSH.Port > 65535 {
		errs = append(errs, fmt.Errorf("ssh.port %d out of range", c.SSH.Port))
	}
	if err := oneOf("http.tls", c.HTTP.TLS, "acme", "files", "off"); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("web.mode", c.Web.Mode, "view_only", "accounts"); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("registration.mode", c.Registration.Mode, "closed", "invite", "open"); err != nil {
		errs = append(errs, err)
	}

	// Contradictions.
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

	return errors.Join(errs...)
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
