package gitd

import (
	"bytes"
	"testing"
)

// FuzzReadPktLine hammers the only parser in the anonymous git:// path
// with attacker-controlled bytes: it must never panic and never return a
// line longer than the pkt-line format allows.
func FuzzReadPktLine(f *testing.F) {
	f.Add([]byte("003egit-upload-pack /a/b\x00host=example.com\x00"))
	f.Add([]byte("0000"))
	f.Add([]byte("0004"))
	f.Add([]byte("ffff" + "x"))
	f.Add([]byte("00zz"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		line, err := readPktLine(bytes.NewReader(data))
		if err == nil && len(line) > 65516 {
			t.Fatalf("line longer than pkt-line max: %d", len(line))
		}
	})
}
