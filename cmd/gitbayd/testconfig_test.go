package main

import (
	"path/filepath"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/seal"
)

// testConfig is a config with a fresh root and a key file outside it,
// the minimum openStore accepts.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	key := filepath.Join(t.TempDir(), "secret.key")
	k, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := seal.WriteKeys(key, []seal.Key{k}); err != nil {
		t.Fatal(err)
	}
	return config.Config{Server: config.Server{Root: t.TempDir(), SecretKeyFile: key}}
}
