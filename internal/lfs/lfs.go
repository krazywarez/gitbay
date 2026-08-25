// Package lfs implements Git LFS server storage and authorization.
//
// The protocol surface lives in httpd (batch API + basic transfers) and
// sshd (git-lfs-authenticate); this package owns the pieces both need:
// content-addressed blob storage behind a small interface, and the
// short-lived tokens that bridge SSH authentication to the HTTP endpoints.
//
// BlobStore is deliberately minimal so an S3-compatible backend is a
// drop-in: implement the four methods against a bucket and the batch and
// transfer handlers work unchanged (the server streams as a proxy).
// Handing clients presigned URLs instead is a later optimization to the
// batch handler, not a rewrite.
package lfs

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OIDPat is a lowercase sha256 hex digest — the only object name LFS uses.
var OIDPat = regexp.MustCompile(`^[a-f0-9]{64}$`)

// BlobStore holds LFS objects by their sha256 content address.
type BlobStore interface {
	// Put stores the reader's content as oid, verifying both size and
	// digest; a mismatch stores nothing.
	Put(oid string, r io.Reader, size int64) error
	Get(oid string) (io.ReadCloser, int64, error)
	Exists(oid string) (int64, bool)
	Delete(oid string) error
}

// LocalStore is the on-disk backend: <root>/<aa>/<bb>/<oid>, written via a
// temp file and renamed only after the digest checks out.
type LocalStore struct {
	Root string
}

func (s LocalStore) path(oid string) string {
	return filepath.Join(s.Root, oid[:2], oid[2:4], oid)
}

func (s LocalStore) Put(oid string, r io.Reader, size int64) error {
	if !OIDPat.MatchString(oid) {
		return fmt.Errorf("bad oid %q", oid)
	}
	dir := filepath.Dir(s.path(oid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("size mismatch: got %d bytes, expected %d", n, size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != oid {
		return fmt.Errorf("content digest %s does not match oid", sum[:12])
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(oid))
}

func (s LocalStore) Get(oid string) (io.ReadCloser, int64, error) {
	if !OIDPat.MatchString(oid) {
		return nil, 0, fmt.Errorf("bad oid %q", oid)
	}
	f, err := os.Open(s.path(oid))
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func (s LocalStore) Exists(oid string) (int64, bool) {
	if !OIDPat.MatchString(oid) {
		return 0, false
	}
	fi, err := os.Stat(s.path(oid))
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

func (s LocalStore) Delete(oid string) error {
	if !OIDPat.MatchString(oid) {
		return fmt.Errorf("bad oid %q", oid)
	}
	return os.Remove(s.path(oid))
}

// Tokens bridge SSH authentication to the HTTP endpoints: stateless,
// HMAC-signed, scoped to one repo and one operation, short-lived. The
// secret persists in the settings table so tokens survive restarts.

const TokenTTL = time.Hour

// Sign mints a token for op ("download" or "upload") on repoID.
func Sign(secret []byte, repoID int64, op string, now time.Time) string {
	payload := fmt.Sprintf("%d:%s:%d", repoID, op, now.Add(TokenTTL).Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a token and returns the repo and operation it authorizes.
func Verify(secret []byte, token string, now time.Time) (repoID int64, op string, ok bool) {
	payloadB64, macB64, found := strings.Cut(token, ".")
	if !found {
		return 0, "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return 0, "", false
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return 0, "", false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), gotMAC) {
		return 0, "", false
	}
	parts := strings.Split(string(payload), ":")
	if len(parts) != 3 {
		return 0, "", false
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || now.Unix() > exp {
		return 0, "", false
	}
	if parts[1] != "download" && parts[1] != "upload" {
		return 0, "", false
	}
	return id, parts[1], true
}

// NewSecret returns 32 random bytes, hex-encoded for the settings table.
func NewSecret() string {
	buf := make([]byte, 32)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}
