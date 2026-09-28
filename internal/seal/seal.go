// Package seal encrypts the secret columns of the database with
// AES-256-GCM under keys held in a file outside the database and outside
// server.root, so neither a copy of the database nor a backup opens them
// (#273). A key file that cannot be re-read after it changes fails
// closed: Seal and Open return errors until the file is fixed.
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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Prefix marks a sealed value: "gbs1:<key id>:<base64 nonce||ciphertext>".
const Prefix = "gbs1:"

// maxKeyFile is the largest key file ReadKeys accepts.
const maxKeyFile = 1 << 20

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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s is mode %04o; it must be readable by its owner alone (0600)", path, perm)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeyFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxKeyFile)
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
// user), then renamed over it. Keys ReadKeys would refuse are refused
// before anything is written.
func WriteKeys(path string, keys []Key) error {
	if len(keys) == 0 {
		return errors.New("no keys to write")
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !validID(k.ID) || len(k.Secret) != 32 || seen[k.ID] {
			return fmt.Errorf("key %q is not an 8-hex-id, 32-byte key or appears twice", k.ID)
		}
		seen[k.ID] = true
	}
	var b strings.Builder
	b.WriteString("# gitbay secret keys, \"<id> <base64 key>\" per line. The last line seals\n")
	b.WriteString("# new values; the others open values sealed before a rotation.\n")
	b.WriteString("# Keep a copy off this host: backups do not carry this file.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %s\n", k.ID, base64.StdEncoding.EncodeToString(k.Secret))
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secret-key-*")
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Losing the file loses every sealed value, so the rename is made
	// durable before returning.
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		return fmt.Errorf("%s was replaced, but syncing %s failed: %w", path, dir, err)
	}
	return nil
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

// Load reads the key file at path and returns a Keyring over it. It
// fails when ReadKeys would.
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

// Seal encrypts plain under the current key with a random nonce. aad
// names the column, so a value copied into another column does not open
// there.
func (k *Keyring) Seal(aad, plain string) (string, error) {
	if aad == "" {
		return "", errNoAAD
	}
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
	if aad == "" {
		return "", errNoAAD
	}
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
	if err != nil || len(ct) < g.NonceSize()+g.Overhead() {
		return "", fmt.Errorf("value sealed with key %s is malformed", id)
	}
	plain, err := g.Open(nil, ct[:g.NonceSize()], ct[g.NonceSize():], []byte(aad))
	if err != nil {
		return "", fmt.Errorf("value sealed with key %s does not open: wrong key, wrong column or altered value", id)
	}
	return string(plain), nil
}

var errNoAAD = errors.New("seal: additional data (table.column) is required")

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
