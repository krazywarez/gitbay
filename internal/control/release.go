package control

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"release", "create"},
		Summary:    "create a release on a tag",
		Usage:      "release create <owner/name> <tag> [--title <t>] [--notes <n> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runReleaseCreate})
	register(Command{Path: []string{"release", "edit"},
		Summary:    "update a release's title and notes",
		Usage:      "release edit <owner/name> <tag> [--title <t>] [--notes <n> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runReleaseEdit})
	register(Command{Path: []string{"release", "list"},
		Summary: "list releases",
		Usage:   "release list <owner/name>", ReadOnly: true, Run: runReleaseList})
	register(Command{Path: []string{"release", "show"},
		Summary: "show a release with assets",
		Usage:   "release show <owner/name> <tag>", ReadOnly: true, Run: runReleaseShow})
	register(Command{Path: []string{"release", "delete"},
		Summary: "delete a release and its assets",
		Usage:   "release delete <owner/name> <tag> --yes", Run: runReleaseDelete})
	register(Command{Path: []string{"release", "asset", "add"},
		Summary:    "upload an asset from stdin",
		Usage:      "release asset add <owner/name> <tag> <filename> < file",
		ReadsStdin: true, Run: runAssetAdd})
	register(Command{Path: []string{"release", "asset", "get"},
		Summary:  "write an asset to stdout",
		Usage:    "release asset get <owner/name> <tag> <filename> > file",
		ReadOnly: true, Run: runAssetGet})
	register(Command{Path: []string{"release", "asset", "remove"},
		Summary: "remove an asset",
		Usage:   "release asset remove <owner/name> <tag> <filename>", Run: runAssetRemove})
}

var assetNamePat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`)

// assetDir holds a release's uploaded files inside the bare repo directory,
// so backup, transfer, and delete all carry them automatically.
func assetDir(root string, repo store.Repo, releaseID int64) string {
	return filepath.Join(RepoDir(root, repo.OwnerName, repo.Name), "gitbay-releases", strconv.FormatInt(releaseID, 10))
}

// releaseRef loads a release for "<owner/name> <tag>" with the permission.
func releaseRef(c *Ctx, args []string, perm func(store.User, store.Repo, string) bool) (store.Repo, store.Release, int) {
	if len(args) < 2 {
		return store.Repo{}, store.Release{}, c.fail(protocol.ExitUsage, "expected <owner/name> <tag>")
	}
	repo, code := resolveRepo(c, args[0], perm)
	if code >= 0 {
		return repo, store.Release{}, code
	}
	rel, err := c.Store.ReleaseByTag(repo.ID, args[1])
	if errors.Is(err, store.ErrNotFound) {
		return repo, rel, c.fail(protocol.ExitNotFound, "no release for tag %q in %s", args[1], repo.Path())
	}
	if err != nil {
		return repo, rel, c.fail(protocol.ExitFailure, "%v", err)
	}
	return repo, rel, -1
}

func runReleaseCreate(c *Ctx, args []string) int {
	const usage = "usage: release create <owner/name> <tag> [--title <t>] [--notes <n> | --file -] [--format md|org]"
	f, err := parseFlags(args, flagSpec{Values: []string{"--title", "--notes", "--file", "--format"}, MaxPos: 2, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, tag := f.pos(0), f.pos(1)
	title, notes, file, format := f.Value("--title"), f.Value("--notes"), f.Value("--file"), f.Value("--format")
	if path == "" || tag == "" {
		return c.fail(protocol.ExitUsage, usage)
	}
	fmtName, err := markupFormat(format)
	if err != nil {
		return c.failErr(err)
	}
	if fmtName == "" {
		fmtName = "md"
	}
	repo, code := resolveRepo(c, path, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, "refs/tags/"+tag); err != nil {
		return c.fail(protocol.ExitNotFound, "no tag %q in %s — push the tag first", tag, repo.Path())
	}
	body, err := bodyFrom(c, notes, file)
	if err != nil {
		return c.failErr(err)
	}
	if title == "" {
		title = tag
	}
	if _, err := c.Store.CreateRelease(repo.ID, tag, title, body, c.User.ID, fmtName); err != nil {
		return c.failErr(err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "release.created", fmt.Sprintf(`{"tag":%q}`, tag))
	return c.emit(map[string]string{"tag": tag, "title": title}, func(w io.Writer) {
		fmt.Fprintf(w, "created release %s on %s\n", tag, repo.Path())
	})
}

type assetOut struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type releaseOut struct {
	Tag         string     `json:"tag"`
	Title       string     `json:"title"`
	Notes       string     `json:"notes,omitempty"`
	NotesFormat string     `json:"notes_format,omitempty"`
	Author      string     `json:"author,omitempty"`
	CreatedAt   string     `json:"created_at"`
	Assets      []assetOut `json:"assets,omitempty"`
}

func releaseToOut(r store.Release, withNotes bool) releaseOut {
	o := releaseOut{Tag: r.Tag, Title: r.Title, Author: r.Author, CreatedAt: r.CreatedAt}
	if withNotes {
		o.Notes = r.Notes
		o.NotesFormat = r.NotesFormat
	}
	for _, a := range r.Assets {
		o.Assets = append(o.Assets, assetOut{a.Name, a.Size, a.SHA256})
	}
	return o
}

func runReleaseEdit(c *Ctx, args []string) int {
	const usage = "usage: release edit <owner/name> <tag> [--title <t>] [--notes <n> | --file -] [--format md|org]"
	f, err := parseFlags(args, flagSpec{Values: []string{"--title", "--notes", "--file", "--format"}, MaxPos: 2, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, tag := f.pos(0), f.pos(1)
	title, notes, file, format := f.Value("--title"), f.Value("--notes"), f.Value("--file"), f.Value("--format")
	setTitle, setNotes := f.Has("--title"), f.Has("--notes") || f.Has("--file")
	fmtName, err := markupFormat(format)
	if err != nil {
		return c.failErr(err)
	}
	if path == "" || tag == "" || (!setTitle && !setNotes && fmtName == "") {
		return c.fail(protocol.ExitUsage, usage)
	}
	repo, code := resolveRepo(c, path, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	rel, err := c.Store.ReleaseByTag(repo.ID, tag)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no release %q in %s", tag, repo.Path())
	}
	// Absent flags keep what the release already says.
	if !setTitle {
		title = rel.Title
	} else if title == "" {
		title = tag
	}
	body := rel.Notes
	if setNotes {
		if body, err = bodyFrom(c, notes, file); err != nil {
			return c.failErr(err)
		}
	}
	if fmtName == "" {
		fmtName = rel.NotesFormat
	}
	if err := c.Store.UpdateRelease(repo.ID, tag, title, body, fmtName); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"tag": tag, "title": title}, func(w io.Writer) {
		fmt.Fprintf(w, "updated release %s\n", tag)
	})
}

func runReleaseList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: release list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	rels, err := c.Store.ListReleases(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var ds []releaseOut
	for _, r := range rels {
		ds = append(ds, releaseToOut(r, false))
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%d asset(s)\n", d.Tag, d.Title, len(d.Assets))
		}
	})
}

func runReleaseShow(c *Ctx, args []string) int {
	_, rel, code := releaseRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	d := releaseToOut(rel, true)
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s\t%s\tby %s on %s\n", d.Tag, d.Title, d.Author, d.CreatedAt)
		if d.Notes != "" {
			fmt.Fprintf(w, "\n%s\n", d.Notes)
		}
		for _, a := range d.Assets {
			fmt.Fprintf(w, "%s\t%d\t%s\n", a.Name, a.Size, a.SHA256)
		}
	})
}

func runReleaseDelete(c *Ctx, args []string) int {
	var rest []string
	var yes bool
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else {
			rest = append(rest, a)
		}
	}
	repo, rel, code := releaseRef(c, rest, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if !yes {
		return c.fail(protocol.ExitUsage, "release delete is permanent (assets included); re-run with --yes")
	}
	if err := c.Store.DeleteRelease(rel.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	os.RemoveAll(assetDir(c.Cfg.Server.Root, repo, rel.ID))
	c.Store.RecordEvent(repo.ID, c.User.ID, "release.deleted", fmt.Sprintf(`{"tag":%q}`, rel.Tag))
	return c.emit(map[string]string{"deleted": rel.Tag}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted release %s\n", rel.Tag)
	})
}

func runAssetAdd(c *Ctx, args []string) int {
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: release asset add <owner/name> <tag> <filename> < file")
	}
	repo, rel, code := releaseRef(c, args[:2], policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	name := args[2]
	if !assetNamePat.MatchString(name) {
		return c.fail(protocol.ExitUsage, "invalid asset name %q: letters, digits, '._+-'; must not start with '.'", name)
	}
	dir := assetDir(c.Cfg.Server.Root, repo, rel.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	limit := c.Cfg.Limits.MaxAssetBytes
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(c.Stdin, limit+1))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading asset: %v", err)
	}
	if n > limit {
		return c.fail(protocol.ExitUsage, "asset exceeds max_asset_bytes (%d)", limit)
	}
	if n == 0 {
		return c.fail(protocol.ExitUsage, "empty asset: pipe the file on stdin")
	}
	if err := tmp.Close(); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := c.Store.AddReleaseAsset(rel.ID, name, n, sum); err != nil {
		return c.failErr(err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		c.Store.RemoveReleaseAsset(rel.ID, name)
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(assetOut{name, n, sum}, func(w io.Writer) {
		fmt.Fprintf(w, "uploaded %s (%d bytes, sha256 %s)\n", name, n, sum)
	})
}

func runAssetGet(c *Ctx, args []string) int {
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: release asset get <owner/name> <tag> <filename> > file")
	}
	repo, rel, code := releaseRef(c, args[:2], policy.CanRead)
	if code >= 0 {
		return code
	}
	name := args[2]
	if !assetNamePat.MatchString(name) {
		return c.fail(protocol.ExitNotFound, "no asset %q", name)
	}
	f, err := os.Open(filepath.Join(assetDir(c.Cfg.Server.Root, repo, rel.ID), name))
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no asset %q on release %s", name, rel.Tag)
	}
	defer f.Close()
	if _, err := io.Copy(c.Stdout, f); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}

func runAssetRemove(c *Ctx, args []string) int {
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: release asset remove <owner/name> <tag> <filename>")
	}
	repo, rel, code := releaseRef(c, args[:2], policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	name := args[2]
	if err := c.Store.RemoveReleaseAsset(rel.ID, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no asset %q on release %s", name, rel.Tag)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if assetNamePat.MatchString(name) {
		os.Remove(filepath.Join(assetDir(c.Cfg.Server.Root, repo, rel.ID), name))
	}
	return c.emit(map[string]string{"removed": name}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", name)
	})
}
