// Package buildinfo reports which commit a binary was built from, so a
// deployed artifact can state its provenance instead of needing a rebuild and
// a hash comparison to infer it.
package buildinfo

import "runtime/debug"

// Commit is stamped at link time by the Makefile:
//
//	-X gitbay.org/gitbay/internal/buildinfo.Commit=<sha>
//
// It carries a -dirty suffix when the tree was not clean, which only happens
// under ALLOW_DIRTY=1 since preflight otherwise refuses to build.
var Commit string

// String returns the build's commit. A binary built by hand rather than by the
// Makefile has no stamp, so fall back to the revision the toolchain embeds;
// that one is absent too when the build ran outside a checkout.
func String() string {
	if Commit != "" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev + modified
}
