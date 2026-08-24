package gitutil

import (
	"fmt"
	"os/exec"
	"strings"
)

const zeroSHA = "0000000000000000000000000000000000000000"

// Parents returns a commit's parent shas.
func Parents(dir, sha string) []string {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%P", sha).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// LastCommitDate returns the committer date (YYYY-MM-DD) of the ref tip,
// or "" for empty repos.
func LastCommitDate(dir, ref string) string {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%cs", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// HasCommit reports whether sha names a commit object present in dir.
func HasCommit(dir, sha string) bool {
	return exec.Command("git", "-C", dir, "cat-file", "-e", sha+"^{commit}").Run() == nil
}

type CommitMsg struct {
	SHA     string
	Message string
}

// RevListMessages returns sha and full message for commits reachable from
// new but not old, newest first, capped at max. An empty or zero old (new
// branch) lists from new alone, still capped.
func RevListMessages(dir, old, new string, max int) ([]CommitMsg, error) {
	args := []string{"-C", dir, "rev-list", fmt.Sprintf("-n%d", max), "--format=%B%x00", new}
	if old != "" && old != zeroSHA {
		args = append(args, "^"+old)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("rev-list messages: %w", err)
	}
	var msgs []CommitMsg
	for _, chunk := range strings.Split(string(out), "\x00") {
		chunk = strings.TrimLeft(chunk, "\n")
		if chunk == "" {
			continue
		}
		header, body, ok := strings.Cut(chunk, "\n")
		if !ok || !strings.HasPrefix(header, "commit ") {
			continue
		}
		msgs = append(msgs, CommitMsg{SHA: strings.TrimPrefix(header, "commit "), Message: strings.TrimSpace(body)})
	}
	return msgs, nil
}
