package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// The runner takes everything as flags, which does not work under a
// service manager. config.toml in the config directory carries the same
// names; a flag on the command line overrides it (#184).

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "gitbay-runner")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "gitbay-runner")
}

func defaultConfigPath() string { return filepath.Join(configDir(), "config.toml") }

// configPathFromArgs finds -config before the flag set is parsed, since
// the file's values must be set before parsing for flags to override them.
func configPathFromArgs(args []string, def string) string {
	for i, a := range args {
		a = strings.TrimPrefix(a, "-")
		if a == "-config" || a == "config" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		if v, ok := strings.CutPrefix(a, "config="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "-config="); ok {
			return v
		}
	}
	return def
}

// configKeys is every key the file may carry: the flag names.
var configKeys = map[string]bool{"remote": true, "ssh-opts": true, "clone-base": true, "workdir": true,
	"poll": true, "timeout": true, "repos": true, "jobs": true, "image": true, "isolation": true,
	"memory": true, "cpus": true, "untrusted": true, "identity": true}

// loadConfig reads path into flag name → value. Absent file: found is
// false and there is no error. An unknown key is an error, not a typo
// the runner silently ignores.
func loadConfig(path string) (values map[string]string, found bool, err error) {
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	values = map[string]string{}
	for k, v := range raw {
		if !configKeys[k] {
			return nil, true, fmt.Errorf("%s: unknown key %s", path, k)
		}
		values[k] = fmt.Sprint(v)
	}
	return values, true, nil
}

// applyConfig sets each value on the flag set, which is what parsing the
// command line would do; parse afterwards and the command line wins.
func applyConfig(fs *flag.FlagSet, values map[string]string) error {
	for k, v := range values {
		if fs.Lookup(k) == nil {
			return fmt.Errorf("config: unknown key %s", k)
		}
		if err := fs.Set(k, v); err != nil {
			return fmt.Errorf("config: %s: %w", k, err)
		}
	}
	return nil
}

// identityOpts is what makes ssh and git use the runner's own key and no
// other. On a laptop the user's ~/.ssh/config names their full-scope key
// for the instance, and IdentitiesOnly keeps identities from the config,
// so the runner would authenticate as that key and, on an admin's
// machine, claim every repository's builds. -F /dev/null drops the
// config; known_hosts is unaffected, and a host seen for the first time
// is accepted, since a service cannot answer a prompt.
func identityOpts(path string) []string {
	if path == "" {
		return nil
	}
	return []string{"-F", "/dev/null", "-i", path, "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=accept-new"}
}
