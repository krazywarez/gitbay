// Package cliconfig manages the client-side configuration: named forge
// instances at ~/.config/forge/config.toml, and parsing of origin remote
// URLs so commands run inside a clone need no --repo argument.
package cliconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

type Instance struct {
	Host string `toml:"host"`
	Port int    `toml:"port,omitempty"`
	User string `toml:"user,omitempty"`
	// SSHOptions are extra arguments passed to the ssh binary verbatim,
	// e.g. ["-i", "~/.ssh/forge_ed25519"]. Most setups need none: the
	// system ssh already honors ~/.ssh/config and the agent.
	SSHOptions []string `toml:"ssh_options,omitempty"`
}

func (i Instance) SSHUser() string {
	if i.User != "" {
		return i.User
	}
	return "git"
}

// CloneURL returns the ssh:// URL for owner/name on this instance.
func (i Instance) CloneURL(repo string) string {
	hostport := i.Host
	if i.Port != 0 && i.Port != 22 {
		hostport = fmt.Sprintf("%s:%d", i.Host, i.Port)
	}
	return fmt.Sprintf("ssh://%s@%s/%s.git", i.SSHUser(), hostport, repo)
}

type Config struct {
	Default   string              `toml:"default,omitempty"`
	Instances map[string]Instance `toml:"instances"`
}

func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "gitbay", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gitbay", "config.toml")
}

func Load() (Config, error) {
	cfg := Config{Instances: map[string]Instance{}}
	raw, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", Path(), err)
	}
	if cfg.Instances == nil {
		cfg.Instances = map[string]Instance{}
	}
	return cfg, nil
}

func Save(cfg Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(cfg); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(b.String()), 0o600)
}

// DefaultInstance returns the configured default (or the only) instance.
func (c Config) DefaultInstance() (Instance, string, error) {
	if c.Default != "" {
		if inst, ok := c.Instances[c.Default]; ok {
			return inst, c.Default, nil
		}
		return Instance{}, "", fmt.Errorf("default instance %q is not configured", c.Default)
	}
	if len(c.Instances) == 1 {
		for name, inst := range c.Instances {
			return inst, name, nil
		}
	}
	return Instance{}, "", fmt.Errorf("no gitbay instance configured; run: gitbay remote add <name> <host>")
}

var (
	sshURLPat = regexp.MustCompile(`^ssh://(?:([^@/]+)@)?([^:/]+)(?::(\d+))?/(.+?)(?:\.git)?/?$`)
	scpPat    = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/]+):(.+?)(?:\.git)?$`)
)

// ParseRemoteURL extracts the instance coordinates and owner/name from a
// git remote URL in ssh:// or scp-like form.
func ParseRemoteURL(url string) (Instance, string, bool) {
	if m := sshURLPat.FindStringSubmatch(url); m != nil {
		inst := Instance{Host: m[2], User: m[1]}
		if m[3] != "" {
			fmt.Sscanf(m[3], "%d", &inst.Port)
		}
		return inst, strings.Trim(m[4], "/"), true
	}
	if m := scpPat.FindStringSubmatch(url); m != nil && !strings.Contains(url, "://") {
		return Instance{Host: m[2], User: m[1]}, strings.Trim(m[3], "/"), true
	}
	return Instance{}, "", false
}
