package sig

import "testing"

// Every parser here eats bytes that arrive over the wire from whoever can
// push or register a key. None may panic on arbitrary input.

func FuzzParseCommit(f *testing.F) {
	f.Add([]byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\n\nmsg\n"))
	f.Add([]byte("tree x\ngpgsig -----BEGIN PGP SIGNATURE-----\n gibberish\n -----END PGP SIGNATURE-----\nauthor <>\n\n\n"))
	f.Add([]byte("\x00\x01\x02"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, raw []byte) {
		ParseCommit(raw)
	})
}

func FuzzDecodeArmorAndParseSSHSig(f *testing.F) {
	f.Add([]byte("-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQ==\n-----END SSH SIGNATURE-----\n"))
	f.Add([]byte("SSHSIG"))
	f.Add([]byte("-----BEGIN SSH SIGNATURE-----\n-----END SSH SIGNATURE-----\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		if blob, err := decodeArmor(data, "SSH SIGNATURE"); err == nil {
			parseSSHSig(blob)
		}
		// The raw blob path too: parseSSHSig on undecoded input.
		parseSSHSig(data)
	})
}

func FuzzParsePGPKey(f *testing.F) {
	f.Add([]byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmQENBF==\n-----END PGP PUBLIC KEY BLOCK-----\n"))
	f.Add([]byte(""))
	f.Add([]byte("not a key"))
	f.Fuzz(func(t *testing.T, armored []byte) {
		ParsePGPKey(armored)
	})
}
