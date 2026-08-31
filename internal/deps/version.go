// Package deps reports dependencies that are behind their upstream
// release. It reads manifests out of a repository's default branch and
// asks the ecosystem's registry what the current version is; it never
// executes a package manager, so the check needs no runner and no
// per-repo configuration beyond opting in.
package deps

import (
	"strconv"
	"strings"
)

// Newer reports whether latest is a strictly greater release than current.
// The four ecosystems agree on the part that matters here — dot-separated
// numbers, optionally followed by a suffix — so one tolerant comparison
// covers all of them. Anything it cannot read compares equal, which
// reports nothing rather than reporting noise.
func Newer(current, latest string) bool {
	ca, cs := split(current)
	la, ls := split(latest)
	if len(ca) == 0 || len(la) == 0 {
		return false
	}
	for i := 0; i < len(ca) || i < len(la); i++ {
		c, l := at(ca, i), at(la, i)
		if c != l {
			return l > c
		}
	}
	// Same release numbers: the suffix decides. A prerelease is behind the
	// plain release, which is behind a post-release.
	return rank(ls) > rank(cs)
}

// IsPrerelease reports whether v carries a prerelease marker. Registries
// mostly hand back stable versions already; this keeps the exceptions from
// being suggested.
func IsPrerelease(v string) bool {
	_, suffix := split(v)
	return rank(suffix) < 0
}

// split separates the leading dot-separated numbers from whatever follows:
// "v1.2.3-rc1" becomes ([1 2 3], "-rc1"), "2.0b1" becomes ([2 0], "b1").
func split(v string) ([]int, string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	var nums []int
	i := 0
	for i < len(v) {
		j := i
		for j < len(v) && v[j] >= '0' && v[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		n, err := strconv.Atoi(v[i:j])
		if err != nil {
			break
		}
		nums = append(nums, n)
		if j < len(v) && v[j] == '.' && j+1 < len(v) && v[j+1] >= '0' && v[j+1] <= '9' {
			i = j + 1
			continue
		}
		i = j
		break
	}
	return nums, v[i:]
}

func at(nums []int, i int) int {
	if i < len(nums) {
		return nums[i]
	}
	return 0
}

// rank orders the three kinds of suffix a release can carry: -1 for a
// prerelease, 0 for none, 1 for a PEP 440 post-release. Two prereleases
// rank equal, which reports nothing rather than guessing at rc1 vs beta2.
func rank(suffix string) int {
	switch {
	case suffix == "":
		return 0
	case strings.HasPrefix(strings.TrimLeft(suffix, ".-_"), "post"):
		return 1
	}
	return -1
}
