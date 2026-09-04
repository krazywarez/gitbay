package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/toolpath"
)

type GrepMatch struct {
	Path string
	Line int
	Text string
}

// Grep runs a literal, case-insensitive git grep over the tree at ref,
// skipping binary files. Matches are capped at max; "no matches" is an
// empty result, not an error.
func Grep(dir, ref, query string, max int) ([]GrepMatch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// -z: NUL after the path and the line number, so paths containing
	// ':' parse unambiguously (format: "ref:path\0line\0text\n").
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir, "grep", "-nIiF", "-z", "-e", query, "--end-of-options", ref)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("git grep at %s: %w", ref, err)
	}
	var matches []GrepMatch
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		matches = append(matches, GrepMatch{
			Path: strings.TrimPrefix(parts[0], ref+":"),
			Line: n,
			Text: parts[2],
		})
		if len(matches) >= max {
			break
		}
	}
	return matches, nil
}
