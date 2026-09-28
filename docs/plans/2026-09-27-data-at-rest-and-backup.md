# Data at rest and backup implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Seal the four secret columns under a key file outside the
database (#273), encrypt backup archives to an age recipient (#274),
and make `--verify` check git connectivity while repository moves and
deletions wait for a running backup (#259), ending with a restore drill
the operator runs and records.

**Architecture:** A new `internal/seal` package holds AES-256-GCM keys
read from `server.secret_key_file` (default `/etc/gitbay/secret.key`,
mode 0600, outside `server.root`). The store seals on write and opens
on read, so no caller above `internal/store` changes. Every value
carries `gbs1:<key id>:`; `serve` seals leftover clear values and
values under retired keys at startup, and `gitbayd admin secrets
rotate` adds a key, reseals, and retires the old one. Backups wrap the
tar.gz in `filippo.io/age` when `[backup] age_recipients` is set.
`--verify` extracts repositories and runs `git fsck
--connectivity-only` on each. A `flock(2)` on `<root>/backup.lock`
keeps deletes, renames and transfers (daemon process) out of a full
backup (separate `gitbayd admin backup` process).

**Tech Stack:** Go 1.27, `crypto/aes` + `crypto/cipher` (GCM),
`filippo.io/age` (new dependency), `syscall.Flock`, SQLite via
`modernc.org/sqlite`, cobra.

**Spec:** the issue texts of #273, #274 and #259 on krz/gitbay, and the
decisions recorded in the brief: AES-GCM, key file under `/etc/gitbay`
mode 0600 excluded from backups, key id prefix on each value, rotation
command, re-encryption of existing rows; age recipients, `--verify`
takes an identity file; the clean-host drill is an operator runbook
recorded on the Admin wiki page.

## Global Constraints

- Each MR on its own branch off `main`. Commits are signed (the repo
  refuses unsigned), messages reference issues (`Ref #N`, and
  `Closes #N` on the commit that finishes one). No attribution to any
  assistant, model or AI anywhere: commits, MR bodies, comments.
- MR: `gitbay mr create --source <branch> --target main --title "..."`;
  merge with `gitbay mr merge <n> --strategy ff` once CI is green, then
  delete the branch locally and on the remote. Behind main → rebase,
  force-push, merge again.
- Locally: `go build ./...`, `go vet ./...`, unit tests of touched
  packages, and at most the one e2e test being written
  (`go test ./e2e -run TestName -count=1`). CI on bay1 runs the full suite.
- Registries that fail CI when a new thing lacks its row: top-level route
  word in `internal/policy/names.go`; new page template in the width map
  of `TestMainWidthClass` (`internal/web/web_test.go`); new `ReadOnly`
  command in `readArgs` in `e2e/readonly_test.go`; new control command
  needs a `pass()` entry in `cmd/gitbay/main.go` (coverage test);
  a command reading stdin needs `ReadsStdin: true`. This plan adds no
  control command, route or template: `gitbayd admin secrets` is a
  host-local cobra command like `admin backup` and `admin gc`, so none
  of the registries gains a row.
- Migrations: the highest today is 0059. This plan owns 0072–0074 and
  uses one, `0072_push_token_hash`. Plans 1–3 own 0060–0071; whoever
  lands second renumbers to the next free number at execution time
  (`loadMigrations` in `internal/store/store.go` refuses a gap, so 0072
  cannot land before 0060–0071 exist: rename it to the next free
  number). Migrations come in `.up.sql`/`.down.sql` pairs. Hand-written
  SQL, no ORM.
- Secrets travel on stdin, never argv; never logged or echoed. The key
  file's contents are never printed; commands print key ids only.
- Wiki pages live in `.gitbay/wiki/`. Update the page in the same MR
  that changes the behaviour it describes, and close the matching
  Known-Gaps row (`.gitbay/wiki/Architecture/10-Known-Gaps.org`) and
  controls-matrix row (`Architecture/09-Controls.org`).
- Writing style: plain, direct, no hype; code comments match the
  surrounding density. Comments and docs state facts, never
  before/after narration.
- Plan-specific:
  - Sealed value format: `gbs1:<8 lowercase hex key id>:<base64 raw
    std (nonce ‖ ciphertext ‖ tag)>`. Additional data is
    `<table>.<column>`. An empty value is never sealed (empty means
    "none" for `webhooks.secret` and `mirrors.token`).
  - Key file format: one `<id> <base64 32 bytes>` per line, `#`
    comments; the last key seals, all keys open. Mode must be 0600 or
    stricter; anything group- or world-readable is refused.
  - `server.secret_key_file` must not be inside `server.root`
    (validation error), so neither the archive nor the restic snapshot
    of `/var/lib/gitbay` can carry it.
  - A missing key file is fatal for every process that opens the
    database through `openStore` (serve, shell, authorized-keys, host
    admin commands), with a message naming the path and
    `gitbayd admin secrets init`. `gitbayd migrate` does not need it.
  - Encrypted archives end in `.age`; `--out` without the suffix gets
    it appended when `[backup] age_recipients` is set.

## Order and dependencies

| MR | Branch | Issue | Contents |
|----|--------|-------|----------|
| 1 | `secrets-at-rest` | Closes #273 | `internal/seal`, `server.secret_key_file`, store sealing, migration 0072, reseal at startup, `admin secrets init/rotate/check`, install.sh, e2e harness key, wiki |
| 2 | `backup-age` | Closes #274 | `[backup] age_recipients`, age-wrapped archives, `--verify --identity`, backup script globs, wiki |
| 3 | `backup-verify-lock` | Ref #259 | `gitutil.FsckConnectivity`, `--verify` extracts and checks each repository, `internal/backuplock`, delete/rename/transfer/org rename refused during a full backup, drill procedure and record table on the Admin page |
| — | `restore-drill-record` (operator) | Closes #259 | the first drill's numbers in the Admin page, Known-Gaps and Controls rows closed |

MR 2 and MR 3 both edit `cmd/gitbayd/backup.go`; land them in order.
MR 2's `testConfig` helper comes from MR 1.

Other plans: no hard dependency. Soft overlaps, resolved by rebase:
plan 3 (#279) changes `internal/mirror/mirror.go`, which reads
`store.Mirror.Token` — the field stays a plain string after this plan,
so its code is unaffected. Plan 5 (#261) touches the migration runner
in `internal/store/store.go`; migration 0072 does not use the
`-- foreign_keys: off` directive.

## File map

| File | MR | Responsibility |
|---|---|---|
| `internal/seal/seal.go` (create) | 1 | key file read/write, `Keyring`, `Seal`/`Open`, `KeyID` |
| `internal/seal/seal_test.go` (create) | 1 | round trip, AAD binding, reload on change, mode refusal |
| `internal/config/config.go` | 1, 2 | `Server.SecretKeyFile`, `Backup.AgeRecipients`, validation |
| `internal/store/secrets.go` (create) | 1 | `SetKeyring`, `sealValue`/`openValue`, `ResealSecrets`, `SecretKeyUse`, `tokenHash` |
| `internal/store/store.go` | 1 | `secrets` field on `Store` |
| `internal/store/cisecrets.go`, `webhooks.go`, `mirrors.go`, `push.go` | 1 | seal in the write transaction, open on read, token hash lookups |
| `internal/store/migrations/0072_push_token_hash.{up,down}.sql` (create) | 1 | `push_devices.token_hash` + unique index |
| `cmd/gitbayd/main.go` | 1 | `openStore` loads the keyring; `serve` reseals; `admin secrets` wired |
| `cmd/gitbayd/secrets.go` (create) | 1 | `admin secrets init|rotate|check` |
| `cmd/gitbayd/testconfig_test.go` (create) | 1 | `testConfig` helper |
| `cmd/gitbayd/backup.go` | 2, 3 | age wrap, `--identity`, lock, connectivity |
| `internal/gitutil/merge.go` | 3 | `FsckConnectivity` |
| `internal/backuplock/backuplock.go` (create) | 3 | `Hold`, `TryShared`, `ErrBusy` |
| `internal/control/repo.go`, `org.go` | 3 | `holdOffBackup` in delete/rename/transfer/org rename |
| `deploy/install.sh` | 1 | create the key file once |
| `deploy/cloud-init.yaml` | 2 | backup script and monitor globs for `.age` |
| `e2e/ssh_test.go`, `acme_test.go`, `system_test.go`, `backup_test.go` | 1, 3 | key file in every config; sealed-at-rest and connectivity checks |
| `.gitbay/wiki/Admin.org`, `Threat-Model.org`, `Architecture/03,06,08,09,10` | 1–3 | docs |
| `CHANGELOG.org` | 1, 2 | upgrade notes |

---

# MR 1: secrets at rest (branch `secrets-at-rest`, closes #273)

### Task 1.1: `internal/seal`

**Files:**
- Create: `internal/seal/seal.go`
- Test: `internal/seal/seal_test.go`

**Interfaces:**
- Produces:
  - `const Prefix = "gbs1:"`
  - `type Key struct { ID string; Secret []byte }`
  - `func NewKey() (Key, error)`
  - `func ReadKeys(path string) ([]Key, error)` — refuses a mode with any group/other bit
  - `func WriteKeys(path string, keys []Key) error` — atomic, 0600, keeps an existing file's owner
  - `type Keyring`; `func Load(path string) (*Keyring, error)`
  - `func (k *Keyring) Seal(aad, plain string) (string, error)`
  - `func (k *Keyring) Open(aad, sealed string) (string, error)`
  - `func (k *Keyring) CurrentID() (string, error)`
  - `func IsSealed(v string) bool`, `func KeyID(v string) (string, bool)`

- [ ] **Step 1: Write the failing tests**

```go
package seal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func keyFile(t *testing.T, keys ...Key) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := WriteKeys(path, keys); err != nil {
		t.Fatal(err)
	}
	return path
}

func newKey(t *testing.T) Key {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := newKey(t)
	ring, err := Load(keyFile(t, k))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ring.Seal("build_secrets.value", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, Prefix+k.ID+":") || strings.Contains(v, "hunter2") {
		t.Fatalf("sealed value %q", v)
	}
	if id, ok := KeyID(v); !ok || id != k.ID {
		t.Fatalf("KeyID = %q, %v", id, ok)
	}
	got, err := ring.Open("build_secrets.value", v)
	if err != nil || got != "hunter2" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	// Two seals of one value differ: the nonce is random.
	if w, _ := ring.Seal("build_secrets.value", "hunter2"); w == v {
		t.Fatal("two seals produced the same value")
	}
}

// A value moved to another column does not open there.
func TestOpenChecksAdditionalData(t *testing.T) {
	ring, err := Load(keyFile(t, newKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := ring.Seal("mirrors.token", "tok")
	if _, err := ring.Open("webhooks.secret", v); err == nil {
		t.Fatal("opened under the wrong column")
	}
}

// A running daemon sees a rotation without a restart: the ring re-reads
// the file when it changes.
func TestKeyringFollowsTheFile(t *testing.T) {
	old, next := newKey(t), newKey(t)
	path := keyFile(t, old)
	ring, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := ring.Seal("webhooks.secret", "s")
	if err := WriteKeys(path, []Key{old, next}); err != nil {
		t.Fatal(err)
	}
	after, err := ring.Seal("webhooks.secret", "s")
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := KeyID(after); id != next.ID {
		t.Fatalf("sealed under %s after rotation, want %s", id, next.ID)
	}
	if got, err := ring.Open("webhooks.secret", before); err != nil || got != "s" {
		t.Fatalf("old value after rotation: %q, %v", got, err)
	}
	if err := WriteKeys(path, []Key{next}); err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Open("webhooks.secret", before); err == nil || !strings.Contains(err.Error(), old.ID) {
		t.Fatalf("a retired key's value opened, or the error does not name the key: %v", err)
	}
}

func TestReadKeysRefusesAReadableFile(t *testing.T) {
	path := keyFile(t, newKey(t))
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeys(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("group-readable key file: %v", err)
	}
}

func TestWriteKeysMode(t *testing.T) {
	path := keyFile(t, newKey(t))
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %04o", fi.Mode().Perm())
	}
}

func TestReadKeysRejectsMalformedLines(t *testing.T) {
	for _, body := range []string{
		"",
		"# only a comment\n",
		"XYZ12345 AAAA\n",
		"0123abcd bm90IDMyIGJ5dGVz\n",
	} {
		path := filepath.Join(t.TempDir(), "k")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadKeys(path); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/seal/ -count=1`
Expected: FAIL, package does not compile (`undefined: WriteKeys`).

- [ ] **Step 3: Write the implementation**

```go
// Package seal encrypts the secret columns of the database with
// AES-256-GCM under keys held in a file outside the database and outside
// server.root, so neither a copy of the database nor a backup opens them
// (#273).
package seal

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Prefix marks a sealed value: "gbs1:<key id>:<base64 nonce||ciphertext>".
const Prefix = "gbs1:"

// Key is one line of the key file.
type Key struct {
	ID     string // 8 lowercase hex characters
	Secret []byte // 32 bytes
}

// NewKey returns a key with a random id and secret.
func NewKey() (Key, error) {
	id := make([]byte, 4)
	secret := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return Key{}, err
	}
	if _, err := rand.Read(secret); err != nil {
		return Key{}, err
	}
	return Key{ID: hex.EncodeToString(id), Secret: secret}, nil
}

// ReadKeys reads the key file. The last key seals; every key opens.
func ReadKeys(path string) ([]Key, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s is mode %04o; it must be readable by its owner alone (0600)", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []Key
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !validID(f[0]) {
			return nil, fmt.Errorf("%s:%d: want \"<8 hex id> <base64 32-byte key>\"", path, n)
		}
		secret, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || len(secret) != 32 {
			return nil, fmt.Errorf("%s:%d: key is not 32 bytes of base64", path, n)
		}
		if seen[f[0]] {
			return nil, fmt.Errorf("%s:%d: key id %s appears twice", path, n, f[0])
		}
		seen[f[0]] = true
		keys = append(keys, Key{ID: f[0], Secret: secret})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s holds no keys", path)
	}
	return keys, nil
}

// WriteKeys replaces the key file: a temporary file in the same
// directory, mode 0600, given the existing file's owner when there is
// one (rotation runs as root; the daemon reads the file as its own
// user), then renamed over it.
func WriteKeys(path string, keys []Key) error {
	var b strings.Builder
	b.WriteString("# gitbay secret keys, \"<id> <base64 key>\" per line. The last line seals\n")
	b.WriteString("# new values; the others open values sealed before a rotation.\n")
	b.WriteString("# Keep a copy off this host: backups do not carry this file.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %s\n", k.ID, base64.StdEncoding.EncodeToString(k.Secret))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	fail := func(err error) error {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(path); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
				return fail(err)
			}
		}
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Keyring is the loaded key file. It re-reads the file whenever the file
// changes, so a running daemon follows a rotation without a restart.
type Keyring struct {
	path string

	mu   sync.Mutex
	fi   os.FileInfo
	cur  string
	aead map[string]cipher.AEAD
}

func Load(path string) (*Keyring, error) {
	k := &Keyring{path: path}
	if err := k.refresh(); err != nil {
		return nil, err
	}
	return k, nil
}

// refresh reloads the file unless it is the one last read. Callers hold k.mu.
func (k *Keyring) refresh() error {
	fi, err := os.Stat(k.path)
	if err != nil {
		return err
	}
	if k.fi != nil && os.SameFile(k.fi, fi) && fi.ModTime().Equal(k.fi.ModTime()) && fi.Size() == k.fi.Size() {
		return nil
	}
	keys, err := ReadKeys(k.path)
	if err != nil {
		return err
	}
	aead := make(map[string]cipher.AEAD, len(keys))
	for _, key := range keys {
		block, err := aes.NewCipher(key.Secret)
		if err != nil {
			return err
		}
		g, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		aead[key.ID] = g
	}
	k.fi, k.cur, k.aead = fi, keys[len(keys)-1].ID, aead
	return nil
}

// CurrentID is the id of the key that seals new values.
func (k *Keyring) CurrentID() (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.refresh(); err != nil {
		return "", err
	}
	return k.cur, nil
}

// Seal encrypts plain under the current key. aad names the column, so a
// value copied into another column does not open there.
func (k *Keyring) Seal(aad, plain string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.refresh(); err != nil {
		return "", err
	}
	g := k.aead[k.cur]
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := g.Seal(nonce, nonce, []byte(plain), []byte(aad))
	return Prefix + k.cur + ":" + base64.RawStdEncoding.EncodeToString(ct), nil
}

// Open decrypts a value Seal produced under any key the file holds.
func (k *Keyring) Open(aad, sealed string) (string, error) {
	id, body, ok := split(sealed)
	if !ok {
		return "", errors.New("not a sealed value")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.refresh(); err != nil {
		return "", err
	}
	g, ok := k.aead[id]
	if !ok {
		return "", fmt.Errorf("sealed with key %s, which %s does not hold", id, k.path)
	}
	ct, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil || len(ct) < g.NonceSize() {
		return "", fmt.Errorf("value sealed with key %s is malformed", id)
	}
	plain, err := g.Open(nil, ct[:g.NonceSize()], ct[g.NonceSize():], []byte(aad))
	if err != nil {
		return "", fmt.Errorf("value sealed with key %s does not open: wrong key or altered value", id)
	}
	return string(plain), nil
}

// IsSealed reports whether v carries the sealed prefix.
func IsSealed(v string) bool { return strings.HasPrefix(v, Prefix) }

// KeyID is the id of the key that sealed v.
func KeyID(v string) (string, bool) {
	id, _, ok := split(v)
	return id, ok
}

func split(v string) (id, body string, ok bool) {
	rest, ok := strings.CutPrefix(v, Prefix)
	if !ok {
		return "", "", false
	}
	id, body, ok = strings.Cut(rest, ":")
	return id, body, ok && validID(id)
}

func validID(s string) bool {
	if len(s) != 8 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/seal/ -count=1 && go vet ./internal/seal/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/seal
git commit -S -m "seal: AES-256-GCM keyring for secret columns

Ref #273"
```

### Task 1.2: `server.secret_key_file`

**Files:**
- Modify: `internal/config/config.go:4-16` (imports), `:48-57` (`Server`), `:271-273` (`Default`), `:319-335` (`Validate`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.Server.SecretKeyFile string` (`toml:"secret_key_file"`), default `/etc/gitbay/secret.key`.

- [ ] **Step 1: Write the failing test** (append to `config_test.go`)

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestSecretKeyFile -count=1`
Expected: FAIL, `unknown config key "server.secret_key_file"`.

- [ ] **Step 3: Implement**

Add `"path/filepath"` to the imports. In `Server`, after `SiteURL`:

```go
	// SecretKeyFile holds the keys that seal the secret columns of the
	// database (internal/seal). It lives outside Root, so neither a
	// backup archive nor a snapshot of Root carries it.
	SecretKeyFile string `toml:"secret_key_file"`
```

In `Default()`:

```go
		Server: Server{Root: "/var/lib/gitbay", SecretKeyFile: "/etc/gitbay/secret.key"},
```

In `Validate()`, after the `server.site_url is required` check:

```go
	switch {
	case c.Server.SecretKeyFile == "":
		errs = append(errs, errors.New("server.secret_key_file is required"))
	case within(c.Server.Root, c.Server.SecretKeyFile):
		errs = append(errs, fmt.Errorf("server.secret_key_file %q is inside server.root: backups of the root would carry the key beside the values it seals", c.Server.SecretKeyFile))
	}
```

And below `oneOf`:

```go
// within reports whether path is dir or below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/config/ -count=1`
Expected: PASS (existing tests use `minimal`, which gets the default).

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -S -m "config: server.secret_key_file, outside server.root

Ref #273"
```

### Task 1.3: the store seals and opens

**Files:**
- Create: `internal/store/secrets.go`, `internal/store/migrations/0072_push_token_hash.up.sql`, `internal/store/migrations/0072_push_token_hash.down.sql`
- Modify: `internal/store/store.go:23-30` (`Store`), `internal/store/cisecrets.go:3-58`, `internal/store/webhooks.go:41-113`, `internal/store/mirrors.go:23-66`, `internal/store/push.go:32-78,153-202`
- Test: `internal/store/secrets_test.go` (create)

**Interfaces:**
- Consumes: `seal.Keyring`, `seal.IsSealed`, `seal.KeyID`, `seal.NewKey`, `seal.WriteKeys`, `seal.Load` (Task 1.1).
- Produces:
  - `func (s *Store) SetKeyring(k *seal.Keyring)`
  - `func (s *Store) ResealSecrets() (int, error)` — seals clear values, reseals values not under the current key, fills missing `push_devices.token_hash`; one transaction; returns values rewritten.
  - `func (s *Store) SecretKeyUse() (map[string]int, error)` — count per key id (`""` = clear), opening each value.
  - Unchanged signatures for every existing store function; `Mirror.Token`, `Webhook.Secret`, `Delivery.Secret`, `PushDevice.Token`, `QueuedPush.Token` hold plaintext as before.

- [ ] **Step 1: Write the failing tests**

```go
package store

import (
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/seal"
)

// keyedStore is a migrated store with a key file of one key.
func keyedStore(t *testing.T) (*Store, string, int64, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret.key")
	k, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := seal.WriteKeys(path, []seal.Key{k}); err != nil {
		t.Fatal(err)
	}
	ring, err := seal.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetKeyring(ring)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, path, uid, repoID
}

// raw reads every stored value of the secret columns.
func raw(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, sc := range secretColumns {
		rows, err := s.DB.Query("SELECT " + sc.column + " FROM " + sc.table + " WHERE " + sc.column + " != ''")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		rows.Close()
	}
	return out
}

func TestSecretColumnsAreSealed(t *testing.T) {
	s, _, uid, repoID := keyedStore(t)
	if err := s.SetBuildSecret(repoID, "DEPLOY", "ci-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWebhook(repoID, "https://hook.example/x", "hook-secret", "*"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMirror(repoID, "push", "https://mirror.example/r.git", "u", "mirror-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPushDevice(uid, "apns-token", "phone"); err != nil {
		t.Fatal(err)
	}

	vals := raw(t, s)
	if len(vals) != 4 {
		t.Fatalf("stored %d values, want 4: %v", len(vals), vals)
	}
	for _, v := range vals {
		if !seal.IsSealed(v) {
			t.Errorf("stored in clear: %q", v)
		}
		for _, plain := range []string{"ci-secret", "hook-secret", "mirror-token", "apns-token"} {
			if strings.Contains(v, plain) {
				t.Errorf("%q carries %q", v, plain)
			}
		}
	}

	secrets, err := s.BuildSecrets(repoID)
	if err != nil || secrets["DEPLOY"] != "ci-secret" {
		t.Fatalf("BuildSecrets = %v, %v", secrets, err)
	}
	hooks, err := s.ListWebhooks(repoID)
	if err != nil || len(hooks) != 1 || hooks[0].Secret != "hook-secret" {
		t.Fatalf("ListWebhooks = %+v, %v", hooks, err)
	}
	ms, err := s.ListMirrors(repoID)
	if err != nil || len(ms) != 1 || ms[0].Token != "mirror-token" {
		t.Fatalf("ListMirrors = %+v, %v", ms, err)
	}
	ds, err := s.PushDevices(uid)
	if err != nil || len(ds) != 1 || ds[0].Token != "apns-token" {
		t.Fatalf("PushDevices = %+v, %v", ds, err)
	}

	// An empty webhook secret or mirror token stays empty: it means none.
	if _, err := s.AddWebhook(repoID, "https://hook.example/y", "", "*"); err != nil {
		t.Fatal(err)
	}
	var empty int
	s.DB.QueryRow("SELECT COUNT(*) FROM webhooks WHERE secret = ''").Scan(&empty)
	if empty != 1 {
		t.Errorf("empty secret stored as %d rows of ''", empty)
	}
}

// A token re-registered under another account changes hands by its
// hash, since two seals of one token differ.
func TestPushDeviceUpsertBySealedToken(t *testing.T) {
	s, _, uid, _ := keyedStore(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AddPushDevice(uid, "tok", "phone")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddPushDevice(bob, "tok", "ipad")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("re-registration made row %d beside %d", second, first)
	}
	if err := s.DeletePushDeviceByToken("tok"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.PushDevices(bob); len(d) != 0 {
		t.Fatalf("device left after delete by token: %+v", d)
	}
}

// Rows written before sealing existed, and rows under a retired key,
// end up under the current key.
func TestResealSecrets(t *testing.T) {
	s, path, uid, repoID := keyedStore(t)
	if _, err := s.DB.Exec("INSERT INTO build_secrets (repo_id, name, value) VALUES (?, 'OLD', 'clear-value')", repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO push_devices (user_id, token, label) VALUES (?, 'clear-token', '')", uid); err != nil {
		t.Fatal(err)
	}
	n, err := s.ResealSecrets()
	if err != nil || n != 2 {
		t.Fatalf("ResealSecrets = %d, %v; want 2", n, err)
	}
	for _, v := range raw(t, s) {
		if !seal.IsSealed(v) {
			t.Errorf("still clear: %q", v)
		}
	}
	var hash string
	s.DB.QueryRow("SELECT COALESCE(token_hash, '') FROM push_devices").Scan(&hash)
	if hash != tokenHash("clear-token") {
		t.Errorf("token_hash = %q", hash)
	}
	if n, _ := s.ResealSecrets(); n != 0 {
		t.Errorf("second reseal rewrote %d values", n)
	}

	// Rotation: add a key, reseal, drop the old key; the value still opens.
	old, err := seal.ReadKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := seal.NewKey()
	if err := seal.WriteKeys(path, append(old, next)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResealSecrets(); err != nil || n != 2 {
		t.Fatalf("reseal after rotation = %d, %v; want 2", n, err)
	}
	if err := seal.WriteKeys(path, []seal.Key{next}); err != nil {
		t.Fatal(err)
	}
	use, err := s.SecretKeyUse()
	if err != nil || use[next.ID] != 2 || len(use) != 1 {
		t.Fatalf("SecretKeyUse = %v, %v", use, err)
	}
	if got, _ := s.BuildSecrets(repoID); got["OLD"] != "clear-value" {
		t.Fatalf("value after rotation: %v", got)
	}
}

func TestSealedValueWithoutKeyFails(t *testing.T) {
	s, _, _, repoID := keyedStore(t)
	if err := s.SetBuildSecret(repoID, "X", "v"); err != nil {
		t.Fatal(err)
	}
	s.SetKeyring(nil)
	if _, err := s.BuildSecrets(repoID); err == nil {
		t.Fatal("opened a sealed value with no key loaded")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/store/ -run 'TestSecretColumnsAreSealed|TestPushDeviceUpsertBySealedToken|TestResealSecrets|TestSealedValueWithoutKeyFails' -count=1`
Expected: FAIL to compile, `s.SetKeyring undefined`.

- [ ] **Step 3: Migration 0072**

`internal/store/migrations/0072_push_token_hash.up.sql`:

```sql
-- APNs tokens are sealed with a random nonce (internal/seal), so two
-- stores of one token differ; lookups and the re-registration upsert go
-- by this SHA-256 of the token instead. Rows from before it are filled
-- by Store.ResealSecrets when the daemon starts.
ALTER TABLE push_devices ADD COLUMN token_hash TEXT;
CREATE UNIQUE INDEX push_devices_token_hash ON push_devices(token_hash);
```

`internal/store/migrations/0072_push_token_hash.down.sql`:

```sql
DROP INDEX push_devices_token_hash;
ALTER TABLE push_devices DROP COLUMN token_hash;
```

- [ ] **Step 4: `Store.secrets` and `internal/store/secrets.go`**

In `store.go`, add the import `"gitbay.org/gitbay/internal/seal"` and a field on `Store` after `logWait`:

```go
	// secrets seals and opens the secret columns (secrets.go). Nil
	// stores values as given; only tests leave it nil, since openStore
	// in cmd/gitbayd refuses to run without a key file.
	secrets *seal.Keyring
```

`internal/store/secrets.go`:

```go
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"gitbay.org/gitbay/internal/seal"
)

// The additional data of each sealed value is its "<table>.<column>".
const (
	aadBuildSecret = "build_secrets.value"
	aadWebhook     = "webhooks.secret"
	aadMirror      = "mirrors.token"
	aadPushToken   = "push_devices.token"
)

type secretColumn struct{ table, column string }

func (c secretColumn) aad() string { return c.table + "." + c.column }

// secretColumns are the columns sealed under the key file (#273).
var secretColumns = []secretColumn{
	{"build_secrets", "value"},
	{"webhooks", "secret"},
	{"mirrors", "token"},
	{"push_devices", "token"},
}

func (s *Store) SetKeyring(k *seal.Keyring) { s.secrets = k }

// sealValue seals v for storage. An empty value stays empty: for
// webhooks and mirrors it means there is no secret.
func (s *Store) sealValue(aad, v string) (string, error) {
	if s.secrets == nil || v == "" {
		return v, nil
	}
	return s.secrets.Seal(aad, v)
}

// openValue returns a stored value in clear. A value not yet sealed is
// returned as stored: rows from before sealing existed stay readable
// until ResealSecrets reaches them.
func (s *Store) openValue(aad, v string) (string, error) {
	if !seal.IsSealed(v) {
		return v, nil
	}
	if s.secrets == nil {
		return "", errors.New("value is sealed and no secret key is loaded")
	}
	return s.secrets.Open(aad, v)
}

// tokenHash is the lookup key for a push device token.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type secretRow struct {
	rowid int64
	value string
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func secretRows(q queryer, c secretColumn) ([]secretRow, error) {
	rows, err := q.Query(fmt.Sprintf("SELECT rowid, %s FROM %s WHERE %s != ''", c.column, c.table, c.column))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretRow
	for rows.Next() {
		var r secretRow
		if err := rows.Scan(&r.rowid, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResealSecrets seals every clear value in the secret columns and
// reseals every value not under the key file's current key, then fills
// push_devices.token_hash where it is missing. It runs in one write
// transaction: every store write of a secret seals inside its own
// transaction, so a write either lands before this one and is resealed,
// or after it and is sealed under the key this one saw. It returns how
// many values it rewrote.
func (s *Store) ResealSecrets() (int, error) {
	if s.secrets == nil {
		return 0, errors.New("no secret key loaded")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cur, err := s.secrets.CurrentID()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range secretColumns {
		rows, err := secretRows(tx, c)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			if id, ok := seal.KeyID(r.value); ok && id == cur {
				continue
			}
			plain, err := s.openValue(c.aad(), r.value)
			if err != nil {
				return 0, fmt.Errorf("%s row %d: %w", c.aad(), r.rowid, err)
			}
			sealed, err := s.secrets.Seal(c.aad(), plain)
			if err != nil {
				return 0, err
			}
			if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE rowid = ?", c.table, c.column), sealed, r.rowid); err != nil {
				return 0, err
			}
			n++
		}
	}
	rows, err := tx.Query("SELECT id, token FROM push_devices WHERE token_hash IS NULL")
	if err != nil {
		return 0, err
	}
	var missing []secretRow
	for rows.Next() {
		var r secretRow
		if err := rows.Scan(&r.rowid, &r.value); err != nil {
			rows.Close()
			return 0, err
		}
		missing = append(missing, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range missing {
		plain, err := s.openValue(aadPushToken, r.value)
		if err != nil {
			return 0, fmt.Errorf("push_devices row %d: %w", r.rowid, err)
		}
		if _, err := tx.Exec("UPDATE push_devices SET token_hash = ? WHERE id = ?", tokenHash(plain), r.rowid); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// SecretKeyUse counts the values in the secret columns by the id of the
// key that sealed them ("" for a value still in clear), opening each
// one, so a wrong or incomplete key file is an error naming the row.
func (s *Store) SecretKeyUse() (map[string]int, error) {
	use := map[string]int{}
	for _, c := range secretColumns {
		rows, err := secretRows(s.DB, c)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if _, err := s.openValue(c.aad(), r.value); err != nil {
				return nil, fmt.Errorf("%s row %d: %w", c.aad(), r.rowid, err)
			}
			id, _ := seal.KeyID(r.value)
			use[id]++
		}
	}
	return use, nil
}
```

- [ ] **Step 5: Seal in the writers, open in the readers**

`cisecrets.go` — replace `SetBuildSecret` and `BuildSecrets`:

```go
// SetBuildSecret stores or replaces one secret. The value never leaves the
// server except inside a claimed build's environment. It is sealed inside
// the write transaction; see ResealSecrets.
func (s *Store) SetBuildSecret(repoID int64, name, value string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sealed, err := s.sealValue(aadBuildSecret, value)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO build_secrets (repo_id, name, value) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, name) DO UPDATE SET value = excluded.value`,
		repoID, name, sealed); err != nil {
		return err
	}
	return tx.Commit()
}
```

```go
// BuildSecrets returns the values, for injection into a claimed build.
func (s *Store) BuildSecrets(repoID int64) (map[string]string, error) {
	rows, err := s.DB.Query("SELECT name, value FROM build_secrets WHERE repo_id = ?", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			return nil, err
		}
		if out[n], err = s.openValue(aadBuildSecret, v); err != nil {
			return nil, fmt.Errorf("build secret %s: %w", n, err)
		}
	}
	return out, rows.Err()
}
```

(add `import "fmt"` to `cisecrets.go`).

`webhooks.go` — `AddWebhook`:

```go
func (s *Store) AddWebhook(repoID int64, url, secret, events string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	sealed, err := s.sealValue(aadWebhook, secret)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		"INSERT INTO webhooks (repo_id, url, secret, events) VALUES (?, ?, ?, ?)",
		repoID, url, sealed, events)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
```

In `ListWebhooks`, after the `rows.Scan(...)` of `&w.Secret`:

```go
		if w.Secret, err = s.openValue(aadWebhook, w.Secret); err != nil {
			return nil, fmt.Errorf("webhook %d: %w", w.ID, err)
		}
```

In `DueDeliveries`, after its `rows.Scan(...)`:

```go
		if d.Secret, err = s.openValue(aadWebhook, d.Secret); err != nil {
			return nil, fmt.Errorf("webhook %d: %w", d.WebhookID, err)
		}
```

(`webhooks.go` imports `"fmt"` and `"time"`.)

`mirrors.go` — `AddMirror`:

```go
func (s *Store) AddMirror(repoID int64, direction, url, username, token string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	sealed, err := s.sealValue(aadMirror, token)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		"INSERT INTO mirrors (repo_id, direction, url, username, token) VALUES (?, ?, ?, ?, ?)",
		repoID, direction, url, username, sealed)
	if err != nil {
		if isUniqueErr(err) {
			return 0, ErrExists
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
```

`scanMirror` becomes a method that opens the token, and `mirrorQuery` calls `s.scanMirror(rows)`:

```go
func (s *Store) scanMirror(row interface{ Scan(...any) error }) (Mirror, error) {
	var m Mirror
	if err := row.Scan(&m.ID, &m.RepoID, &m.Direction, &m.URL, &m.Username, &m.Token,
		&m.Dirty, &m.LastSync, &m.LastError); err != nil {
		return m, err
	}
	var err error
	if m.Token, err = s.openValue(aadMirror, m.Token); err != nil {
		return m, fmt.Errorf("mirror %d: %w", m.ID, err)
	}
	return m, nil
}
```

(`mirrors.go` imports `"errors"` and `"fmt"`.)

`push.go` — `AddPushDevice` body between `defer tx.Rollback()` and `if prev != 0 ...`:

```go
	h := tokenHash(token)
	var prev int64
	if err := tx.QueryRow("SELECT user_id FROM push_devices WHERE token_hash = ?", h).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	sealed, err := s.sealValue(aadPushToken, token)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT INTO push_devices (user_id, token, token_hash, label) VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET user_id = excluded.user_id, label = excluded.label`,
		userID, sealed, h, label); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow("SELECT id FROM push_devices WHERE token_hash = ?", h).Scan(&id); err != nil {
		return 0, err
	}
```

Extend the doc comment's second sentence: "The token is sealed (secrets.go), so the lookup and the upsert go by its hash."

`PushDevices`, after `rows.Scan(...)`:

```go
		if d.Token, err = s.openValue(aadPushToken, d.Token); err != nil {
			return nil, fmt.Errorf("push device %d: %w", d.ID, err)
		}
```

`DuePush`, after `rows.Scan(...)`:

```go
		if p.Token, err = s.openValue(aadPushToken, p.Token); err != nil {
			return nil, fmt.Errorf("push device %d: %w", p.DeviceID, err)
		}
```

`DeletePushDeviceByToken`:

```go
	_, err := s.DB.Exec("DELETE FROM push_devices WHERE token_hash = ?", tokenHash(token))
```

(`push.go` adds `"fmt"` to its imports.)

- [ ] **Step 6: Run the store tests**

Run: `go test ./internal/store/ -count=1`
Expected: PASS, including `TestMigrateUpDown` (0072 down drops the index before the column) and the existing `TestPushDevices` (nil keyring, hash lookups).

- [ ] **Step 7: Build and vet everything above the store**

Run: `go build ./... && go vet ./...`
Expected: no output. No caller changed signature.

- [ ] **Step 8: Commit**

```bash
git add internal/store
git commit -S -m "store: seal CI secrets, webhook secrets, mirror tokens and device tokens

Values are AES-256-GCM under the key file; push devices are looked up
by token hash (migration 0072).

Ref #273"
```

### Task 1.4: `openStore` loads the key; `serve` reseals; `admin secrets`

**Files:**
- Modify: `cmd/gitbayd/main.go:40-66` (`openStore`), `:138-142` (`serve`), `:408-422` (`adminCmd`)
- Create: `cmd/gitbayd/secrets.go`, `cmd/gitbayd/testconfig_test.go`, `cmd/gitbayd/secrets_test.go`
- Modify tests: `cmd/gitbayd/main_test.go:17`, `cmd/gitbayd/backup_test.go:46-47`

**Interfaces:**
- Consumes: `seal.Load`, `seal.ReadKeys`, `seal.WriteKeys`, `seal.NewKey`, `Store.SetKeyring`, `Store.ResealSecrets`, `Store.SecretKeyUse`.
- Produces: `func testConfig(t *testing.T) config.Config` (test helper, cmd/gitbayd), `func rotateSecrets(cfg config.Config) error`, `func secretsCmd() *cobra.Command`.

- [ ] **Step 1: The test helper and failing tests**

`cmd/gitbayd/testconfig_test.go`:

```go
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
```

In `main_test.go:17` replace the config line with `cfg := testConfig(t)`.
In `backup_test.go:46-47` replace the two lines with:

```go
	cfg := testConfig(t)
	root := cfg.Server.Root
```

Both files then no longer use `internal/config`; drop that import from
each (MR 2 adds it back to `backup_test.go` for `TestArchivePath`).

`cmd/gitbayd/secrets_test.go`:

```go
package main

import (
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/seal"
)

func TestOpenStoreRefusesWithoutKeyFile(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.SecretKeyFile = filepath.Join(t.TempDir(), "absent.key")
	_, err := openStore(cfg)
	if err == nil || !strings.Contains(err.Error(), "gitbayd admin secrets init") {
		t.Fatalf("openStore without a key file: %v", err)
	}
}

func TestRotateSecrets(t *testing.T) {
	cfg := testConfig(t)
	before, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
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
	st.Close()

	if err := rotateSecrets(cfg); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	after, err := seal.ReadKeys(cfg.Server.SecretKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID == before[0].ID {
		t.Fatalf("key file after rotation holds %v, before %v", after, before)
	}
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/gitbayd/ -run 'TestOpenStore|TestRotateSecrets|TestBackupDBOnly' -count=1`
Expected: FAIL to compile, `undefined: rotateSecrets`.

- [ ] **Step 3: `openStore` and `serve`**

`openStore` begins with the key file (add imports `"errors"`, `"io/fs"` and `"gitbay.org/gitbay/internal/seal"`):

```go
func openStore(cfg config.Config) (*store.Store, error) {
	// The key file seals the secret columns (#273). Without it the
	// database's secrets cannot be read or written, so nothing that
	// opens the database runs.
	keys, err := seal.Load(cfg.Server.SecretKeyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("secret key file %s does not exist: create it with gitbayd admin secrets init, or restore it from its off-host copy (backups do not carry it)", cfg.Server.SecretKeyFile)
	}
	if err != nil {
		return nil, fmt.Errorf("secret key file: %w", err)
	}
	s, err := store.Open(filepath.Join(cfg.Server.Root, "gitbay.db"))
	if err != nil {
		return nil, err
	}
	s.SetKeyring(keys)
```

(the rest of the function is unchanged).

In `serve`, after `defer st.Close()` (line 142):

```go
			// Values stored before sealing existed, or under a key a
			// rotation retired, are sealed under the current key before
			// anything reads them. A value the key file cannot open
			// stops the start here rather than failing each delivery.
			if n, err := st.ResealSecrets(); err != nil {
				return fmt.Errorf("sealing secrets: %w", err)
			} else if n > 0 {
				slog.Info("sealed secret values", "count", n)
			}
```

- [ ] **Step 4: `cmd/gitbayd/secrets.go`**

```go
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/seal"
)

// secretsCmd manages the key file that seals CI secrets, webhook
// secrets, mirror tokens and push device tokens in the database.
func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "the key file that seals secrets stored in the database",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "init",
			Short: "create the key file (server.secret_key_file) with one new key",
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load(configPath)
				if err != nil {
					return err
				}
				path := cfg.Server.SecretKeyFile
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s already exists; gitbayd admin secrets rotate replaces its key", path)
				}
				k, err := seal.NewKey()
				if err != nil {
					return err
				}
				if err := seal.WriteKeys(path, []seal.Key{k}); err != nil {
					return err
				}
				fmt.Printf("wrote %s (key %s). Copy it off this host: backups do not carry it, and a restored database's secrets do not open without it.\n", path, k.ID)
				return nil
			},
		},
		&cobra.Command{
			Use:   "rotate",
			Short: "seal every secret under a new key and retire the old ones",
			Long: `Adds a new key to the key file, reseals every value under it in one
transaction, then removes the old keys from the file. A running daemon
re-reads the file when it changes, so no restart is needed. Run as the
user that can replace the key file (root, for /etc/gitbay); the file
keeps its owner. Copy the new file off the host afterwards.`,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load(configPath)
				if err != nil {
					return err
				}
				return rotateSecrets(cfg)
			},
		},
		&cobra.Command{
			Use:   "check",
			Short: "open every sealed value and count them by key",
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load(configPath)
				if err != nil {
					return err
				}
				st, err := openStore(cfg)
				if err != nil {
					return err
				}
				defer st.Close()
				use, err := st.SecretKeyUse()
				if err != nil {
					return err
				}
				ids := make([]string, 0, len(use))
				for id := range use {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					if id == "" {
						fmt.Printf("clear: %d values (sealed when the daemon next starts)\n", use[id])
					} else {
						fmt.Printf("key %s: %d sealed\n", id, use[id])
					}
				}
				if len(ids) == 0 {
					fmt.Println("no secrets stored")
				}
				return nil
			},
		},
	)
	return cmd
}

// rotateSecrets adds a key, reseals under it, then drops the old keys.
// Each step leaves a file that opens every stored value: after the first
// write the file holds old and new keys; the reseal is one transaction;
// the last write happens only after the reseal committed. Interrupted
// anywhere, running it again finishes the job.
func rotateSecrets(cfg config.Config) error {
	path := cfg.Server.SecretKeyFile
	old, err := seal.ReadKeys(path)
	if err != nil {
		return err
	}
	next, err := seal.NewKey()
	if err != nil {
		return err
	}
	if err := seal.WriteKeys(path, append(old, next)); err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.ResealSecrets()
	if err != nil {
		return fmt.Errorf("resealing: %w (the key file holds the old keys and %s; run rotate again)", err, next.ID)
	}
	if err := seal.WriteKeys(path, []seal.Key{next}); err != nil {
		return err
	}
	retired := make([]string, len(old))
	for i, k := range old {
		retired[i] = k.ID
	}
	fmt.Printf("key %s: resealed %d values; retired %s. Copy %s off this host.\n", next.ID, n, strings.Join(retired, ", "), path)
	return nil
}
```

In `adminCmd` add `secretsCmd(),` after `backupCmd(),` (line 417).

- [ ] **Step 5: Run the package tests**

Run: `go test ./cmd/gitbayd/ -count=1 && go vet ./cmd/gitbayd/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/gitbayd
git commit -S -m "gitbayd: load the secret key file; admin secrets init, rotate, check

serve seals values still in clear, or under a retired key, before it
starts listening.

Ref #273"
```

### Task 1.5: e2e harness key and a sealed-at-rest check through a backup

**Files:**
- Modify: `e2e/ssh_test.go:17-27` (`instance`), `:81-117` (`startInstanceWith`)
- Modify: `e2e/acme_test.go:28-39`, `e2e/system_test.go:32-41`, `e2e/backup_test.go:14-157`

**Interfaces:**
- Produces: `instance.keyFile string`.

- [ ] **Step 1: Harness**

Add to `instance`:

```go
	keyFile  string // server.secret_key_file, outside root
```

In `startInstanceWith`, before building `cfg`:

```go
	inst.keyFile = filepath.Join(t.TempDir(), "secret.key")
```

and change the config's `[server]` table and its `Sprintf` arguments:

```go
[server]
root = %q
site_url = "https://gitbay.test"
secret_key_file = %q
```

```go
`, inst.root, inst.keyFile, inst.port, inst.httpPort, inst.gitPort)
```

After writing the config file and before `inst.proc = exec.Command(...)`:

```go
	inst.admin(t, "admin", "secrets", "init")
```

In `acme_test.go` and `system_test.go`, add `secret_key_file = %q` under `site_url` and `inst.keyFile` as the second `Sprintf` argument. In `backup_test.go`'s restore config (line 76-85) do the same.

- [ ] **Step 2: Extend `TestAdminBackup`**

Add `"bytes"` to its imports. After the issue create (line 37):

```go
	// A build secret, to show the archive carries it sealed and the key
	// file not at all.
	if _, errOut, code := inst.ssh(t, aliceKey, "hunter2-at-rest", "repo", "secret", "set", "alice/keep", "DEPLOY_TOKEN"); code != 0 {
		t.Fatalf("secret set: %s", errOut)
	}
```

After the transient-state loop (line 66):

```go
	if strings.Contains(names, "secret.key") {
		t.Fatalf("archive carries the key file:\n%s", names)
	}
	db, err := exec.Command("tar", "-xzOf", archive, "gitbay.db").Output()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(db, []byte("hunter2-at-rest")) {
		t.Fatal("the archived database carries the build secret in clear")
	}
```

After the restored instance answers `whoami` (line 151):

```go
	// With the original key the restored secrets open; with another key
	// they do not.
	if out, err := exec.Command(inst.gitbayd, "--config", config2, "admin", "secrets", "check").CombinedOutput(); err != nil || !strings.Contains(string(out), ": 1 sealed") {
		t.Fatalf("secrets check on the restored instance: %v\n%s", err, out)
	}
	config3 := filepath.Join(root2, "config-wrong-key.toml")
	wrong := strings.Replace(cfg, fmt.Sprintf("secret_key_file = %q", inst.keyFile),
		fmt.Sprintf("secret_key_file = %q", filepath.Join(t.TempDir(), "other.key")), 1)
	if err := os.WriteFile(config3, []byte(wrong), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(inst.gitbayd, "--config", config3, "admin", "secrets", "init").CombinedOutput(); err != nil {
		t.Fatalf("init the wrong key: %v\n%s", err, out)
	}
	if out, err := exec.Command(inst.gitbayd, "--config", config3, "admin", "secrets", "check").CombinedOutput(); err == nil || !strings.Contains(string(out), "does not hold") {
		t.Fatalf("secrets check with the wrong key: %v\n%s", err, out)
	}
```

- [ ] **Step 3: Run the one e2e test**

Run: `go test ./e2e -run TestAdminBackup -count=1`
Expected: PASS. (The other e2e tests pick up the harness change in CI.)

- [ ] **Step 4: Commit**

```bash
git add e2e
git commit -S -m "e2e: a key file per instance; the archive carries secrets sealed and no key

Ref #273"
```

### Task 1.6: install.sh, wiki, changelog

**Files:**
- Modify: `deploy/install.sh:12-20`
- Modify: `.gitbay/wiki/Admin.org` (Install block lines 17-22, `** [server]` 55-64, a new `** Secret key` under `* Backup and restore`)
- Modify: `.gitbay/wiki/Architecture/06-Data-and-Cryptography.org` (inventory rows 17-19, "Outside the database" table, "At rest" table and the paragraph after it, migration count line 5)
- Modify: `.gitbay/wiki/Architecture/03-Deployment.org` (file table, after the `config.toml` row)
- Modify: `.gitbay/wiki/Architecture/09-Controls.org:59`, `.gitbay/wiki/Architecture/10-Known-Gaps.org` (#273 row)
- Modify: `CHANGELOG.org`

- [ ] **Step 1: install.sh creates the key once**

Replace the remote script with:

```sh
ssh -p "$port" "root@$host" '
  set -eu
  chmod 755 /usr/local/bin/gitbayd.new
  mv /usr/local/bin/gitbayd.new /usr/local/bin/gitbayd
  /usr/local/bin/gitbayd --config /etc/gitbay/config.toml check-config --no-host-checks
  # The key that seals secrets in the database. Created on the first
  # install, never replaced here; gitbayd refuses to start without it.
  if [ ! -e /etc/gitbay/secret.key ]; then
    /usr/local/bin/gitbayd --config /etc/gitbay/config.toml admin secrets init
    chown gitbay:gitbay /etc/gitbay/secret.key
  fi
  systemctl restart gitbayd
  sleep 1
  systemctl --no-pager --lines=5 status gitbayd
'
```

- [ ] **Step 2: Admin.org**

Install block becomes:

```sh
install -m 755 gitbayd /usr/local/bin/
adduser --system --group --home /var/lib/gitbay --shell /usr/sbin/nologin gitbay
install -d -o gitbay -g gitbay -m 750 /var/lib/gitbay
gitbayd --config /etc/gitbay/config.toml check-config
gitbayd --config /etc/gitbay/config.toml admin secrets init
chown gitbay:gitbay /etc/gitbay/secret.key
```

Under `** [server]`, after `site_url`:

```org
- =secret_key_file= (default =/etc/gitbay/secret.key=) — the keys that
  seal CI secrets, webhook secrets, mirror tokens and push device
  tokens in the database. Must be outside =root=, mode 0600, readable
  by the daemon's user. See "Secret key" below.
```

New subsection at the end of `* Backup and restore` (before `* Upgrades`):

```org
** Secret key

CI secrets, webhook secrets, mirror tokens and APNs device tokens are
stored sealed: AES-256-GCM under a key in =server.secret_key_file=,
each value prefixed with the id of the key that sealed it
(=gbs1:<id>:=). The key file is not in the database, not under
=server.root=, and therefore in neither the local archives nor the
restic snapshots. Keep a copy off the host; without it a restored
database's secrets cannot be opened, and gitbayd refuses to start
against them.

#+begin_src sh
gitbayd admin secrets init     # once; deploy/install.sh does it on first install
gitbayd admin secrets check    # open every value, count by key
gitbayd admin secrets rotate   # new key, reseal, retire the old one (as root)
#+end_src

- Missing file: every gitbayd process that opens the database refuses
  to run and names the path, including =serve= and, in system mode,
  =authorized-keys=. =migrate= does not need it.
- Wrong key: =serve= stops at startup naming the first row that does
  not open; =secrets check= does the same without starting anything.
- Upgrade: the first start after the upgrade seals every value still
  in clear and logs =sealed secret values=.
- Rotation: =rotate= adds a key, reseals every value under it in one
  transaction, then removes the old keys. The daemon re-reads the file
  when it changes, so it needs no restart. Copy the new file off the
  host afterwards.
- Push devices are looked up by the SHA-256 of their token
  (=push_devices.token_hash=), since two seals of one token differ.
```

- [ ] **Step 3: Architecture pages**

`06-Data-and-Cryptography.org`: in the inventory, the CI secrets note becomes `sealed (AES-256-GCM)`, Integrations `webhook secret and mirror token sealed`, Notifications `device tokens sealed; looked up by SHA-256`. Add a row to "Outside the database":

```org
| Secret key file            | =server.secret_key_file= (=/etc/gitbay/secret.key=) | C |
```

In "At rest", replace the secrets row with:

```org
| CI secrets, webhook secrets, mirror tokens, APNs device tokens | AES-256-GCM under a key file outside the database and outside =server.root=; additional data is the column name; key id on each value (=internal/seal=, =internal/store/secrets.go=) |
```

and replace the paragraph "The code base contains no symmetric encryption. ..." with:

```org
The database file or a backup read by anyone other than the =gitbay=
user discloses no CI secret, webhook secret, mirror token or device
token without the key file, which neither carries. Rotation:
=gitbayd admin secrets rotate= (Admin wiki).
```

Update line 5's migration count to the number of files in
`internal/store/migrations/` divided by two after this MR.

`03-Deployment.org`, file table, after the `config.toml` row:

```org
| =/etc/gitbay/secret.key=          | keys sealing secret columns                | 0600, owner =gitbay= (=deploy/install.sh=) |
```

`09-Controls.org:59`:

```org
| Secrets encrypted at rest                   | in place | AES-256-GCM, key file outside the database and backups (=internal/seal=) |
```

`10-Known-Gaps.org`: delete the `#273` row.

- [ ] **Step 4: CHANGELOG.org**

If the file has no `* Unreleased` heading above the latest version, add one under the header paragraph; under it:

```org
*Upgrade note.* gitbayd needs =server.secret_key_file= (default
=/etc/gitbay/secret.key=) and refuses to start without it. Before
replacing the binary, run =gitbayd admin secrets init= as root and
=chown gitbay:gitbay /etc/gitbay/secret.key= (=deploy/install.sh= does
both when the file is missing). The first start seals the stored
secrets. Copy the key file off the host: backups do not carry it.

- CI secrets, webhook secrets, mirror tokens and push device tokens are
  stored sealed with AES-256-GCM (#273). =gitbayd admin secrets
  init|rotate|check=.
```

- [ ] **Step 5: Verify and commit**

Run: `go build ./... && go vet ./... && go test ./internal/seal/ ./internal/config/ ./internal/store/ ./cmd/gitbayd/ -count=1`
Expected: PASS.

```bash
git add deploy/install.sh .gitbay/wiki CHANGELOG.org
git commit -S -m "deploy, wiki: provision and document the secret key file

Closes #273"
```

- [ ] **Step 6: MR**

```bash
git push -u origin secrets-at-rest
gitbay mr create --source secrets-at-rest --target main --title "Seal secret columns under a key file outside the database"
```

After CI is green: `gitbay mr merge <n> --strategy ff`, delete the
branch locally and remotely. The operator steps for bay1 are in
"Operator runbook", part A.

---

# MR 2: encrypted backup archives (branch `backup-age`, closes #274)

### Task 2.1: `[backup] age_recipients`

**Files:**
- Modify: `go.mod`, `go.sum` (`filippo.io/age`)
- Modify: `internal/config/config.go` (`Config`, new `Backup` type, `Validate`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.Backup Backup` (`toml:"backup"`), `type Backup struct { AgeRecipients []string }`, `func (b Backup) Recipients() ([]age.Recipient, error)`.

- [ ] **Step 1: Add the dependency**

Run: `go get filippo.io/age@latest && go mod tidy`
Expected: `filippo.io/age` in `go.mod`'s require block. Record the version in the commit message.

- [ ] **Step 2: Write the failing test**

```go
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
```

(add `"filippo.io/age"` to the test imports).

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestBackupRecipients -count=1`
Expected: FAIL, `cfg.Backup undefined`.

- [ ] **Step 4: Implement**

In `Config`, after `Push`:

```go
	Backup       Backup       `toml:"backup"`
```

After the `Push` type's methods:

```go
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
```

In `Validate`, before `// Contradictions.`:

```go
	if _, err := c.Backup.Recipients(); err != nil {
		errs = append(errs, err)
	}
```

Import `"filippo.io/age"`.

- [ ] **Step 5: Run and commit**

Run: `go test ./internal/config/ -count=1`
Expected: PASS.

```bash
git add go.mod go.sum internal/config
v=$(go list -m -f '{{.Version}}' filippo.io/age)
git commit -S -m "config: [backup] age_recipients (filippo.io/age $v)

Ref #274"
```

### Task 2.2: age-wrapped archives and `--verify --identity`

**Files:**
- Modify: `cmd/gitbayd/backup.go:28-64` (`backupCmd`), `:66-151` (`runBackup`), `:187-197` (`verifyBackup` opening)
- Test: `cmd/gitbayd/backup_test.go`

**Interfaces:**
- Consumes: `cfg.Backup.Recipients()` (Task 2.1), `testConfig` (Task 1.4).
- Produces: `func archivePath(out string, cfg config.Config, now time.Time) string`; `func verifyBackup(path, identity string) error` (was `verifyBackup(path string)`); `func archiveReader(f io.Reader, path, identity string) (io.Reader, error)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBackupEncryptedToAgeRecipient(t *testing.T) {
	cfg := testConfig(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Backup.AgeRecipients = []string{id.Recipient().String()}
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	out := filepath.Join(t.TempDir(), "b.tar.gz.age")
	if err := runBackup(cfg, out, true); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 22)
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(f, head)
	f.Close()
	if string(head) != "age-encryption.org/v1\n" {
		t.Fatalf("archive is not age-encrypted: %q", head)
	}

	if err := verifyBackup(out, ""); err == nil || !strings.Contains(err.Error(), "--identity") {
		t.Fatalf("verify without an identity: %v", err)
	}
	idFile := filepath.Join(t.TempDir(), "backup-identity.txt")
	if err := os.WriteFile(idFile, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(out, idFile); err != nil {
		t.Fatalf("verify with the identity: %v", err)
	}
	other, _ := age.GenerateX25519Identity()
	otherFile := filepath.Join(t.TempDir(), "other.txt")
	os.WriteFile(otherFile, []byte(other.String()+"\n"), 0o600)
	if err := verifyBackup(out, otherFile); err == nil {
		t.Fatal("verify with another identity succeeded")
	}
}

func TestArchivePath(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	plain := testConfig(t)
	enc := plain
	enc.Backup.AgeRecipients = []string{"age1x"}
	for _, c := range []struct {
		out  string
		cfg  config.Config
		want string
	}{
		{"", plain, "gitbay-backup-20260927-090000.tar.gz"},
		{"", enc, "gitbay-backup-20260927-090000.tar.gz.age"},
		{"/b/x.tar.gz", enc, "/b/x.tar.gz.age"},
		{"/b/x.tar.gz.age", enc, "/b/x.tar.gz.age"},
		{"/b/x.tar.gz", plain, "/b/x.tar.gz"},
	} {
		if got := archivePath(c.out, c.cfg, now); got != c.want {
			t.Errorf("archivePath(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}
```

(add `"filippo.io/age"`, `"strings"`, `"time"` and
`"gitbay.org/gitbay/internal/config"` to the test imports).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/gitbayd/ -run 'TestBackupEncrypted|TestArchivePath' -count=1`
Expected: FAIL to compile (`undefined: archivePath`; `verifyBackup` takes one argument).

- [ ] **Step 3: Implement**

`backupCmd`: add `identity` to the `var` line, replace the `RunE` body and add a flag:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			if verify != "" {
				return verifyBackup(verify, identity)
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return runBackup(cfg, archivePath(out, cfg, time.Now()), dbOnly)
		},
```

```go
	cmd.Flags().StringVar(&identity, "identity", "", "with --verify: an age identity file that opens an encrypted archive")
```

Append to the `Long` text:

```
With [backup] age_recipients set, the archive is encrypted to those age
public keys and its name ends in .age. --verify then needs --identity
<file> holding a matching private key, which is kept off the host.
```

New function:

```go
// archivePath is where the archive goes: out, or a timestamped name,
// ending in .age when the archive is encrypted.
func archivePath(out string, cfg config.Config, now time.Time) string {
	if out == "" {
		out = fmt.Sprintf("gitbay-backup-%s.tar.gz", now.UTC().Format("20060102-150405"))
	}
	if len(cfg.Backup.AgeRecipients) > 0 && !strings.HasSuffix(out, ".age") {
		out += ".age"
	}
	return out
}
```

In `runBackup`, replace lines 86-87 (`gz := gzip.NewWriter(f)` and `tw := tar.NewWriter(gz)`) with:

```go
	var sink io.Writer = f
	var enc io.WriteCloser
	if len(cfg.Backup.AgeRecipients) > 0 {
		rs, err := cfg.Backup.Recipients()
		if err != nil {
			return err
		}
		if enc, err = age.Encrypt(f, rs...); err != nil {
			return err
		}
		sink = enc
	}
	gz := gzip.NewWriter(sink)
	tw := tar.NewWriter(gz)
```

and after `gz.Close()` (line 137-139):

```go
	if enc != nil {
		if err := enc.Close(); err != nil {
			return err
		}
	}
```

In `verifyBackup`, change the signature to `func verifyBackup(path, identity string) error` and replace `gz, err := gzip.NewReader(f)` with:

```go
	plain, err := archiveReader(f, path, identity)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(plain)
```

New function (import `"bufio"` and `"filippo.io/age"`):

```go
const ageHeader = "age-encryption.org/v1\n"

// archiveReader returns the archive's gzip stream, decrypting it first
// when it is an age file.
func archiveReader(f io.Reader, path, identity string) (io.Reader, error) {
	br := bufio.NewReader(f)
	head, _ := br.Peek(len(ageHeader))
	if string(head) != ageHeader {
		return br, nil
	}
	if identity == "" {
		return nil, fmt.Errorf("%s is encrypted; pass --identity <file> with the private key for one of its recipients", path)
	}
	idf, err := os.Open(identity)
	if err != nil {
		return nil, err
	}
	defer idf.Close()
	ids, err := age.ParseIdentities(idf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", identity, err)
	}
	r, err := age.Decrypt(br, ids...)
	if err != nil {
		return nil, fmt.Errorf("%s: decrypting: %w", path, err)
	}
	return r, nil
}
```

Update the verify doc comment's first sentence to "verifyBackup reads an archive back, decrypting it with identity when it is encrypted:".

- [ ] **Step 4: Run the package tests**

Run: `go test ./cmd/gitbayd/ -count=1 && go vet ./cmd/gitbayd/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gitbayd
git commit -S -m "backup: encrypt archives to [backup] age_recipients; --verify --identity

Ref #274"
```

### Task 2.3: scripts, wiki, changelog

**Files:**
- Modify: `deploy/cloud-init.yaml:96-100` (`age_h`), `:258-259`, `:276-277` (prune globs)
- Modify: `.gitbay/wiki/Admin.org` (Configuration reference: new `** [backup]` after `** [push]`; `* Backup and restore` 373-393)
- Modify: `.gitbay/wiki/Architecture/06-Data-and-Cryptography.org` (At rest, Backups row), `Architecture/08-Operations.org` (Backup table), `Architecture/09-Controls.org:61`, `Architecture/10-Known-Gaps.org` (#274 row)
- Modify: `CHANGELOG.org`

- [ ] **Step 1: cloud-init**

`age_h`:

```sh
      age_h() {
        f=$(ls -t "$1"/*.tar.gz "$1"/*.tar.gz.age 2>/dev/null | head -1)
```

Full backup prune (line 259):

```sh
      ls -1t "$dir"/gitbay-*.tar.gz* | tail -n +8 | xargs -r rm --
```

Database backup prune (line 277):

```sh
      ls -1t "$dir"/gitbay-db-*.tar.gz* | tail -n +49 | xargs -r rm --
```

- [ ] **Step 2: Admin.org**

New `** [backup]` section after `** [push]`:

```org
** [backup]
- =age_recipients= (optional) — age public keys (=age1...=). When set,
  =admin backup= encrypts every archive to them and appends =.age= to
  its name. Generate the pair off the host with =age-keygen=; only the
  public key goes here, so the host writes archives it cannot read.
  The restic copy is unaffected: it snapshots =/var/lib/gitbay=, not
  the archives.
```

In `* Backup and restore`, after the `--verify` paragraph:

```org
With =[backup] age_recipients= set the archive is =<name>.tar.gz.age=
and =--verify= needs the private key:

#+begin_src sh
gitbayd admin backup --verify gitbay-20260927-090000.tar.gz.age --identity ~/.config/gitbay/backup-identity.txt
age -d -i ~/.config/gitbay/backup-identity.txt gitbay-20260927-090000.tar.gz.age | tar -xz -C /new/root
#+end_src

The identity lives off the host (with the secret key file and the
restic credentials), so verifying an encrypted archive happens there
or on a restore host.
```

- [ ] **Step 3: Architecture pages**

`06-Data-and-Cryptography.org`, At rest, Backups row:

```org
| Backups                               | local archives age-encrypted when =[backup] age_recipients= is set; restic encrypts the offsite copy; neither carries the secret key file |
```

`08-Operations.org`, Full archive and Database only rows: append `; age-encrypted when =[backup] age_recipients= is set` to Contents.

`09-Controls.org:61`:

```org
| Local backups encrypted                     | in place | age to =[backup] age_recipients= (=cmd/gitbayd/backup.go=); offsite copy by restic |
```

`10-Known-Gaps.org`: delete the `#274` row.

- [ ] **Step 4: CHANGELOG.org** under `* Unreleased`:

```org
- =gitbayd admin backup= encrypts archives to =[backup] age_recipients=
  when set (#274); =--verify= takes =--identity <file>=. Archive names
  gain =.age=; the shipped backup scripts and monitor match both.
```

- [ ] **Step 5: Verify and commit**

Run: `go build ./... && go vet ./...`
Expected: no output.

```bash
git add deploy/cloud-init.yaml .gitbay/wiki CHANGELOG.org
git commit -S -m "deploy, wiki: encrypted archives in the backup scripts and docs

Closes #274"
git push -u origin backup-age
gitbay mr create --source backup-age --target main --title "Encrypt backup archives to an age recipient"
```

Merge with `--strategy ff` after CI, delete the branch both places.

---

# MR 3: verify connectivity, hold moves during a backup (branch `backup-verify-lock`, ref #259)

### Task 3.1: `gitutil.FsckConnectivity`

**Files:**
- Modify: `internal/gitutil/merge.go` (after `PruneNow`, line 68)
- Test: `internal/gitutil/fsck_test.go` (create)

**Interfaces:**
- Produces: `func FsckConnectivity(dir string) error`.

- [ ] **Step 1: Failing test**

```go
package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFsckConnectivityFindsAMissingObject(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "a\n")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "one")
	if err := FsckConnectivity(dir); err != nil {
		t.Fatalf("intact repository: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD:a.txt").Output()
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(string(out))
	if err := os.Remove(filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if err := FsckConnectivity(dir); err == nil {
		t.Fatal("a repository missing a blob passed")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/gitutil/ -run TestFsckConnectivity -count=1`
Expected: FAIL, `undefined: FsckConnectivity`.

- [ ] **Step 3: Implement** (in `merge.go`, after `PruneNow`)

```go
// FsckConnectivity checks that every object reachable from the
// repository's refs is present, without reading blob contents. A backup
// verify runs it on each archived repository (#259).
func FsckConnectivity(dir string) error {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "fsck", "--connectivity-only", "--no-progress", "--no-dangling")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fsck --connectivity-only: %v\n%s", err, out)
	}
	return nil
}
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/gitutil/ -count=1`
Expected: PASS.

```bash
git add internal/gitutil
git commit -S -m "gitutil: FsckConnectivity

Ref #259"
```

### Task 3.2: `internal/backuplock`

**Files:**
- Create: `internal/backuplock/backuplock.go`, `internal/backuplock/backuplock_test.go`

**Interfaces:**
- Produces: `const Name = "backup.lock"`, `var ErrBusy error`, `func Hold(root string) (func(), error)`, `func TryShared(root string) (func(), error)`.

- [ ] **Step 1: Failing tests**

```go
package backuplock

import (
	"errors"
	"testing"
	"time"
)

func TestTrySharedRefusedWhileHeld(t *testing.T) {
	root := t.TempDir()
	release, err := Hold(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryShared(root); !errors.Is(err, ErrBusy) {
		t.Fatalf("TryShared during a backup: %v", err)
	}
	release()
	r, err := TryShared(root)
	if err != nil {
		t.Fatalf("TryShared after the backup: %v", err)
	}
	r()
}

func TestSharedHoldersCoexist(t *testing.T) {
	root := t.TempDir()
	a, err := TryShared(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := TryShared(root)
	if err != nil {
		t.Fatalf("second shared holder: %v", err)
	}
	b()
}

// A backup waits for a delete already under way.
func TestHoldWaitsForSharedHolder(t *testing.T) {
	root := t.TempDir()
	shared, err := TryShared(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		release, err := Hold(root)
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		close(got)
		release()
	}()
	select {
	case <-got:
		t.Fatal("Hold returned while a shared holder was in")
	case <-time.After(100 * time.Millisecond):
	}
	shared()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("Hold never returned after the shared holder left")
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/backuplock/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

```go
// Package backuplock keeps repository deletes, renames and transfers
// out of a full backup's way (#259). The backup runs in its own process
// (gitbayd admin backup) and a delete in the daemon's, so the lock is
// flock(2) on a file under server.root: the backup holds it exclusively
// from its database snapshot until the last repository is archived, and
// each delete or move holds it shared while it runs.
package backuplock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Name is the lock file under server.root. Backups skip it.
const Name = "backup.lock"

// ErrBusy is TryShared's answer while a backup holds the lock.
var ErrBusy = errors.New("a backup is running; repositories cannot be deleted, renamed or moved until it finishes, usually within minutes")

// open opens the lock file read-only, which is all flock needs, so the
// daemon's user can lock a file a root-run backup created.
func open(root string) (*os.File, error) {
	return os.OpenFile(filepath.Join(root, Name), os.O_RDONLY|os.O_CREATE, 0o644)
}

// Hold takes the lock exclusively, waiting for deletes and moves under
// way to finish. Closing the file releases it.
func Hold(root string) (func(), error) {
	f, err := open(root)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// TryShared takes the lock shared without waiting: ErrBusy while a
// backup holds it.
func TryShared(root string) (func(), error) {
	f, err := open(root)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/backuplock/ -count=1 -race`
Expected: PASS.

```bash
git add internal/backuplock
git commit -S -m "backuplock: flock between a full backup and repository moves

Ref #259"
```

### Task 3.3: deletes and moves refuse during a full backup

**Files:**
- Modify: `internal/control/repo.go` (`runRepoTransfer` 481-538, `runRepoRename` 540-575, `deleteRepo` 611-626; new `holdOffBackup`)
- Modify: `internal/control/org.go` (`runOrgRename` 152-184)
- Test: `internal/control/backuplock_test.go` (create)

**Interfaces:**
- Consumes: `backuplock.TryShared`, `backuplock.Hold`, `backuplock.ErrBusy`.
- Produces: `func holdOffBackup(c *Ctx) (func(), int)` in package control.

- [ ] **Step 1: Failing test**

```go
package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/backuplock"
	"gitbay.org/gitbay/internal/protocol"
)

func TestRepoDeleteAndRenameRefusedDuringBackup(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	owner, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	release, err := backuplock.Hold(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"repo", "rename", repo.Path(), "renamed"},
		{"repo", "delete", repo.Path(), "--yes"},
	} {
		c, errOut := pruneCtx(st, root, owner)
		if code := Dispatch(c, argv); code != protocol.ExitFailure || !strings.Contains(errOut.String(), "a backup is running") {
			t.Fatalf("%v during a backup: exit %d, %s", argv, code, errOut)
		}
	}
	if got, err := st.RepoByID(repo.ID); err != nil || got.Name != repo.Name {
		t.Fatalf("repository changed during a backup: %+v, %v", got, err)
	}
	release()

	c, errOut := pruneCtx(st, root, owner)
	if code := Dispatch(c, []string{"repo", "delete", repo.Path(), "--yes"}); code != protocol.ExitOK {
		t.Fatalf("delete after the backup: exit %d, %s", code, errOut)
	}
}
```

Transfer and org rename get the same call; the test covers rename and
delete because a transfer target needs an org fixture this test does
not build, and the call is identical.

- [ ] **Step 2: Run it**

Run: `go test ./internal/control/ -run TestRepoDeleteAndRenameRefusedDuringBackup -count=1`
Expected: FAIL, rename exits 0 during the backup.

- [ ] **Step 3: Implement**

`repo.go`, import `"gitbay.org/gitbay/internal/backuplock"` and add after `deleteRepo`:

```go
// holdOffBackup keeps a full backup from starting while a repository
// directory moves or goes, and refuses while one runs: the backup's
// database snapshot names every repository its walk then archives
// (#259). The caller defers the returned release.
func holdOffBackup(c *Ctx) (func(), int) {
	release, err := backuplock.TryShared(c.Cfg.Server.Root)
	if err != nil {
		return nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	return release, -1
}
```

Call it with this block:

```go
	release, code := holdOffBackup(c)
	if code >= 0 {
		return code
	}
	defer release()
```

- `deleteRepo`: first statement of the function.
- `runRepoRename`: after the `os.Stat(newDir)` check, before `c.Store.RenameRepo` (line 560). `code` is already declared there by `resolveRepo`, so this call uses `lockCode`:

```go
	release, lockCode := holdOffBackup(c)
	if lockCode >= 0 {
		return lockCode
	}
	defer release()
```

- `runRepoTransfer`: the same `lockCode` block after the `os.Stat(newDir)` check, before `os.MkdirAll` (line 522).
- `org.go` `runOrgRename`: the same `lockCode` block after its `os.Stat(newDir)` check, before `c.Store.RenameOrg` (line 170).

Repository creation, forks and imports are not held: a repository the
snapshot does not name is reported by `--verify` as extra, which is
harmless.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/control/ -count=1 && go vet ./internal/control/`
Expected: PASS.

```bash
git add internal/control
git commit -S -m "control: repository delete, rename, transfer and org rename wait out a full backup

Ref #259"
```

### Task 3.4: the backup holds the lock; `--verify` checks connectivity

**Files:**
- Modify: `cmd/gitbayd/backup.go` (`runBackup`, `verifyBackup`)
- Test: `cmd/gitbayd/backup_test.go`

**Interfaces:**
- Consumes: `backuplock.Hold`, `backuplock.Name`, `gitutil.FsckConnectivity`, `archiveReader` (Task 2.2).
- Produces: `verifyBackup(path, identity string) error` now also runs `FsckConnectivity` per repository.

- [ ] **Step 1: Failing tests**

```go
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// verify runs git's connectivity check on every repository the
// database names: a repository missing an object fails it.
func TestVerifyChecksConnectivity(t *testing.T) {
	cfg := testConfig(t)
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("krz", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "thing", "public"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	work := t.TempDir()
	gitIn(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "a.txt")
	gitIn(t, work, "commit", "-q", "-m", "one")
	dir := filepath.Join(cfg.Server.Root, "repos", "krz", "thing.git")
	gitIn(t, work, "clone", "-q", "--bare", work, dir)

	good := filepath.Join(t.TempDir(), "good.tar.gz")
	if err := runBackup(cfg, good, false); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(good, ""); err != nil {
		t.Fatalf("intact archive: %v", err)
	}

	blob := gitIn(t, dir, "rev-parse", "HEAD:a.txt")
	if err := os.Remove(filepath.Join(dir, "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := runBackup(cfg, bad, false); err != nil {
		t.Fatal(err)
	}
	err = verifyBackup(bad, "")
	if err == nil || !strings.Contains(err.Error(), "krz/thing") || !strings.Contains(err.Error(), "connectivity") {
		t.Fatalf("archive with a missing blob: %v", err)
	}
}

// A full backup waits for a delete under way, and does not archive its
// own lock file.
func TestFullBackupWaitsForRepositoryMoves(t *testing.T) {
	cfg := testConfig(t)
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	inFlight, err := backuplock.TryShared(cfg.Server.Root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	done := make(chan error, 1)
	go func() { done <- runBackup(cfg, out, false) }()
	select {
	case err := <-done:
		t.Fatalf("backup finished while a delete held the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	inFlight()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("backup never started after the delete finished")
	}
	for _, n := range members(t, out) {
		if n == backuplock.Name {
			t.Fatalf("archive carries %s", n)
		}
	}
}
```

(`backup_test.go` imports gain `"os/exec"` and `"gitbay.org/gitbay/internal/backuplock"`; `strings` and `time` came with MR 2.)

`TestVerifyChecksConnectivity` relies on a local `git clone --bare`
hardlinking loose objects into `dir`, so removing the blob there leaves
`work` intact. If git packs instead, the `os.Remove` fails and the test
says so.

- [ ] **Step 2: Run them**

Run: `go test ./cmd/gitbayd/ -run 'TestVerifyChecksConnectivity|TestFullBackupWaitsForRepositoryMoves' -count=1`
Expected: FAIL: the bad archive verifies; the backup finishes while the lock is held.

- [ ] **Step 3: `runBackup` holds the lock**

At the top of `runBackup`, before `openStore`:

```go
	// Deletes, renames and transfers wait until the walk finishes, so
	// every repository the snapshot names is still on disk when the walk
	// reaches it (#259). A database-only archive reads no repository.
	if !dbOnly {
		release, err := backuplock.Hold(cfg.Server.Root)
		if err != nil {
			return fmt.Errorf("backup lock: %w", err)
		}
		defer release()
	}
```

Add `backuplock.Name: true` to the `skip` map.

- [ ] **Step 4: `verifyBackup` extracts and checks repositories**

Replace the function (keeping `archiveReader` from Task 2.2):

```go
// verifyBackup reads an archive back, decrypting it with identity when
// it is encrypted: the database snapshot must pass SQLite's integrity
// check, every repository it names must be in the archive, and each of
// those must pass git fsck --connectivity-only. Repositories are
// extracted to a temporary directory for the check, so it needs free
// space for them. A database-only archive is checked for integrity
// alone and says so.
func verifyBackup(path, identity string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	plain, err := archiveReader(f, path, identity)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return fmt.Errorf("%s: not a gzip archive: %w", path, err)
	}
	tr := tar.NewReader(gz)
	tmp, err := os.MkdirTemp("", "gitbay-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dbPath := ""
	inArchive := map[string]bool{}
	members := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: archive damaged after %d members: %w", path, members, err)
		}
		members++
		switch {
		case h.Name == "gitbay.db":
			dbPath = filepath.Join(tmp, "gitbay.db")
			if err := extractTo(tr, dbPath); err != nil {
				return fmt.Errorf("%s: extracting the database: %w", path, err)
			}
		case strings.HasPrefix(h.Name, "repos/"):
			// repos/<owner>/<name>.git/HEAD marks one repository present.
			parts := strings.Split(h.Name, "/")
			if len(parts) == 4 && parts[3] == "HEAD" && strings.HasSuffix(parts[2], ".git") {
				inArchive[parts[1]+"/"+strings.TrimSuffix(parts[2], ".git")] = true
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			if !filepath.IsLocal(h.Name) {
				return fmt.Errorf("%s: member %q leaves the archive root", path, h.Name)
			}
			if err := extractTo(tr, filepath.Join(tmp, filepath.FromSlash(h.Name))); err != nil {
				return fmt.Errorf("%s: extracting %s: %w", path, h.Name, err)
			}
		}
	}
	if dbPath == "" {
		return fmt.Errorf("%s: no gitbay.db in the archive", path)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("%s: database does not open: %w", path, err)
	}
	defer st.Close()
	var integrity string
	if err := st.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("%s: integrity check: %w", path, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%s: database integrity: %s", path, integrity)
	}
	repos, err := st.ListAllRepos()
	if err != nil {
		return err
	}
	if len(inArchive) == 0 {
		fmt.Printf("%s: database only; integrity ok, %d repositories in the database, none in the archive\n", path, len(repos))
		return nil
	}
	var missing []string
	for _, r := range repos {
		if !inArchive[r.Path()] {
			missing = append(missing, r.Path())
		}
	}
	extra := len(inArchive) - (len(repos) - len(missing))
	fmt.Printf("%s: integrity ok, %d repositories in the database, %d in the archive\n", path, len(repos), len(inArchive))
	if len(missing) > 0 {
		return fmt.Errorf("%s: %d repositories the database names are not in the archive: %s", path, len(missing), strings.Join(missing, ", "))
	}
	if extra > 0 {
		fmt.Printf("%d repositories in the archive that the database does not name (created after the snapshot)\n", extra)
	}
	var broken []string
	for _, r := range repos {
		dir := filepath.Join(tmp, "repos", r.OwnerName, r.Name+".git")
		if err := gitutil.FsckConnectivity(dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", r.Path(), err)
			broken = append(broken, r.Path())
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("%s: %d repositories fail the connectivity check: %s", path, len(broken), strings.Join(broken, ", "))
	}
	fmt.Printf("connectivity ok on %d repositories\n", len(repos))
	return nil
}

// extractTo writes one archive member to dest, owner-only.
func extractTo(r io.Reader, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	w, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
```

Imports gain `"gitbay.org/gitbay/internal/backuplock"` and
`"gitbay.org/gitbay/internal/gitutil"`. The "extra" message changes
from "deleted after the snapshot" to "created after the snapshot",
since a delete can no longer land mid-backup.

Update `backupCmd`'s `--verify` flag text:

```go
	cmd.Flags().StringVar(&verify, "verify", "", "check an archive instead of writing one: database integrity, its repositories against the archive's, and git connectivity of each")
```

- [ ] **Step 5: Run the package tests**

Run: `go test ./cmd/gitbayd/ -count=1 && go vet ./cmd/gitbayd/`
Expected: PASS, including `TestBackupDBOnlyOmitsRepositories` (its repository is not in the database, so no fsck runs on its HEAD-only directory).

- [ ] **Step 6: e2e**

In `e2e/backup_test.go`, after the transient-state checks:

```go
	if out := inst.admin(t, "admin", "backup", "--verify", archive); !strings.Contains(out, "connectivity ok on 1 repositories") {
		t.Fatalf("verify: %s", out)
	}
```

Run: `go test ./e2e -run TestAdminBackup -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/gitbayd e2e/backup_test.go
git commit -S -m "backup: hold repository moves off during a full backup; verify git connectivity

Ref #259"
```

### Task 3.5: wiki: behaviour and the restore drill procedure

**Files:**
- Modify: `.gitbay/wiki/Admin.org` (`* Backup and restore`: the `--verify` paragraph; new `** Restore drill`)
- Modify: `.gitbay/wiki/Architecture/08-Operations.org:55-70`
- Modify: `.gitbay/wiki/Threat-Model.org:218-220`

- [ ] **Step 1: Admin.org**

Replace the `--verify` paragraph with:

```org
=--verify= reads an archive back: the snapshot must pass SQLite's
integrity check, every repository the snapshot names must be in the
archive, and each must pass =git fsck --connectivity-only=. It
extracts the repositories to a temporary directory for that, so it
needs free space the size of the repositories. A database-only archive
is checked for integrity and says so. Exit is non-zero on damage, a
missing repository or a missing object.

A full backup holds =<root>/backup.lock= from its database snapshot to
its last repository. While it runs, =repo delete=, =repo rename=,
=repo transfer=, =admin repo delete= and =org rename= refuse with "a
backup is running"; retry when it finishes. Database-only backups take
no lock.
```

Add a new subsection after `** Secret key`:

```org
** Restore drill

A restore onto a clean host, run on a schedule and recorded below.
The disaster it rehearses is losing bay1, so the local archives are
gone with it and the sources are the offsite restic repository and
what the operator keeps off the host (=~/.config/gitbay/=: =offsite.env=,
=secret.key=, =config.toml=, =backup-identity.txt=). The steps are in
the data-at-rest plan's operator runbook
(=docs/plans/2026-09-27-data-at-rest-and-backup.md=).

Time to service runs from the clean host's first root login to the
first successful =git clone= over SSH from it. The recovery point is
the time of the newest restic snapshot restored.

| Date | Host | Snapshot restored (UTC) | Time to service | DB integrity | Connectivity | LFS | Release assets | Host key | Secrets | Notes |
|------+------+-------------------------+-----------------+--------------+--------------+-----+----------------+----------+---------+-------|
```

- [ ] **Step 2: Architecture/08 and Threat-Model**

`08-Operations.org`: replace the `--verify` bullet (lines 60-62) with:

```org
- =gitbayd admin backup --verify= checks SQLite integrity, that every
  repository the database names is present, and =git fsck
  --connectivity-only= on each (=backup.go=).
- Repository deletes, renames and transfers refuse while a full backup
  runs (=internal/backuplock=), so the snapshot and the walk agree.
```

Replace the "Recovery time" bullet with:

```org
- Recovery time: see the Admin wiki's Restore drill table.
```

`Threat-Model.org:218-220` becomes:

```org
- Backups are consistent per the DB-snapshot-first ordering, and
  repository deletes and moves wait out a full backup; a push during
  one leaves only unreferenced objects (see [[Admin]]).
```

- [ ] **Step 3: Commit and MR**

```bash
git add .gitbay/wiki
git commit -S -m "wiki: backup verify, backup lock, restore drill record

Ref #259"
git push -u origin backup-verify-lock
gitbay mr create --source backup-verify-lock --target main --title "Backup verify checks git connectivity; moves wait out a backup"
```

Merge with `--strategy ff` after CI, delete the branch both places.
#259 stays open until the drill below is recorded.

---

# Operator runbook (cmc)

Run on bay1 and a clean host. Nothing here is automated by the MRs.
One forge write per shell call; bay1 root is `ssh -p 2222 root@gitbay.org`.

## A. After MR 1 deploys

1. `make deploy` runs `deploy/install.sh`, which creates
   `/etc/gitbay/secret.key` (missing on bay1) before the restart.
   Confirm: `ssh -p 2222 root@gitbay.org 'ls -l /etc/gitbay/secret.key; journalctl -u gitbayd -n 50 | grep "sealed secret values"'`
   → mode `-rw-------`, owner `gitbay gitbay`, one log line with a count.
2. `ssh -p 2222 root@gitbay.org 'gitbayd --config /etc/gitbay/config.toml admin secrets check'`
   → one `key <id>: N sealed` line, no `clear:` line.
3. Escrow: `scp -P 2222 root@gitbay.org:/etc/gitbay/secret.key ~/.config/gitbay/secret.key && chmod 600 ~/.config/gitbay/secret.key`.
   Also copy `/etc/gitbay/config.toml` to `~/.config/gitbay/config.toml`
   (mode 600) if no copy exists off the host; the drill needs it.
4. Check CI builds that use secrets (blotter, hutch, orgo) still run,
   and a webhook delivery still verifies.

## B. After MR 2 deploys

1. On the laptop: `age-keygen -o ~/.config/gitbay/backup-identity.txt`
   (mode 600). Note the printed `age1...` public key.
2. On bay1, add to `/etc/gitbay/config.toml`:
   ```toml
   [backup]
   age_recipients = ["age1..."]
   ```
   then `gitbayd --config /etc/gitbay/config.toml check-config --no-host-checks`.
3. bay1's installed `/usr/local/bin/gitbay-backup.sh`,
   `/usr/local/bin/gitbay-db-backup.sh` and `/usr/local/bin/gitbay-monitor.sh`
   predate the cloud-init change (cloud-init runs once). Apply the same
   glob edits as Task 2.3 Step 1 by hand.
4. Before relying on encryption, find how `/var/lib/gitbay-stage` (the
   staged database restic copies) is produced. If it reads a local
   archive, it must decrypt, which the host cannot; switch it to its own
   `VACUUM INTO` or an unencrypted database-only snapshot kept under
   `/var/lib/gitbay-stage` only. If it snapshots the live database
   directly, nothing changes.
5. After the next hourly run: `ls -l /var/backups/gitbay/db | tail -2`
   shows `.tar.gz.age`; the monitor's `db_snapshot_h` stays under 2.
   Copy one archive to the laptop and run
   `gitbayd admin backup --verify <file> --identity ~/.config/gitbay/backup-identity.txt`
   (a local gitbayd build; `--verify` reads no config).
6. Old unencrypted archives age out of the 7/48 rotation on their own.

## C. Restore drill (after MR 3 deploys; closes #259)

Record every timestamp as you go. Start the clock at step 2.

1. Pick the snapshot: `set -a; . ~/.config/gitbay/offsite.env; set +a; restic $RESTIC_OPTS snapshots --latest 1`.
   Note its time (recovery point). `restic $RESTIC_OPTS ls latest /var/lib/gitbay-stage`
   to find the staged database file name.
2. Provision a clean Ubuntu 24.04 host (throwaway VPS or local VM) with
   `deploy/cloud-init.yaml`. First root login: **clock starts**.
3. Before gitbayd ever starts, block outbound traffic so the restored
   instance cannot send mail, deliver webhooks, push mirrors or call
   APNs: `ufw default deny outgoing; ufw allow out 53; ufw allow out to <restic endpoint> port 443; ufw reload`.
   (Allow the restic endpoint only for the restore, then remove it.)
4. Install restic, restore: `restic $RESTIC_OPTS restore latest --target / --include /var/lib/gitbay --include /var/lib/gitbay-stage`.
   Replace `/var/lib/gitbay/gitbay.db` with the staged copy (the live
   file in the snapshot may be mid-write); remove any `gitbay.db-wal`
   and `gitbay.db-shm`. `chown -R gitbay:gitbay /var/lib/gitbay`.
5. Config and key: copy `~/.config/gitbay/config.toml` to
   `/etc/gitbay/config.toml` and `~/.config/gitbay/secret.key` to
   `/etc/gitbay/secret.key` (`chown gitbay:gitbay`, mode 600). In the
   config for the drill only: `site_url` to `http://<drill-ip>:8080`,
   `[http] addr = ":8080"`, `tls = "off"`, remove `[mail]`, set
   `registration.mode = "closed"`, `[push] enabled = false`, remove
   `[backup]` (step 7's archive is local and read back at once). If
   `[lfs] root` is set outside `server.root`, note it: neither the
   archive nor this restore carries those objects.
6. Install the gitbayd binary of the tag bay1 runs (`/healthz` names the
   commit) with `deploy/install.sh <drill-ip> 2222`; it will not create
   a key because one is present.
7. Checks, each recorded in the table:
   - Database integrity and connectivity: as `gitbay`,
     `gitbayd --config /etc/gitbay/config.toml admin backup --out /tmp/drill.tar.gz && gitbayd admin backup --verify /tmp/drill.tar.gz`
     → `integrity ok`, `connectivity ok on N repositories`; N equals
     `gitbayd admin stats --json` repository count on bay1 at the
     snapshot.
   - Secrets: `gitbayd --config /etc/gitbay/config.toml admin secrets check`
     → every value under one key, no error. The journal shows no
     `sealing secrets` failure.
   - LFS: `cd /var/lib/gitbay/lfs && find . -type f | while read f; do [ "$(sha256sum < "$f" | cut -c1-64)" = "$(basename "$f")" ] || echo "BAD $f"; done` → no output; object count against bay1's.
   - Release assets: every row's file exists with its digest:
     ```sh
     sqlite3 /var/lib/gitbay/gitbay.db "SELECT COALESCE(u.username, o.name) || '/' || r.name || '.git/gitbay-releases/' || a.release_id || '/' || a.name, a.sha256 FROM release_assets a JOIN releases rl ON rl.id = a.release_id JOIN repos r ON r.id = rl.repo_id LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id LEFT JOIN orgs o ON r.owner_kind = 'org' AND o.id = r.owner_id" |
     while IFS='|' read p sum; do [ "$(sha256sum < "/var/lib/gitbay/repos/$p" | cut -c1-64)" = "$sum" ] || echo "BAD $p"; done
     ```
     → no output.
   - Host key: `ssh-keyscan -p 22 <drill-ip>` fingerprint equals
     `ssh-keyscan -p 22 gitbay.org`'s.
   - Config: `gitbayd --config /etc/gitbay/config.toml check-config` → `config ok`.
8. Service: from the laptop, `ssh -p 22 git@<drill-ip> whoami` →
   `cmc`; `git clone ssh://git@<drill-ip>/krz/gitbay.git` succeeds.
   **Clock stops** at the clone.
9. Record on `.gitbay/wiki/Admin.org`, Restore drill table: date, host,
   snapshot time, time to service (step 2 → step 8), each check's
   result, and anything that needed a manual fix in Notes. Update:
   - `Architecture/10-Known-Gaps.org`: delete the `#259` row; the
     "measured recovery time" question row reads the measured figure
     and the date.
   - `Architecture/09-Controls.org:102`:
     `| Restore tested | in place | drill <date>, Admin wiki "Restore drill" |`.
   - `Architecture/08-Operations.org`: the "Recovery time" bullet names
     the figure.
   Branch `restore-drill-record`, one signed commit ending
   `Closes #259`, MR, ff merge, delete the branch.
10. Destroy the drill host. Repeat the drill every quarter and after
    any change to `cmd/gitbayd/backup.go` or the restic job, adding a
    row each time.

---

## Open questions

1. How `/var/lib/gitbay-stage` is populated on bay1 is not in the
   repository. If the staging step reads the local archives, enabling
   `age_recipients` breaks the restic path (runbook B.4 checks this
   before it matters).
2. Is `/etc/gitbay/config.toml` kept anywhere off the host today? The
   repository does not say; runbook A.3 creates a copy, and the drill
   depends on it.
3. Drill cadence: the plan proposes quarterly and after backup changes;
   #259 says only "on a schedule".
4. Where the clean host runs (throwaway VPS or local VM) is left to the
   operator; the runbook works for either.
5. `lfs.root` on bay1: if it points outside `server.root`, LFS objects
   are in neither the archive nor the restic snapshot of
   `/var/lib/gitbay`. The drill records it; fixing it is not in this
   plan.
6. Out of scope, noted while reading: `webhook add` takes `--secret`
   on argv (`internal/control/webhook.go:16-23`), against the
   stdin-only rule for secrets.

## Self-review

- #273: encryption of the four columns (Task 1.3), key file outside the
  database and root (1.2), key id prefix (1.1), rotation (1.4
  `rotate`), existing clear rows sealed at startup (1.4 `serve`,
  `ResealSecrets`), missing key behaviour (1.4 `openStore`, documented
  1.6), backups do not carry it (validation 1.2, e2e 1.5), install
  provisioning (1.6).
- #274: age recipients config (2.1), encryption (2.2), `--verify
  --identity` (2.2), restic path unaffected by the archives and checked
  for its staging step (runbook B.4), scripts (2.3).
- #259: `--verify` connectivity (3.1, 3.4), delete/rename/transfer held
  (3.2, 3.3, 3.4), drill covering database integrity, connectivity,
  LFS, release assets, config, host keys, secrets and the key, with time
  to service on the Admin page (3.5, runbook C).
- Names used across tasks: `seal.Keyring`/`Load`/`Seal`/`Open`/
  `CurrentID`/`KeyID`/`IsSealed`/`NewKey`/`ReadKeys`/`WriteKeys`;
  `Store.SetKeyring`/`ResealSecrets`/`SecretKeyUse`; `testConfig`;
  `archivePath`/`archiveReader`/`verifyBackup(path, identity)`;
  `backuplock.Hold`/`TryShared`/`ErrBusy`/`Name`; `holdOffBackup`;
  `gitutil.FsckConnectivity`. Consistent.
