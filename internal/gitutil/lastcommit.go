package gitutil

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// EntryCommit is the newest commit touching one entry of a tree listing.
type EntryCommit struct {
	SHA     string
	Subject string
	Author  string
	Email   string
	When    time.Time
}

// lastCommitScan bounds the history walk. A directory whose entries are
// all recently touched resolves in a handful of commits; this only caps
// the pathological case, where the remaining entries are reported absent
// rather than costing an unbounded scan on every page view.
const lastCommitScan = 2000

// LastCommits resolves the newest commit touching each of names directly
// under path, for the tree at ref.
//
// One git log process serves the whole listing rather than one per entry:
// the walk streams newest-first and is killed as soon as every name is
// accounted for, so an active directory reads only the few commits it
// needs no matter how deep the history goes. Names still unresolved when
// the walk ends are absent from the map, and callers render them blank.
func LastCommits(dir, ref, path string, names []string) map[string]EntryCommit {
	if len(names) == 0 {
		return nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	prefix := ""
	if path != "" {
		prefix = strings.TrimSuffix(path, "/") + "/"
	}

	args := []string{"-C", dir, "log", "--first-parent", "--name-only",
		"--format=%x1e%H%x1f%ct%x1f%an%x1f%ae%x1f%s", "-n", strconv.Itoa(lastCommitScan), ref}
	if prefix != "" {
		args = append(args, "--", strings.TrimSuffix(prefix, "/"))
	}
	cmd := exec.Command("git", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	defer func() {
		// The walk usually ends early; stop git rather than let it finish
		// reading history nobody is going to look at.
		cmd.Process.Kill()
		cmd.Wait()
	}()

	out := make(map[string]EntryCommit, len(names))
	var cur EntryCommit
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "\x1e") {
			cur = parseCommitHeader(line[1:])
			continue
		}
		if line == "" || cur.SHA == "" {
			continue
		}
		name, ok := entryName(line, prefix)
		if !ok || !want[name] {
			continue
		}
		out[name] = cur
		delete(want, name)
		if len(want) == 0 {
			break
		}
	}
	return out
}

// parseCommitHeader reads sha, commit time, author name and address, and
// subject, unit separated so a subject containing spaces stays intact.
func parseCommitHeader(s string) EntryCommit {
	f := strings.SplitN(s, "\x1f", 5)
	if len(f) != 5 {
		return EntryCommit{}
	}
	c := EntryCommit{SHA: f[0], Author: f[2], Email: f[3], Subject: f[4]}
	if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
		c.When = time.Unix(n, 0).UTC()
	}
	return c
}

// TipCommit is the commit at ref, for the bar above a tree listing that
// answers "who touched this repository last".
func TipCommit(dir, ref string) EntryCommit {
	out, err := exec.Command("git", "-C", dir, "log", "-1",
		"--format=%H%x1f%ct%x1f%an%x1f%ae%x1f%s", ref).Output()
	if err != nil {
		return EntryCommit{}
	}
	return parseCommitHeader(strings.TrimRight(string(out), "\n"))
}

// entryName maps a changed path to the listing entry that contains it:
// "internal/web/web.go" under prefix "internal/" is the entry "web".
func entryName(changed, prefix string) (string, bool) {
	if prefix != "" {
		if !strings.HasPrefix(changed, prefix) {
			return "", false
		}
		changed = changed[len(prefix):]
	}
	if changed == "" {
		return "", false
	}
	if i := strings.IndexByte(changed, '/'); i >= 0 {
		return changed[:i], true
	}
	return changed, true
}

// StatPath returns the tree entry for a single path at ref, so a blob
// page can report the facts the file listing no longer carries: its size,
// and whether it is executable or a symlink.
func StatPath(dir, ref, path string) (TreeEntry, bool) {
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-l", ref, "--", path).Output()
	if err != nil {
		return TreeEntry{}, false
	}
	line := strings.TrimRight(string(out), "\n")
	meta, name, ok := strings.Cut(line, "\t")
	if !ok {
		return TreeEntry{}, false
	}
	f := strings.Fields(meta)
	if len(f) != 4 {
		return TreeEntry{}, false
	}
	size := int64(-1)
	if f[3] != "-" {
		size, _ = strconv.ParseInt(f[3], 10, 64)
	}
	return TreeEntry{Mode: f[0], Type: f[1], SHA: f[2], Size: size, Name: name}, true
}
