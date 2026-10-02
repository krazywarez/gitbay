package gitutil

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// NumStat is one file's line counts between two commits. A binary file
// counts -1 each way. Status is A, M, D or R.
type NumStat struct {
	Path    string
	Added   int
	Deleted int
	Status  string
}

// DiffNumstat lists the files changed from base to head with their line
// counts and status, renames detected. A renamed file is listed under
// its new path.
func DiffNumstat(dir, base, head string) ([]NumStat, error) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command(toolpath.Look("git"), append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s %s: %w", args[0], args[1], err)
		}
		return string(out), nil
	}
	num, err := run("diff", "--numstat", "-z", "-M", "--end-of-options", base, head)
	if err != nil {
		return nil, err
	}
	names, err := run("diff", "--name-status", "-z", "-M", "--end-of-options", base, head)
	if err != nil {
		return nil, err
	}
	// --name-status -z: "M\0path\0", or "R100\0old\0new\0" for a rename
	// or copy.
	status := map[string]string{}
	f := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	for i := 0; i < len(f); {
		s := f[i]
		if s == "" {
			i++
			continue
		}
		if s[0] == 'R' || s[0] == 'C' {
			if i+2 < len(f) {
				status[f[i+2]] = s[:1]
			}
			i += 3
			continue
		}
		if i+1 < len(f) {
			status[f[i+1]] = s[:1]
		}
		i += 2
	}
	// --numstat -z: "a\td\tpath\0", or "a\td\t\0old\0new\0" for a rename.
	var out []NumStat
	recs := strings.Split(strings.TrimSuffix(num, "\x00"), "\x00")
	for i := 0; i < len(recs); i++ {
		parts := strings.SplitN(recs[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(recs) {
			path = recs[i+2]
			i += 2
		}
		ns := NumStat{Path: path, Added: -1, Deleted: -1, Status: status[path]}
		if parts[0] != "-" {
			ns.Added, _ = strconv.Atoi(parts[0])
			ns.Deleted, _ = strconv.Atoi(parts[1])
		}
		out = append(out, ns)
	}
	return out, nil
}
