package gitutil

import (
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// CountCommits returns the number of commits reachable from ref, or 0 when
// the ref does not resolve (an empty repository).
func CountCommits(dir, ref string) int {
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", ref).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// Contributor is one author of a repository's history.
type Contributor struct {
	Name    string
	Email   string
	Commits int
}

// Contributors summarises authorship reachable from ref, most commits
// first. Identities are keyed by email, since that is what the forge can
// tie back to an account.
func Contributors(dir, ref string, max int) []Contributor {
	out, err := exec.Command("git", "-C", dir, "log", "--format=%an%x01%ae", ref).Output()
	if err != nil {
		return nil
	}
	byEmail := map[string]*Contributor{}
	var order []string
	for _, line := range strings.Split(string(out), "\n") {
		name, email, ok := strings.Cut(line, "\x01")
		if !ok || email == "" {
			continue
		}
		c, seen := byEmail[email]
		if !seen {
			byEmail[email] = &Contributor{Name: name, Email: email, Commits: 1}
			order = append(order, email)
			continue
		}
		c.Commits++
	}
	list := make([]Contributor, 0, len(order))
	for _, e := range order {
		list = append(list, *byEmail[e])
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Commits > list[j].Commits })
	if max > 0 && len(list) > max {
		list = list[:max]
	}
	return list
}

// Languages reports the byte share of each language in the tree at ref,
// largest first, keyed by the extension map the caller supplies. Only
// blobs count; git's own metadata does not.
func Languages(dir, ref string, lang func(path string) string) []Language {
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-r", "-l", "--full-name", ref).Output()
	if err != nil {
		return nil
	}
	bytesBy := map[string]int64{}
	var total int64
	for _, line := range strings.Split(string(out), "\n") {
		// <mode> blob <sha> <size>\t<path>
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 || f[1] != "blob" {
			continue
		}
		size, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			continue
		}
		name := lang(path)
		if name == "" {
			continue
		}
		bytesBy[name] += size
		total += size
	}
	if total == 0 {
		return nil
	}
	langs := make([]Language, 0, len(bytesBy))
	for name, b := range bytesBy {
		langs = append(langs, Language{Name: name, Bytes: b,
			Percent: float64(b) * 100 / float64(total)})
	}
	sort.Slice(langs, func(i, j int) bool {
		if langs[i].Bytes != langs[j].Bytes {
			return langs[i].Bytes > langs[j].Bytes
		}
		return langs[i].Name < langs[j].Name
	})
	return langs
}

type Language struct {
	Name    string
	Bytes   int64
	Percent float64
}
