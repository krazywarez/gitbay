package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/seal"
	"gitbay.org/gitbay/internal/store"
)

func TestOpenStoreRefusesWithoutKeyFile(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.SecretKeyFile = filepath.Join(t.TempDir(), "absent.key")
	_, err := openStore(cfg)
	if err == nil || !strings.Contains(err.Error(), "gitbayd admin secrets init") || !strings.Contains(err.Error(), cfg.Server.SecretKeyFile) {
		t.Fatalf("openStore without a key file: %v", err)
	}
}

// storeWithSecret opens cfg's store and stores one build secret.
func storeWithSecret(t *testing.T, cfg config.Config) (*store.Store, int64) {
	t.Helper()
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetBuildSecret(repoID, "TOKEN", "v1"); err != nil {
		t.Fatal(err)
	}
	return st, repoID
}

func TestRotateSecrets(t *testing.T) {
	cfg := testConfig(t)
	before, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	st, repoID := storeWithSecret(t, cfg)
	st.Close()

	var out bytes.Buffer
	if err := rotateSecrets(cfg, &out); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	after, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID == before[0].ID {
		t.Fatalf("key file after rotation holds %v, before %v", after, before)
	}
	assertNoKeyMaterial(t, out.String(), append(before, after...))
	st, err = openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	use, err := st.SecretKeyUse()
	if err != nil || use[after[0].ID] != 1 || len(use) != 1 {
		t.Fatalf("SecretKeyUse after rotation = %v, %v", use, err)
	}
	if got, _ := st.BuildSecrets(repoID); got["TOKEN"] != "v1" {
		t.Fatalf("value after rotation: %v", got)
	}
}

// A reseal that fails leaves the old keys in the file, so every value
// still opens.
func TestRotateSecretsKeepsOldKeysWhenResealFails(t *testing.T) {
	cfg := testConfig(t)
	before, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	st, repoID := storeWithSecret(t, cfg)
	// A second value sealed under a key the file does not hold.
	if _, err := st.DB.Exec("INSERT INTO build_secrets (repo_id, name, value) VALUES (?, 'BAD', 'gbs1:deadbeef:AAAA')", repoID); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := rotateSecrets(cfg, &bytes.Buffer{}); err == nil {
		t.Fatal("rotate succeeded over a value that does not open")
	}
	after, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].ID != before[0].ID {
		t.Fatalf("key file after a failed rotation holds %v", after)
	}
}

func TestInitSecrets(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.SecretKeyFile = filepath.Join(t.TempDir(), "secret.key")
	var out bytes.Buffer
	if err := initSecrets(cfg, &out); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %04o", fi.Mode().Perm())
	}
	keys, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys %v, %v", keys, err)
	}
	if !strings.Contains(out.String(), keys[0].ID) {
		t.Fatalf("output does not name the key id: %q", out.String())
	}
	assertNoKeyMaterial(t, out.String(), keys)
	if err := initSecrets(cfg, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: %v", err)
	}
	if again, _ := seal.ReadKeys(cfg.Server.SecretKeyFile); again[0].ID != keys[0].ID {
		t.Fatal("second init replaced the key")
	}
}

func TestCheckSecrets(t *testing.T) {
	cfg := testConfig(t)
	keys, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	st, repoID := storeWithSecret(t, cfg)

	var out bytes.Buffer
	if err := checkSecrets(cfg, &out); err != nil {
		t.Fatalf("check: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "build_secrets.value: key "+keys[0].ID+" 1") {
		t.Fatalf("check output:\n%s", out.String())
	}
	assertNoKeyMaterial(t, out.String(), keys)

	if _, err := st.DB.Exec("INSERT INTO build_secrets (repo_id, name, value) VALUES (?, 'BAD', 'gbs1:deadbeef:AAAA')", repoID); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out.Reset()
	err = checkSecrets(cfg, &out)
	if err == nil || !strings.Contains(err.Error(), "does not open 1 stored value") {
		t.Fatalf("check over a value that does not open: %v", err)
	}
	if !strings.Contains(out.String(), "deadbeef") || !strings.Contains(out.String(), "build_secrets.value row") {
		t.Fatalf("check output does not name the failing row:\n%s", out.String())
	}
}

func assertNoKeyMaterial(t *testing.T, out string, keys []seal.Key) {
	t.Helper()
	for _, k := range keys {
		if strings.Contains(out, base64.StdEncoding.EncodeToString(k.Secret)) {
			t.Fatalf("output carries key %s's secret", k.ID)
		}
	}
}
