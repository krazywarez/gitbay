// Package policy holds access-control and naming rules.
package policy

import (
	"fmt"
	"regexp"
	"strings"
)

// reservedNames are forbidden as usernames and org names because they are, or
// will be, top-level web routes (the UI serves /<owner>/<name>). Any change to
// the httpd mux's top-level routes must be reflected here; the httpd package
// asserts this in its tests.
var reservedNames = map[string]bool{
	"admin":         true,
	"api":           true,
	"archive":       true,
	"bookmarks":     true,
	"explore":       true,
	"favicon.svg":   true,
	"gitbay":        true, // vanity go-import path on gitbay.org
	"gitbay-bot":    true, // authors dependency-update issues
	"healthz":       true,
	"login":         true,
	"logout":        true,
	"new":           true,
	"notifications": true,
	"privacy":       true,
	"raw":           true,
	"register":      true,
	"search":        true,
	"settings":      true,
	"static":        true,
}

// namePat matches valid user, org, and repo names: lowercase alphanumerics,
// dot, dash, underscore; must start with an alphanumeric, or with a single
// dot before one. A leading dot marks a repository as infrastructure rather
// than a project — .gitbay holds an owner's profile content — and is refused
// for owners by ValidateOwnerName. Dots are further restricted by
// ValidateName to avoid "." / ".." and ".git" suffixes.
var namePat = regexp.MustCompile(`^\.?[a-z0-9][a-z0-9._-]{0,61}$`)

// ValidateOwnerName checks a username or org name.
func ValidateOwnerName(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	// The leading dot is a repository affordance. An owner is a top-level
	// route, and /.gitbay is not one.
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid name %q: must start with a letter or digit", name)
	}
	if reservedNames[name] {
		return fmt.Errorf("name %q is reserved", name)
	}
	return nil
}

// ValidateName checks a repo name (reserved words are allowed for repos;
// routes are namespaced under the owner).
func ValidateName(name string) error {
	if !namePat.MatchString(name) {
		return fmt.Errorf("invalid name %q: lowercase letters, digits, '.', '-', '_' only; must start with a letter or digit; max 63 chars", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid name %q", name)
	}
	// HasSuffix covers "repo.git" and the bare ".git" the leading-dot rule
	// would otherwise let through.
	if strings.HasSuffix(name, ".git") {
		return fmt.Errorf("invalid name %q: must not end in .git", name)
	}
	// /{owner}/activity.atom is the owner's feed; a repository by that
	// name would be unreachable.
	if strings.HasSuffix(name, ".atom") {
		return fmt.Errorf("invalid name %q: must not end in .atom", name)
	}
	return nil
}

// Reserved reports whether name is a reserved route word. Exported so the
// httpd tests can assert route/reserved-list agreement.
func Reserved(name string) bool { return reservedNames[name] }
