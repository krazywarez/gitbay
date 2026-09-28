package seal

import (
	"encoding/base64"
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

func TestOpenRefusesAnAlteredValue(t *testing.T) {
	ring, err := Load(keyFile(t, newKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := ring.Seal("mirrors.token", "tok")
	// A character in the middle: the last one may carry only padding bits.
	i := len(v) - 10
	alt := byte('A')
	if v[i] == 'A' {
		alt = 'B'
	}
	if _, err := ring.Open("mirrors.token", v[:i]+string(alt)+v[i+1:]); err == nil {
		t.Fatal("opened an altered value")
	}
	if _, err := ring.Open("mirrors.token", "tok"); err == nil {
		t.Fatal("opened a clear value")
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

func TestWriteKeysRefusesABadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	good := newKey(t)
	for _, keys := range [][]Key{
		nil,
		{{ID: good.ID, Secret: good.Secret[:16]}},
		{{ID: "XYZ12345", Secret: good.Secret}},
		{good, good},
	} {
		if err := WriteKeys(path, keys); err == nil {
			t.Errorf("wrote %d keys that do not read back", len(keys))
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused write left a file: %v", err)
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

func TestOpenRefusesMalformedInput(t *testing.T) {
	k := newKey(t)
	ring, err := Load(keyFile(t, k))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{
		"gbs1:",
		"gbs1:" + k.ID + ":",
		"gbs1:" + strings.ToUpper(k.ID) + ":AAAA",
		"gbs1:" + k.ID[:7] + ":AAAA",
		"gbs1:" + k.ID + ":!!!not base64!!!",
		"gbs1:" + k.ID + ":AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // 29 bytes
		"gbs1:0badf00d:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if got, err := ring.Open("mirrors.token", v); err == nil {
			t.Errorf("Open(%q) = %q, want an error", v, got)
		}
	}
}

func TestEmptyAdditionalDataRefused(t *testing.T) {
	ring, err := Load(keyFile(t, newKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Seal("", "s"); err == nil {
		t.Fatal("sealed with no column")
	}
	v, _ := ring.Seal("mirrors.token", "s")
	if _, err := ring.Open("", v); err == nil {
		t.Fatal("opened with no column")
	}
}

func TestReadKeysRefusesDuplicatesDirectoriesAndLargeFiles(t *testing.T) {
	k := newKey(t)
	line := k.ID + " " + base64.StdEncoding.EncodeToString(k.Secret) + "\n"
	dup := filepath.Join(t.TempDir(), "dup")
	if err := os.WriteFile(dup, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeys(dup); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate id: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeys(dir); err == nil {
		t.Error("read a directory")
	}
	big := filepath.Join(t.TempDir(), "big")
	body := line + "#" + strings.Repeat("x", maxKeyFile) + "\n"
	if err := os.WriteFile(big, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeys(big); err == nil {
		t.Error("read a file over the size limit")
	}
}
