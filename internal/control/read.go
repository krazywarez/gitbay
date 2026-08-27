package control

import (
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{
		Path:     []string{"repo", "tree"},
		Summary:  "list a directory: repo tree <owner/name> [<path>] [--ref <ref>]",
		ReadOnly: true,
		Run:      runRepoTree,
	})
	register(Command{
		Path:     []string{"repo", "cat"},
		Summary:  "read a file: repo cat <owner/name> <path> [--ref <ref>]",
		ReadOnly: true,
		Run:      runRepoCat,
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
