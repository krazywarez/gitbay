package control

import (
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"repo", "deploy-key", "add"},
		Summary: "bind a read-only (or --rw) key to one repository",
		Usage:   "repo deploy-key add <owner/name> [--rw] [--ttl 30d|720h] < key.pub",
		Flags: []Flag{
			{"--rw", "", "the key may push, not just fetch", ""},
			{"--ttl", "30d|720h", "how long the key authenticates", "never expires"},
		},
		Examples:        []string{"repo deploy-key add krz/gitbay < key.pub", "repo deploy-key add krz/gitbay --ttl 30d < key.pub"},
		ReadsStdin:      true,
		MintsCredential: true, NeedsRecentSignIn: true, Run: runDeployKeyAdd})
	register(Command{Path: []string{"repo", "deploy-key", "list"},
		Summary:  "list deploy keys",
		Usage:    "repo deploy-key list <owner/name>",
		Examples: []string{"repo deploy-key list krz/gitbay"},
		ReadOnly: true, Run: runDeployKeyList})
	register(Command{Path: []string{"repo", "deploy-key", "remove"},
		Summary:  "remove a deploy key",
		Usage:    "repo deploy-key remove <owner/name> <fingerprint>",
		Examples: []string{"repo deploy-key remove krz/gitbay SHA256:abcd1234"},
		Run:      runDeployKeyRemove})
}

func runDeployKeyAdd(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--ttl"}, Bools: []string{"--rw"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path := f.pos(0)
	if path == "" {
		return c.usage()
	}
	mode := "ro"
	if f.Has("--rw") {
		mode = "rw"
	}
	expires, code := c.ttlFlag(f)
	if code >= 0 {
		return code
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading key: %v", err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return c.fail(protocol.ExitUsage, "not a valid public key in authorized_keys format: %v", err)
	}
	label, err := keyLabel(comment)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	fp := ssh.FingerprintSHA256(pub)
	scope := fmt.Sprintf("deploy:%d:%s", repo.ID, mode)
	if err := c.Store.AddSSHKeyFrom(c.User.ID, fp, pub.Type(), pub.Marshal(), scope, label, store.KeyOrigin{CreatedByToken: c.TokenID, ExpiresAt: expires}); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return c.failErr(err)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	d := map[string]any{"fingerprint": fp, "mode": mode}
	if expires != nil {
		d["expires_at"] = expires
	}
	return c.emit(d, func(w io.Writer) {
		line := fmt.Sprintf("deploy key %s (%s) bound to %s", fp, mode, repo.Path())
		if expires != nil {
			line += ", expires " + c.expiresText(expires, time.Now())
		}
		fmt.Fprintln(w, line)
	})
}

func runDeployKeyList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	keys, err := c.Store.ListDeployKeys(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Fingerprint string     `json:"fingerprint"`
		Algo        string     `json:"algo"`
		Mode        string     `json:"mode"`
		Label       string     `json:"label"`
		LastUsedAt  string     `json:"last_used_at,omitempty"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	}
	var ds []out
	for _, k := range keys {
		mode := "ro"
		if policy.DeployScopeAllows(k.Scope, repo.ID, true) {
			mode = "rw"
		}
		ds = append(ds, out{k.Fingerprint, k.Algo, mode, k.Label, k.LastUsedAt, k.ExpiresAt})
	}
	now := time.Now()
	return c.emitView(ds, func(w io.Writer) {
		tb := c.table(w, "FINGERPRINT", "ALGO", "MODE", "LABEL", "USED", "EXPIRES")
		for _, d := range ds {
			tb.row(cFlex(d.Fingerprint), cText(d.Algo), cState(d.Mode), cText(d.Label),
				cText(c.usedText(d.LastUsedAt)), cText(c.expiresText(d.ExpiresAt, now)))
		}
		tb.flush()
	}, func() screen {
		rows := make([]row, len(ds))
		for i, d := range ds {
			rows[i] = rowOf(cFlexRef(d.Fingerprint), cState(d.Mode), cText(d.Label),
				cMeta(d.Algo, "used "+c.usedText(d.LastUsedAt), c.expiresText(d.ExpiresAt, now)))
		}
		return listScreen("Deploy keys", rows,
			action{"Keys", []string{"repo", "deploy-key", "add", repo.Path()}},
			action{"Keys", []string{"repo", "deploy-key", "remove", repo.Path(), "<fingerprint>"}},
		)
	})
}

func runDeployKeyRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.RemoveDeployKey(repo.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no deploy key %s on %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed deploy key %s from %s\n", args[1], repo.Path())
	})
}
