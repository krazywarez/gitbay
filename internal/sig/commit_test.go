package sig

import (
	"bytes"
	"testing"
)

var signedCommit = []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
	"parent 0123456789012345678901234567890123456789\n" +
	"author T <a@example.test> 1700000000 +0000\n" +
	"committer T <c@example.test> 1700000000 +0000\n" +
	"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
	" \n" +
	" base64base64\n" +
	" =abcd\n" +
	" -----END PGP SIGNATURE-----\n" +
	"\n" +
	"subject line\n\nbody\n")

func TestParseCommitSigned(t *testing.T) {
	c, err := ParseCommit(signedCommit)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"parent 0123456789012345678901234567890123456789\n" +
		"author T <a@example.test> 1700000000 +0000\n" +
		"committer T <c@example.test> 1700000000 +0000\n" +
		"\n" +
		"subject line\n\nbody\n")
	if !bytes.Equal(c.Payload, wantPayload) {
		t.Errorf("payload not byte-exact:\ngot  %q\nwant %q", c.Payload, wantPayload)
	}
	wantSig := "-----BEGIN PGP SIGNATURE-----\n\nbase64base64\n=abcd\n-----END PGP SIGNATURE-----"
	if string(c.Signature) != wantSig {
		t.Errorf("signature reconstruction:\ngot  %q\nwant %q", c.Signature, wantSig)
	}
	if c.AuthorEmail != "a@example.test" || c.CommitterEmail != "c@example.test" ||
		c.Subject != "subject line" || c.AuthorUnix != 1700000000 {
		t.Errorf("fields: %+v", c)
	}
	if KindOf(c.Signature) != SigOpenPGP {
		t.Errorf("kind = %v", KindOf(c.Signature))
	}
}

func TestParseCommitUnsigned(t *testing.T) {
	raw := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author T <a@example.test> 1700000000 +0000\n" +
		"committer T <a@example.test> 1700000000 +0000\n" +
		"\nsubject\n")
	c, err := ParseCommit(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Signature != nil {
		t.Errorf("unsigned commit has signature %q", c.Signature)
	}
	if !bytes.Equal(c.Payload, raw) {
		t.Errorf("unsigned payload must equal raw object")
	}
}
