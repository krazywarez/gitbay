package ci

import (
	"path"
	"strings"
)

// zeroSHA is git's null object id: the old side of a ref update that
// created the ref, carrying no diff base.
const zeroSHA = "0000000000000000000000000000000000000000"

// Match reports whether a changed file path matches a job's glob.
// A trailing /** matches the directory and everything below it at any
// depth; anything else goes to path.Match, whose * stops at a separator.
func Match(pattern, file string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return file == prefix || strings.HasPrefix(file, prefix+"/")
	}
	ok, err := path.Match(pattern, file)
	return err == nil && ok
}

// Selected reports whether a job's path filters admit a push that
// changed the given files. Call it only once the changed-file list is
// known; a caller that cannot compute one must run the job instead of
// calling this.
//
// The job runs when Paths is empty or at least one file matches one of
// its patterns, and is then held back only if PathsIgnore is non-empty
// and every file matches one of its patterns.
func Selected(j Job, files []string) bool {
	if len(j.Paths) > 0 {
		hit := false
		for _, f := range files {
			for _, p := range j.Paths {
				if Match(p, f) {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	if len(j.PathsIgnore) > 0 {
		for _, f := range files {
			ignored := false
			for _, p := range j.PathsIgnore {
				if Match(p, f) {
					ignored = true
				}
			}
			if !ignored {
				return true
			}
		}
		return false
	}
	return true
}

// HasDiffBase reports whether old names a commit a diff can start from.
// A branch's first push carries an empty or all-zero old sha, so there
// is nothing to compare the new commit against.
func HasDiffBase(old string) bool {
	return old != "" && old != zeroSHA
}
