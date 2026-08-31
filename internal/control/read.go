package control

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{
		Path:     []string{"repo", "tree"},
		Summary:  "list a directory",
		Usage:    "repo tree <owner/name> [<path>] [--ref <ref>]",
		ReadOnly: true,
		Run:      runRepoTree,
	})
	register(Command{
		Path:     []string{"repo", "cat"},
		Summary:  "read a file",
		Usage:    "repo cat <owner/name> <path> [--ref <ref>]",
		ReadOnly: true,
		Run:      runRepoCat,
	})
	register(Command{
		Path:     []string{"repo", "blame"},
		Summary:  "attribute lines to commits",
		Usage:    "repo blame <owner/name> <path> [--ref <ref>] [--from <n>] [--to <n>]",
		ReadOnly: true,
		Run:      runRepoBlame,
	})
	register(Command{
		Path:     []string{"repo", "refs"},
		Summary:  "list branches and tags",
		Usage:    "repo refs <owner/name>",
		ReadOnly: true,
		Run:      runRepoRefs,
	})
}

func runRepoRefs(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo refs <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	branches, err := gitutil.Refs(dir, "heads")
	if err != nil {
		return c.fail(protocol.ExitFailure, "listing branches: %v", err)
	}
	tags, err := gitutil.Refs(dir, "tags")
	if err != nil {
		return c.fail(protocol.ExitFailure, "listing tags: %v", err)
	}
	type refOut struct {
		Name string `json:"name"`
		SHA  string `json:"sha"`
	}
	type out struct {
		Branches []refOut `json:"branches"`
		Tags     []refOut `json:"tags"`
	}
	d := out{Branches: []refOut{}, Tags: []refOut{}}
	for _, ref := range branches {
		d.Branches = append(d.Branches, refOut{Name: ref.Name, SHA: ref.SHA})
	}
	for _, ref := range tags {
		d.Tags = append(d.Tags, refOut{Name: ref.Name, SHA: ref.SHA})
	}
	return c.emit(d, func(w io.Writer) {
		for _, ref := range d.Branches {
			fmt.Fprintf(w, "branch\t%s\t%.10s\n", ref.Name, ref.SHA)
		}
		for _, ref := range d.Tags {
			fmt.Fprintf(w, "tag\t%s\t%.10s\n", ref.Name, ref.SHA)
		}
	})
}

// BlameSpan caps one blame request, and is the page size the web renders.
// An unbounded blame on a large file is a slow query for every surface.
const BlameSpan = 1000

func runRepoBlame(c *Ctx, args []string) int {
	const usage = "repo blame <owner/name> <path> [--ref <ref>] [--from <n>] [--to <n>]"
	var rest []string
	var ref string
	from, to := 0, 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ref", "--from", "--to":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			v := args[i+1]
			if args[i] == "--ref" {
				ref = v
			} else {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return c.fail(protocol.ExitUsage, "%s must be a positive line number", args[i])
				}
				if args[i] == "--from" {
					from = n
				} else {
					to = n
				}
			}
			i++
		default:
			if strings.HasPrefix(args[i], "--") {
				return c.fail(protocol.ExitUsage, "unknown flag %q\nusage: %s", args[i], usage)
			}
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 2 {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	repo, code := resolveRepo(c, rest[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	filePath, ok := cleanRepoPath(rest[1])
	if !ok || filePath == "" {
		return c.fail(protocol.ExitUsage, "path must stay inside the repository")
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, ref); err != nil {
		return c.fail(protocol.ExitNotFound, "no ref %q in %s", ref, repo.Path())
	}
	data, err := gitutil.ReadBlob(dir, ref, filePath, c.Cfg.Limits.MaxBlobBytes)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no such file %q in %s at %s", filePath, repo.Path(), ref)
	}
	if gitutil.IsBinary(data) {
		return c.fail(protocol.ExitUsage, "%s is binary; there is nothing to attribute", filePath)
	}
	total := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		total++
	}
	if total == 0 {
		return c.fail(protocol.ExitNotFound, "%s is empty at %s", filePath, ref)
	}
	if from == 0 {
		from = 1
	}
	if from > total {
		return c.fail(protocol.ExitUsage, "--from %d is past the end of %s (%d lines)", from, filePath, total)
	}
	if to == 0 || to > total {
		to = total
	}
	if to < from {
		return c.fail(protocol.ExitUsage, "--to must not precede --from")
	}
	// One span per call; a client pages with --from/--to.
	if to-from+1 > BlameSpan {
		to = from + BlameSpan - 1
	}
	raw, err := gitutil.Blame(dir, ref, filePath, from, to)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	type hunkOut struct {
		SHA         string   `json:"sha"`
		AuthorName  string   `json:"author_name"`
		AuthorEmail string   `json:"author_email"`
		Date        string   `json:"date"`
		Summary     string   `json:"summary"`
		StartLine   int      `json:"start_line"`
		Lines       []string `json:"lines"`
	}
	type out struct {
		Path       string    `json:"path"`
		Ref        string    `json:"ref"`
		File       string    `json:"file"`
		From       int       `json:"from"`
		To         int       `json:"to"`
		TotalLines int       `json:"total_lines"`
		Hunks      []hunkOut `json:"hunks"`
	}
	d := out{Path: repo.Path(), Ref: ref, File: filePath, From: from, To: to,
		TotalLines: total, Hunks: []hunkOut{}}
	for _, h := range raw {
		d.Hunks = append(d.Hunks, hunkOut{
			SHA: h.SHA, AuthorName: h.AuthorName, AuthorEmail: h.AuthorEmail,
			Date:      time.Unix(h.AuthorUnix, 0).UTC().Format(time.RFC3339),
			Summary:   h.Summary,
			StartLine: h.StartLine, Lines: h.Lines,
		})
	}
	return c.emit(d, func(w io.Writer) {
		for _, h := range d.Hunks {
			for i, line := range h.Lines {
				fmt.Fprintf(w, "%.10s\t%s\t%d\t%s\n", h.SHA, h.AuthorName, h.StartLine+i, line)
			}
		}
	})
}

// readArgs pulls the shared "<owner/name> [positional...] [--ref r]" shape
// off argv. Positionals are returned in order so each command can name them
// in its own usage message.
func readArgs(c *Ctx, args []string, usage string, maxPos int) (pos []string, ref string, code int) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ref":
			if i+1 >= len(args) {
				return nil, "", c.fail(protocol.ExitUsage, "--ref requires a value")
			}
			ref = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, "", c.fail(protocol.ExitUsage, "unknown flag %q\nusage: %s", args[i], usage)
			}
			if len(pos) >= maxPos {
				return nil, "", c.fail(protocol.ExitUsage, "usage: %s", usage)
			}
			pos = append(pos, args[i])
		}
	}
	return pos, ref, -1
}

// cleanRepoPath keeps a caller inside the repository: no absolute paths, no
// "..", no leading slash. git would resolve those against the work tree.
func cleanRepoPath(p string) (string, bool) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", true
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

// entryOut is one tree entry. The sha lets a client cache by object id
// rather than by path and ref, which is what makes an offline client
// tractable.
type entryOut struct {
	Name string `json:"name"`
	Type string `json:"type"` // blob | tree
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size,omitempty"` // absent for trees
}

func runRepoTree(c *Ctx, args []string) int {
	const usage = "repo tree <owner/name> [<path>] [--ref <ref>]"
	pos, ref, code := readArgs(c, args, usage, 2)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	repo, code := resolveRepo(c, pos[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	dirPath := ""
	if len(pos) == 2 {
		var ok bool
		if dirPath, ok = cleanRepoPath(pos[1]); !ok {
			return c.fail(protocol.ExitUsage, "path must stay inside the repository")
		}
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, ref); err != nil {
		return c.fail(protocol.ExitNotFound, "no ref %q in %s", ref, repo.Path())
	}
	entries, err := gitutil.ListTree(dir, ref, dirPath)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no such path %q in %s at %s", dirPath, repo.Path(), ref)
	}

	type out struct {
		Path    string     `json:"path"`
		Ref     string     `json:"ref"`
		Dir     string     `json:"dir"`
		Entries []entryOut `json:"entries"`
	}
	d := out{Path: repo.Path(), Ref: ref, Dir: dirPath, Entries: []entryOut{}}
	for _, e := range entries {
		eo := entryOut{Name: e.Name, Type: e.Type, Mode: e.Mode, SHA: e.SHA}
		if e.Type != "tree" && e.Size >= 0 {
			eo.Size = e.Size
		}
		d.Entries = append(d.Entries, eo)
	}
	return c.emit(d, func(w io.Writer) {
		for _, e := range d.Entries {
			name := e.Name
			if e.Type == "tree" {
				name += "/"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", e.SHA[:min(10, len(e.SHA))], sizeCol(e), name)
		}
	})
}

func sizeCol(e entryOut) string {
	if e.Type == "tree" {
		return "-"
	}
	return fmt.Sprintf("%d", e.Size)
}

func runRepoCat(c *Ctx, args []string) int {
	const usage = "repo cat <owner/name> <path> [--ref <ref>]"
	pos, ref, code := readArgs(c, args, usage, 2)
	if code >= 0 {
		return code
	}
	if len(pos) != 2 {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	repo, code := resolveRepo(c, pos[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	filePath, ok := cleanRepoPath(pos[1])
	if !ok || filePath == "" {
		return c.fail(protocol.ExitUsage, "path must stay inside the repository")
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, ref); err != nil {
		return c.fail(protocol.ExitNotFound, "no ref %q in %s", ref, repo.Path())
	}
	// One byte over the cap distinguishes "exactly at the limit" from
	// "truncated", so a client is told which it got.
	limit := c.Cfg.Limits.MaxBlobBytes
	data, err := gitutil.ReadBlob(dir, ref, filePath, limit+1)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no such file %q in %s at %s", filePath, repo.Path(), ref)
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	binary := gitutil.IsBinary(data)

	type out struct {
		Path      string `json:"path"`
		Ref       string `json:"ref"`
		File      string `json:"file"`
		Size      int    `json:"size"`
		Binary    bool   `json:"binary"`
		Truncated bool   `json:"truncated,omitempty"`
		// Exactly one of these is set: text for UTF-8-safe content,
		// base64 for anything else, so a client never has to guess.
		Content string `json:"content,omitempty"`
		Base64  string `json:"base64,omitempty"`
	}
	d := out{Path: repo.Path(), Ref: ref, File: filePath, Size: len(data),
		Binary: binary, Truncated: truncated}
	if binary {
		d.Base64 = base64.StdEncoding.EncodeToString(data)
	} else {
		d.Content = string(data)
	}
	return c.emit(d, func(w io.Writer) {
		if binary {
			fmt.Fprintf(w, "%s: %d bytes of binary content (use --json for base64)\n", filePath, len(data))
			return
		}
		w.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Fprintln(w)
		}
	})
}
