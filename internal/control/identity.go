package control

import (
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{
		Path:     []string{"whoami"},
		Summary:  "show the authenticated account",
		ReadOnly: true,
		Run:      runWhoami,
	})
	register(Command{
		Path:     []string{"keys", "list"},
		Summary:  "list registered SSH keys",
		ReadOnly: true,
		Run:      runKeysList,
	})
	register(Command{
		Path:       []string{"keys", "add"},
		Summary:    "register an SSH public key (authorized_keys format on stdin) [--scope full|git]",
		ReadsStdin: true,
		Run:        runKeysAdd,
	})
	register(Command{
		Path:    []string{"keys", "remove"},
		Summary: "remove an SSH key by fingerprint",
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
	}
	var ds []out
	for _, k := range keys {
		ds = append(ds, out{k.Fingerprint, k.Algo, k.Scope})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\n", d.Fingerprint, d.Algo, d.Scope)
		}
	})
}

func runKeysAdd(c *Ctx, args []string) int {
	scope := "full"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scope":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--scope requires a value")
			}
			scope = args[i+1]
			i++
		default:
			return c.fail(protocol.ExitUsage, "usage: keys add [--scope full|git] < key.pub")
		}
	}
	if scope != "full" && scope != "git" {
		// deploy:* scopes are granted via repo settings, not self-service.
		return c.fail(protocol.ExitUsage, "scope must be full or git")
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading key: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return c.fail(protocol.ExitUsage, "not a valid public key in authorized_keys format: %v", err)
	}
	fp := ssh.FingerprintSHA256(pub)
	if err := c.Store.AddSSHKey(c.User.ID, fp, pub.Type(), pub.Marshal(), scope); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return c.fail(protocol.ExitUsage, "%v", err)
		}
		return c.fail(protocol.ExitFailure, "adding key: %v", err)
	}
	type out struct {
		Fingerprint string `json:"fingerprint"`
		Scope       string `json:"scope"`
	}
	d := out{fp, scope}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "added %s (%s)\n", d.Fingerprint, d.Scope)
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
