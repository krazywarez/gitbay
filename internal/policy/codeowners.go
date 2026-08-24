package policy

import (
	"path"
	"strings"
)

// CodeownersRule is one line of a CODEOWNERS file: a pattern and the users
// who own paths matching it.
type CodeownersRule struct {
	Pattern string
	Owners  []string // usernames, @ stripped
}

// ParseCodeowners reads CODEOWNERS content: one rule per line, gitignore-
// style pattern followed by @user owners; #-comments and blanks ignored.
func ParseCodeowners(content string) []CodeownersRule {
	var rules []CodeownersRule
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var owners []string
		for _, f := range fields[1:] {
			owners = append(owners, strings.TrimPrefix(f, "@"))
		}
		rules = append(rules, CodeownersRule{Pattern: fields[0], Owners: owners})
	}
	return rules
}

// OwnersFor returns the owners of a path: the last matching rule wins,
// CODEOWNERS convention. nil means unowned.
func OwnersFor(rules []CodeownersRule, filePath string) []string {
	var owners []string
	for _, r := range rules {
		if codeownersMatch(r.Pattern, filePath) {
			owners = r.Owners
		}
	}
	return owners
}

// codeownersMatch implements the pattern subset gitbay supports:
//   - "*"            everything
//   - "*.go"         extension match on the basename, anywhere
//   - "docs/"        directory prefix (anywhere unless anchored with /)
//   - "/cmd/x.go"    exact or glob path anchored at the root
//   - "internal/*"   glob against the full path (one segment per *)
func codeownersMatch(pattern, filePath string) bool {
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "*" {
		return true
	}
	// Directory rule: everything under it.
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		if strings.HasPrefix(filePath, dir+"/") {
			return true
		}
		if !anchored && strings.Contains(filePath, "/"+dir+"/") {
			return true
		}
		return false
	}
	// Bare pattern without a slash: match the basename anywhere.
	if !strings.Contains(pattern, "/") && !anchored {
		ok, _ := path.Match(pattern, path.Base(filePath))
		return ok
	}
	ok, _ := path.Match(pattern, filePath)
	return ok
}
