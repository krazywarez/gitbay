package control

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{
		Path:     []string{"whoami"},
		Summary:  "show the authenticated account",
		Usage:    "whoami",
		ReadOnly: true,
		Run:      runWhoami,
	})
	register(Command{
		Path:     []string{"keys", "list"},
		Summary:  "list registered SSH keys",
		Usage:    "keys list",
		ReadOnly: true,
		Run:      runKeysList,
	})
	register(Command{
		Path:       []string{"keys", "add"},
		Summary:    "register an SSH public key (authorized_keys format)",
		Usage:      "keys add [--scope full|git|runner] [--label <text>] < key.pub",
		ReadsStdin: true,
		Run:        runKeysAdd,
	})
	register(Command{
		Path:    []string{"keys", "label"},
		Summary: "name a key; an empty label clears it",
		Usage:   "keys label <fingerprint> [<text>]",
		Run:     runKeysLabel,
	})
	register(Command{
		Path:    []string{"keys", "remove"},
		Summary: "remove an SSH key by fingerprint",
		Usage:   "keys remove <fingerprint>",
		Run:     runKeysRemove,
	})
}

func runWhoami(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: whoami [--json]")
	}
	type out struct {
		Username string `json:"username"`
		Admin    bool   `json:"admin"`
		KeyScope string `json:"key_scope"`
	}
	d := out{Username: c.User.Username, Admin: c.User.IsAdmin, KeyScope: c.Scope}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintln(w, d.Username)
	})
}

func runKeysList(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: keys list [--json]")
	}
	keys, err := c.Store.ListSSHKeys(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "listing keys: %v", err)
	}
	type out struct {
		Fingerprint string `json:"fingerprint"`
		Algo        string `json:"algo"`
		Scope       string `json:"scope"`
		Label       string `json:"label"`
	}
	var ds []out
	for _, k := range keys {
		ds = append(ds, out{k.Fingerprint, k.Algo, k.Scope, k.Label})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Fingerprint, d.Algo, d.Scope, d.Label)
		}
	})
}

// maxKeyLabel bounds a key's name. Labels are display text, one line.
const maxKeyLabel = 64

// keyLabel normalises a label: surrounding space trimmed, control
// characters refused, length capped. An empty result is a valid "no
// label".
func keyLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxKeyLabel {
		return "", fmt.Errorf("label is longer than %d bytes", maxKeyLabel)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("label must be a single line of printable text")
		}
	}
	return s, nil
}

func runKeysAdd(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--scope", "--label"}, MaxPos: 0, Usage: "keys add [--scope full|git|runner] [--label <text>] < key.pub"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	scope := "full"
	if f.Has("--scope") {
		scope = f.Value("--scope")
	}
	if scope != "full" && scope != "git" && scope != "runner" {
		// deploy:* scopes are granted via repo settings, not self-service.
		return c.fail(protocol.ExitUsage, "scope must be full, git or runner")
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading key: %v", err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return c.fail(protocol.ExitUsage, "not a valid public key in authorized_keys format: %v", err)
	}
	// The key's own comment is the label unless --label says otherwise.
	label := comment
	if f.Has("--label") {
		label = f.Value("--label")
	}
	if label, err = keyLabel(label); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	fp := ssh.FingerprintSHA256(pub)
	if err := c.Store.AddSSHKey(c.User.ID, fp, pub.Type(), pub.Marshal(), scope, label); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return c.failErr(err)
		}
		return c.fail(protocol.ExitFailure, "adding key: %v", err)
	}
	type out struct {
		Fingerprint string `json:"fingerprint"`
		Scope       string `json:"scope"`
		Label       string `json:"label"`
	}
	d := out{fp, scope, label}
	return c.emit(d, func(w io.Writer) {
		if d.Label != "" {
			fmt.Fprintf(w, "added %s (%s) %s\n", d.Fingerprint, d.Scope, d.Label)
			return
		}
		fmt.Fprintf(w, "added %s (%s)\n", d.Fingerprint, d.Scope)
	})
}

func runKeysLabel(c *Ctx, args []string) int {
	if len(args) < 1 || len(args) > 2 {
		return c.fail(protocol.ExitUsage, "usage: keys label <fingerprint> [<text>]")
	}
	label := ""
	if len(args) == 2 {
		label = args[1]
	}
	label, err := keyLabel(label)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetSSHKeyLabel(c.User.ID, args[0], label); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no key with fingerprint %s on your account", args[0])
		}
		return c.fail(protocol.ExitFailure, "labelling key: %v", err)
	}
	d := map[string]string{"fingerprint": args[0], "label": label}
	return c.emit(d, func(w io.Writer) {
		if label == "" {
			fmt.Fprintf(w, "cleared label on %s\n", args[0])
			return
		}
		fmt.Fprintf(w, "%s is now %q\n", args[0], label)
	})
}

func runKeysRemove(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: keys remove <fingerprint>")
	}
	if err := c.Store.RemoveSSHKey(c.User.ID, args[0]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no key with fingerprint %s on your account", args[0])
		}
		return c.fail(protocol.ExitFailure, "removing key: %v", err)
	}
	return c.emit(map[string]string{"removed": args[0]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", args[0])
	})
}
