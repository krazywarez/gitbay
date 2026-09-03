package gitutil

import (
	"bytes"
	"errors"
	"testing"
)

// An archive is bounded: past MaxArchiveBytes git is stopped and the
// caller gets ErrArchiveTooLarge rather than a truncated stream (#124).
func TestArchiveCap(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "big.txt", string(bytes.Repeat([]byte("gitbay archive cap test line\n"), 4000)))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	var out bytes.Buffer
	if err := Archive(dir, "main", "x", &out); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if out.Len() < 2 || out.Bytes()[0] != 0x1f || out.Bytes()[1] != 0x8b {
		t.Fatalf("not gzip output: %d bytes", out.Len())
	}

	defer func(v int64) { MaxArchiveBytes = v }(MaxArchiveBytes)
	MaxArchiveBytes = 64
	out.Reset()
	err := Archive(dir, "main", "x", &out)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("over the cap: err=%v, %d bytes written", err, out.Len())
	}
	if int64(out.Len()) > 64 {
		t.Fatalf("wrote %d bytes past a 64-byte cap", out.Len())
	}
}
