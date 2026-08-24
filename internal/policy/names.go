// Package policy holds access-control and naming rules.
package policy

import (
	"fmt"
	"regexp"
)

// reservedNames are forbidden as usernames and org names because they are, or
// will be, top-level web routes (the UI serves /<owner>/<name>). Any change to
// the httpd mux's top-level routes must be reflected here; the httpd package
// asserts this in its tests.
var reservedNames = map[string]bool{
	"admin":       true,
	"api":         true,
	"archive":     true,
	"explore":     true,
	"favicon.svg": true,
	"gitbay":      true, // vanity go-import path on gitbay.org
	"login":       true,
	"logout":      true,
	"new":         true,
	"raw":         true,
	"register":    true,
	"settings":    true,
	"static":      true,
}

// namePat matches valid user, org, and repo names: lowercase alphanumerics,
// dot, dash, underscore; must start with an alphanumeric. Dots are further
// restricted by ValidateName to avoid "." / ".." and ".git" suffixes.
var namePat = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidateOwnerName checks a username or org name.
func ValidateOwnerName(name string) error {
	if err := ValidateName(name); err != nil {
		return err
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
	if len(name) > 4 && name[len(name)-4:] == ".git" {
		return fmt.Errorf("invalid name %q: must not end in .git", name)
	}
	return nil
}

// Reserved reports whether name is a reserved route word. Exported so the
// httpd tests can assert route/reserved-list agreement.
func Reserved(name string) bool { return reservedNames[name] }
