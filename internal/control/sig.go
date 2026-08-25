package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/sig"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"pgp", "add"},
		Summary: "register an OpenPGP public key (armored, on stdin)", ReadsStdin: true, Run: runPGPAdd})
	register(Command{Path: []string{"pgp", "list"},
		Summary: "list registered OpenPGP keys", ReadOnly: true, Run: runPGPList})
	register(Command{Path: []string{"pgp", "remove"},
		Summary: "remove an OpenPGP key by fingerprint", Run: runPGPRemove})
	register(Command{Path: []string{"repo", "log"},
		Summary: "commit log with signature states: repo log <owner/name> [--limit n] [--path <file>]", ReadOnly: true, Run: runRepoLog})
}

func runPGPAdd(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: pgp add < key.asc")
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 1<<20))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading key: %v", err)
	}
	meta, err := sig.ParsePGPKey(raw)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	uids, _ := json.Marshal(meta.Emails)
	if err := c.Store.AddPGPKey(c.User.ID, meta.Fingerprint, string(raw), string(uids), meta.ExpiresAt, meta.RevokedAt); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return c.fail(protocol.ExitUsage, "%v", err)
		}
		return c.fail(protocol.ExitFailure, "adding key: %v", err)
	}
	type out struct {
		Fingerprint string   `json:"fingerprint"`
		Emails      []string `json:"emails"`
	}
	d := out{meta.Fingerprint, meta.Emails}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "added %s (%v)\n", d.Fingerprint, d.Emails)
	})
}

func runPGPList(c *Ctx, args []string) int {
	keys, err := c.Store.ListPGPKeys(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Fingerprint string     `json:"fingerprint"`
		Emails      string     `json:"emails"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
		RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	}
	var ds []out
	for _, k := range keys {
		ds = append(ds, out{k.Fingerprint, k.UIDsJSON, k.ExpiresAt, k.RevokedAt})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\n", d.Fingerprint, d.Emails)
		}
	})
}

func runPGPRemove(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: pgp remove <fingerprint>")
	}
	if err := c.Store.RemovePGPKey(c.User.ID, args[0]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no key %s on your account", args[0])
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[0]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", args[0])
	})
}

// sigParse is a package-local alias so callers avoid importing sig directly.
func sigParse(raw []byte) (*sig.Commit, error) { return sig.ParseCommit(raw) }

// VerifyCommitCached verifies one commit with the epoch cache. Shared with
// the web UI.
func VerifyCommitCached(st *store.Store, repo store.Repo, parsed *sig.Commit, sha string) (sig.Result, error) {
	epoch, err := st.KeyEpoch()
	if err != nil {
		return sig.Result{}, err
	}
	if res, ok, err := st.CachedSignature(repo.ID, sha, epoch); err != nil {
		return sig.Result{}, err
	} else if ok {
		return res, nil
	}
	res, err := sig.VerifyCommit(store.SigDB{Store: st}, parsed)
	if err != nil {
		return sig.Result{}, err
	}
	if err := st.StoreSignature(repo.ID, sha, res, epoch); err != nil {
		return sig.Result{}, err
	}
	return res, nil
}

func runRepoLog(c *Ctx, args []string) int {
	limit := 30
	var path, filePath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--limit requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 || n > 1000 {
				return c.fail(protocol.ExitUsage, "--limit must be 1..1000")
			}
			limit = n
			i++
		case "--path":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--path requires a value")
			}
			filePath = args[i+1]
			i++
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "usage: repo log <owner/name> [--limit n] [--path <file>]")
			}
			path = args[i]
		}
	}
	if path == "" {
		return c.fail(protocol.ExitUsage, "usage: repo log <owner/name> [--limit n] [--path <file>]")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	var shas []string
	var err error
	if filePath != "" {
		shas, err = gitutil.RevListPath(dir, repo.DefaultBranch, filePath, limit)
	} else {
		shas, err = gitutil.RevList(dir, repo.DefaultBranch, limit)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading log: %v", err)
	}

	type sigOut struct {
		State       string `json:"state"`
		Signer      string `json:"signer,omitempty"`
		Fingerprint string `json:"key_fingerprint,omitempty"`
	}
	type out struct {
		SHA            string `json:"sha"`
		Subject        string `json:"subject"`
		AuthorName     string `json:"author_name"`
		AuthorEmail    string `json:"author_email"`
		CommitterEmail string `json:"committer_email,omitempty"` // only when it differs
		Date           string `json:"date"`
		Signature      sigOut `json:"signature"`
	}
	var ds []out
	for _, sha := range shas {
		raw, err := gitutil.ReadCommit(dir, sha)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		parsed, err := sig.ParseCommit(raw)
		if err != nil {
			return c.fail(protocol.ExitFailure, "parsing %s: %v", sha, err)
		}
		res, err := VerifyCommitCached(c.Store, repo, parsed, sha)
		if err != nil {
			return c.fail(protocol.ExitFailure, "verifying %s: %v", sha, err)
		}
		d := out{
			SHA:         sha,
			Subject:     parsed.Subject,
			AuthorName:  parsed.AuthorName,
			AuthorEmail: parsed.AuthorEmail,
			Date:        time.Unix(parsed.AuthorUnix, 0).UTC().Format(time.RFC3339),
			Signature:   sigOut{State: string(res.State), Fingerprint: res.KeyFingerprint},
		}
		if parsed.CommitterEmail != parsed.AuthorEmail {
			d.CommitterEmail = parsed.CommitterEmail
		}
		if res.SignerUserID != 0 {
			if u, err := c.Store.UserByID(res.SignerUserID); err == nil {
				d.Signature.Signer = u.Username
			}
		}
		ds = append(ds, d)
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%.10s  %-22s %s (%s <%s>)\n", d.SHA, d.Signature.State, d.Subject, d.AuthorName, d.AuthorEmail)
		}
	})
}
