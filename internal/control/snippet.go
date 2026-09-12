package control

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// A snippet keeps at most this many files; a paste is not a repository.
const maxSnippetFiles = 64

func init() {
	register(Command{Path: []string{"snippet", "create"},
		Summary:    "create a snippet from one file on stdin",
		Usage:      "snippet create <filename> [--description <d>] [--visibility public|unlisted|private] < file",
		ReadsStdin: true, Run: runSnippetCreate})
	register(Command{Path: []string{"snippet", "show"},
		Summary: "show a snippet's metadata and files",
		Usage:   "snippet show <id>", ReadOnly: true, Run: runSnippetShow})
	register(Command{Path: []string{"snippet", "list"},
		Summary: "list your snippets, or an owner's public ones",
		Usage:   "snippet list [<owner>] [--limit n] [--cursor c]", ReadOnly: true, Run: runSnippetList})
	register(Command{Path: []string{"snippet", "edit"},
		Summary: "change a snippet's description or visibility",
		Usage:   "snippet edit <id> [--description <d>] [--visibility public|unlisted|private]", Run: runSnippetEdit})
	register(Command{Path: []string{"snippet", "delete"},
		Summary: "delete a snippet and its files",
		Usage:   "snippet delete <id>", Run: runSnippetDelete})
	register(Command{Path: []string{"snippet", "file", "set"},
		Summary:    "add a file to a snippet, or replace one, from stdin",
		Usage:      "snippet file set <id> <filename> < file",
		ReadsStdin: true, Run: runSnippetFileSet})
	register(Command{Path: []string{"snippet", "file", "get"},
		Summary: "write a snippet file to stdout",
		Usage:   "snippet file get <id> <filename> > file", ReadOnly: true, Run: runSnippetFileGet})
	register(Command{Path: []string{"snippet", "file", "remove"},
		Summary: "remove a file from a snippet",
		Usage:   "snippet file remove <id> <filename>", Run: runSnippetFileRemove})
}

type SnippetFileOut struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"`
}

type SnippetOut struct {
	ID          string           `json:"id"`
	URL         string           `json:"url"`
	Owner       string           `json:"owner"`
	Description string           `json:"description"`
	Visibility  string           `json:"visibility"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	Files       []SnippetFileOut `json:"files"`
}

func snippetURL(c *Ctx, sn store.Snippet) string {
	return c.Cfg.Server.SiteURL + "/" + sn.OwnerName + "/-/snippets/" + sn.PublicID
}

func snippetOut(c *Ctx, sn store.Snippet) SnippetOut {
	o := SnippetOut{ID: sn.PublicID, URL: snippetURL(c, sn), Owner: sn.OwnerName,
		Description: sn.Description, Visibility: sn.Visibility,
		CreatedAt: sn.CreatedAt, UpdatedAt: sn.UpdatedAt, Files: []SnippetFileOut{}}
	for _, f := range sn.Files {
		o.Files = append(o.Files, SnippetFileOut{Name: f.Name, Size: f.Size, Content: string(f.Content)})
	}
	return o
}

func validSnippetVisibility(v string) bool {
	return v == "public" || v == "unlisted" || v == "private"
}

// snippetRef loads a snippet the caller may read; with write, one they
// may change. Unreadable and missing are the same not-found, so a
// private id cannot be confirmed by probing.
func snippetRef(c *Ctx, id string, write bool) (store.Snippet, int) {
	sn, err := c.Store.SnippetByPublicID(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return sn, c.fail(protocol.ExitFailure, "%v", err)
	}
	if err != nil || !policy.CanReadSnippet(c.User, sn) {
		return sn, c.fail(protocol.ExitNotFound, "no snippet %q", id)
	}
	if write && !policy.CanWriteSnippet(c.User, sn) {
		return sn, c.fail(protocol.ExitDenied, "snippet %s belongs to %s", id, sn.OwnerName)
	}
	return sn, -1
}

// readSnippetBody reads one file from stdin under the limit, and insists
// on text: the page highlights it and the raw route serves text/plain.
func readSnippetBody(c *Ctx) ([]byte, int) {
	limit := c.Cfg.Limits.MaxSnippetBytes
	data, err := io.ReadAll(io.LimitReader(c.Stdin, limit+1))
	if err != nil {
		return nil, c.fail(protocol.ExitFailure, "reading stdin: %v", err)
	}
	if int64(len(data)) > limit {
		return nil, c.fail(protocol.ExitUsage, "file exceeds max_snippet_bytes (%d)", limit)
	}
	if len(data) == 0 {
		return nil, c.fail(protocol.ExitUsage, "empty file: pipe it on stdin")
	}
	if !utf8.Valid(data) {
		return nil, c.fail(protocol.ExitUsage, "snippets hold text: the file is not valid UTF-8")
	}
	return data, -1
}

func checkSnippetFileName(c *Ctx, name string) int {
	if !assetNamePat.MatchString(name) {
		return c.fail(protocol.ExitUsage, "invalid file name %q: letters, digits, '._+-'; must not start with '.'", name)
	}
	return -1
}

func newSnippetID() string {
	buf := make([]byte, 6)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

func runSnippetCreate(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--description", "--visibility"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	name := f.pos(0)
	if name == "" {
		return c.usage()
	}
	if code := checkSnippetFileName(c, name); code >= 0 {
		return code
	}
	visibility := f.Value("--visibility")
	if visibility == "" {
		visibility = "unlisted"
	}
	if !validSnippetVisibility(visibility) {
		return c.fail(protocol.ExitUsage, "visibility is public, unlisted or private")
	}
	if limit := c.Cfg.Limits.MaxSnippetsPerUser; limit > 0 {
		n, err := c.Store.CountSnippets(c.User.ID, true)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if n >= limit {
			return c.fail(protocol.ExitUsage, "snippet limit reached (%d); delete one first", limit)
		}
	}
	data, code := readSnippetBody(c)
	if code >= 0 {
		return code
	}
	var pid string
	for try := 0; ; try++ {
		pid = newSnippetID()
		_, err = c.Store.CreateSnippet(c.User.ID, pid, f.Value("--description"), visibility, name, data)
		if !errors.Is(err, store.ErrExists) || try == 4 {
			break
		}
	}
	if err != nil {
		return c.failErr(err)
	}
	sn, err := c.Store.SnippetByPublicID(pid)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(snippetOut(c, sn), func(w io.Writer) {
		fmt.Fprintf(w, "created snippet %s\n%s\n", sn.PublicID, snippetURL(c, sn))
	})
}

func runSnippetShow(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	sn, code := snippetRef(c, args[0], false)
	if code >= 0 {
		return code
	}
	files, err := c.Store.SnippetFiles(sn.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	sn.Files = files
	return c.emit(snippetOut(c, sn), func(w io.Writer) {
		fmt.Fprintf(w, "snippet %s by %s (%s)\n", sn.PublicID, sn.OwnerName, sn.Visibility)
		if sn.Description != "" {
			fmt.Fprintf(w, "%s\n", sn.Description)
		}
		fmt.Fprintf(w, "%s\nupdated %s\n", snippetURL(c, sn), sn.UpdatedAt)
		for _, f := range files {
			fmt.Fprintf(w, "  %s\t%d bytes\n", f.Name, f.Size)
		}
	})
}

func runSnippetList(c *Ctx, args []string) int {
	rest, p, code := parsePageFlags(c, args, "snippet", true)
	if code >= 0 {
		return code
	}
	if len(rest) > 1 {
		return c.usage()
	}
	owner := c.User
	if len(rest) == 1 {
		u, err := c.Store.UserByUsername(rest[0])
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no user %q", rest[0])
		}
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		owner = u
	}
	all := owner.ID == c.User.ID || c.User.IsAdmin
	rows, err := c.Store.ListSnippets(owner.ID, all, p.queryLimit(), p.keyInt())
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	rows, next := trimPage(p, rows, "snippet", func(sn store.Snippet) string { return strconv.FormatInt(sn.ID, 10) })
	items := make([]SnippetOut, 0, len(rows))
	for _, sn := range rows {
		items = append(items, snippetOut(c, sn))
	}
	return c.emitPage(p, items, next, func(w io.Writer) {
		for _, sn := range rows {
			names := ""
			for i, f := range sn.Files {
				if i > 0 {
					names += ", "
				}
				names += f.Name
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", sn.PublicID, sn.Visibility, names, sn.Description)
		}
	})
}

func runSnippetEdit(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--description", "--visibility"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if f.pos(0) == "" || (!f.Has("--description") && !f.Has("--visibility")) {
		return c.usage()
	}
	sn, code := snippetRef(c, f.pos(0), true)
	if code >= 0 {
		return code
	}
	description, visibility := sn.Description, sn.Visibility
	if f.Has("--description") {
		description = f.Value("--description")
	}
	if f.Has("--visibility") {
		visibility = f.Value("--visibility")
		if !validSnippetVisibility(visibility) {
			return c.fail(protocol.ExitUsage, "visibility is public, unlisted or private")
		}
	}
	if err := c.Store.UpdateSnippet(sn.ID, description, visibility); err != nil {
		return c.failErr(err)
	}
	sn, err = c.Store.SnippetByPublicID(sn.PublicID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(snippetOut(c, sn), func(w io.Writer) {
		fmt.Fprintf(w, "updated snippet %s (%s)\n", sn.PublicID, sn.Visibility)
	})
}

func runSnippetDelete(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	sn, code := snippetRef(c, args[0], true)
	if code >= 0 {
		return code
	}
	if err := c.Store.DeleteSnippet(sn.ID); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]string{"id": sn.PublicID}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted snippet %s\n", sn.PublicID)
	})
}

func runSnippetFileSet(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.usage()
	}
	sn, code := snippetRef(c, args[0], true)
	if code >= 0 {
		return code
	}
	name := args[1]
	if code := checkSnippetFileName(c, name); code >= 0 {
		return code
	}
	exists := false
	for _, f := range sn.Files {
		exists = exists || f.Name == name
	}
	if !exists && len(sn.Files) >= maxSnippetFiles {
		return c.fail(protocol.ExitUsage, "a snippet holds at most %d files", maxSnippetFiles)
	}
	data, code := readSnippetBody(c)
	if code >= 0 {
		return code
	}
	if err := c.Store.SetSnippetFile(sn.ID, name, data); err != nil {
		return c.failErr(err)
	}
	return c.emit(SnippetFileOut{Name: name, Size: int64(len(data))}, func(w io.Writer) {
		fmt.Fprintf(w, "set %s (%d bytes) on snippet %s\n", name, len(data), sn.PublicID)
	})
}

func runSnippetFileGet(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.usage()
	}
	sn, code := snippetRef(c, args[0], false)
	if code >= 0 {
		return code
	}
	f, err := c.Store.SnippetFile(sn.ID, args[1])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no file %q in snippet %s", args[1], sn.PublicID)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if c.JSON {
		return c.emit(SnippetFileOut{Name: f.Name, Size: f.Size, Content: string(f.Content)}, nil)
	}
	if _, err := c.Stdout.Write(f.Content); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}

func runSnippetFileRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.usage()
	}
	sn, code := snippetRef(c, args[0], true)
	if code >= 0 {
		return code
	}
	if len(sn.Files) == 1 && sn.Files[0].Name == args[1] {
		return c.fail(protocol.ExitUsage, "a snippet keeps at least one file; delete the snippet instead")
	}
	err := c.Store.RemoveSnippetFile(sn.ID, args[1])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no file %q in snippet %s", args[1], sn.PublicID)
	}
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]string{"id": sn.PublicID, "name": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s from snippet %s\n", args[1], sn.PublicID)
	})
}
