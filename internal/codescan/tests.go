package codescan

import (
	"path"
	"strings"
)

// insecureRandomRule flags random number generators that are not
// cryptographically secure. In a test the numbers protect nothing.
const insecureRandomRule = "insecure-random"

// testPath reports whether a path is a test by the usual layouts: a tests
// directory, or a file named as one.
func testPath(p string) bool {
	dirs := strings.Split(path.Dir(p), "/")
	for _, d := range dirs {
		switch {
		case d == "test", d == "tests", d == "__tests__", d == "spec", d == "testdata", strings.HasSuffix(d, "Tests"):
			return true
		}
	}
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"),
		base == "conftest.py",
		strings.HasSuffix(stem, ".test"), strings.HasSuffix(stem, ".spec"),
		path.Ext(base) == ".swift" && (strings.HasSuffix(stem, "Tests") || strings.HasSuffix(stem, "Test")),
		(path.Ext(base) == ".java" || path.Ext(base) == ".kt") && strings.HasSuffix(stem, "Test"):
		return true
	}
	return false
}
