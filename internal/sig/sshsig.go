package sig

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHSIG armored signature format, per openssh PROTOCOL.sshsig.
const sshsigMagic = "SSHSIG"

type sshsigBlob struct {
	Version       uint32
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

func verifySSH(db DB, c *Commit) (Result, error) {
	blob, err := decodeArmor(c.Signature, "SSH SIGNATURE")
	if err != nil {
		return Result{State: BadSignature}, nil
	}
	sb, err := parseSSHSig(blob)
	if err != nil {
		return Result{State: BadSignature}, nil
	}
	pub, err := ssh.ParsePublicKey(sb.PublicKey)
	if err != nil {
		return Result{State: BadSignature}, nil
	}

	fp := ssh.FingerprintSHA256(pub)
	key, found, err := db.SSHSignerByFingerprint(fp)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{State: SignedUnknownKey, KeyFingerprint: fp}, nil
	}
	res := Result{SignerUserID: key.UserID, KeyFingerprint: fp}

	// Reconstruct the signed blob: MAGIC || namespace || reserved ||
	// hash_algorithm || H(payload).
	var h []byte
	switch sb.HashAlgorithm {
	case "sha512":
		d := sha512.Sum512(c.Payload)
		h = d[:]
	case "sha256":
		d := sha256.Sum256(c.Payload)
		h = d[:]
	default:
		res.State = BadSignature
		return res, nil
	}
	if sb.Namespace != "git" {
		res.State = BadSignature
		return res, nil
	}
	signed := buildSSHSignedData(sb.Namespace, sb.Reserved, sb.HashAlgorithm, h)

	var sshSig ssh.Signature
	if err := ssh.Unmarshal(sb.Signature, &sshSig); err != nil {
		res.State = BadSignature
		return res, nil
	}
	if err := pub.Verify(signed, &sshSig); err != nil {
		res.State = BadSignature
		return res, nil
	}

	// SSH keys carry no identities: the principal set is the owning
	// account's verified emails.
	verified, err := db.VerifiedEmails(key.UserID)
	if err != nil {
		return Result{}, err
	}
	if !slices.Contains(verified, c.AuthorEmail) {
		res.State = SignedEmailMismatch
		return res, nil
	}
	res.State = Verified
	return res, nil
}

func parseSSHSig(blob []byte) (*sshsigBlob, error) {
	if !bytes.HasPrefix(blob, []byte(sshsigMagic)) {
		return nil, fmt.Errorf("missing SSHSIG magic")
	}
	var sb sshsigBlob
	if err := ssh.Unmarshal(blob[len(sshsigMagic):], &sb); err != nil {
		return nil, err
	}
	if sb.Version != 1 {
		return nil, fmt.Errorf("unsupported sshsig version %d", sb.Version)
	}
	return &sb, nil
}

func buildSSHSignedData(namespace, reserved, hashAlg string, hash []byte) []byte {
	var b bytes.Buffer
	b.WriteString(sshsigMagic)
	writeSSHString(&b, []byte(namespace))
	writeSSHString(&b, []byte(reserved))
	writeSSHString(&b, []byte(hashAlg))
	writeSSHString(&b, hash)
	return b.Bytes()
}

func writeSSHString(b *bytes.Buffer, s []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(s)))
	b.Write(l[:])
	b.Write(s)
}

// MarshalSSHSig builds an armored SSHSIG over payload with the given signer.
// Used by fixture generation and (later) forge-side tooling.
func MarshalSSHSig(signer ssh.Signer, payload []byte) ([]byte, error) {
	d := sha512.Sum512(payload)
	signed := buildSSHSignedData("git", "", "sha512", d[:])
	sshSig, err := signer.Sign(nil, signed)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	body.WriteString(sshsigMagic)
	blob := sshsigBlob{
		Version:       1,
		PublicKey:     signer.PublicKey().Marshal(),
		Namespace:     "git",
		Reserved:      "",
		HashAlgorithm: "sha512",
		Signature:     ssh.Marshal(sshSig),
	}
	body.Write(ssh.Marshal(blob))

	b64 := base64.StdEncoding.EncodeToString(body.Bytes())
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(b64) > 70 {
		out.WriteString(b64[:70] + "\n")
		b64 = b64[70:]
	}
	out.WriteString(b64 + "\n-----END SSH SIGNATURE-----\n")
	return []byte(out.String()), nil
}

// decodeArmor extracts the base64 body between BEGIN/END markers for the
// given label. Works for both SSHSIG and OpenPGP armor (checksum lines and
// armor headers are skipped).
func decodeArmor(armored []byte, label string) ([]byte, error) {
	begin := "-----BEGIN " + label + "-----"
	end := "-----END " + label + "-----"
	s := string(armored)
	i := strings.Index(s, begin)
	if i < 0 {
		return nil, fmt.Errorf("no %s armor", label)
	}
	bodyStart := i + len(begin)
	// Search for the end marker only after the begin block, so an end
	// marker overlapping the begin line's trailing dashes cannot yield a
	// negative-length body.
	rel := strings.Index(s[bodyStart:], end)
	if rel < 0 {
		return nil, fmt.Errorf("no %s armor", label)
	}
	j := bodyStart + rel
	var b64 strings.Builder
	for _, line := range strings.Split(s[bodyStart:j], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "=") || strings.Contains(line, ":") {
			continue // armor checksum or header line
		}
		b64.WriteString(line)
	}
	return base64.StdEncoding.DecodeString(b64.String())
}
