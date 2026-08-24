package gitutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// FetchInto copies srcRef from srcDir into dstDir as dstRef, forcing the
// update. Objects are copied, not shared — the destination owns everything
// afterward, which is what keeps MRs alive when their fork is deleted.
func FetchInto(dstDir, srcDir, srcRef, dstRef string) error {
	cmd := exec.Command("git", "-C", dstDir, "fetch", "--quiet", "--no-write-fetch-head",
		srcDir, "+"+srcRef+":"+dstRef)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetch %s from %s: %v\n%s", srcRef, srcDir, err, out)
	}
	return nil
}

// UpdateRefCAS points ref at newSHA only if it currently points at oldSHA
// (empty oldSHA = must not exist). This is the compare-and-swap that makes
// merges safe against concurrent pushes.
func UpdateRefCAS(dir, ref, newSHA, oldSHA string) error {
	args := []string{"-C", dir, "update-ref", ref, newSHA}
	if oldSHA != "" {
		args = append(args, oldSHA)
	}
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update-ref %s: %v\n%s", ref, err, out)
	}
	return nil
}

func DeleteRef(dir, ref string) error {
	cmd := exec.Command("git", "-C", dir, "update-ref", "-d", ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("delete-ref %s: %v\n%s", ref, err, out)
	}
	return nil
}

// RevListRange returns commits in old..new, newest first.
func RevListRange(dir, old, new string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-list", new, "^"+old)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rev-list %s..%s: %w", old, new, err)
	}
	var shas []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			shas = append(shas, l)
		}
	}
	return shas, nil
}

// MergeTree performs a real merge of ours and theirs, returning the merged
// tree id. conflict=true means the merge cannot be done automatically.
func MergeTree(dir, ours, theirs string) (tree string, conflict bool, err error) {
	cmd := exec.Command("git", "-C", dir, "merge-tree", "--write-tree", ours, theirs)
	out, runErr := cmd.Output()
	tree = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", true, nil // conflicted merge
		}
		return "", false, fmt.Errorf("merge-tree: %w", runErr)
	}
	return tree, false, nil
}

// CommitTree creates a merge commit with the given parents, authored and
// committed by the merging user. There is no server signing key by design.
func CommitTree(dir, tree string, parents []string, name, email, message string) (string, error) {
	args := []string{"-C", dir, "commit-tree", tree, "-m", message}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Diff returns the patch for old..new (three-dot semantics are the caller's
// job: pass the merge base as old).
func Diff(dir, old, new string, limit int64) (string, error) {
	cmd := exec.Command("git", "-C", dir, "diff", "--stat", "--patch", old, new)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("diff: %w", err)
	}
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return string(out), nil
}

// MergeBase returns the best common ancestor, or an error if none exists.
func MergeBase(dir, a, b string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "merge-base", a, b)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no common history between %s and %s", a, b)
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitFileChange writes content at path on branch as a new commit and
// advances the branch with compare-and-swap. Used by web edits; hooks do not
// run, so callers enforce policy themselves.
func CommitFileChange(dir, branch, path string, content []byte, name, email, message string) (string, error) {
	branchRef := "refs/heads/" + branch
	parent, err := ResolveRef(dir, branchRef)
	if err != nil {
		return "", fmt.Errorf("branch %s: %w", branch, err)
	}

	// Hash the new blob.
	hb := exec.Command("git", "-C", dir, "hash-object", "-w", "--stdin")
	hb.Stdin = strings.NewReader(string(content))
	out, err := hb.Output()
	if err != nil {
		return "", fmt.Errorf("hash-object: %w", err)
	}
	blob := strings.TrimSpace(string(out))

	// Stage the parent tree in a temporary index, splice the blob in, and
	// write the new tree.
	idx, err := os.CreateTemp("", "gitbay-index-*")
	if err != nil {
		return "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx.Name())

	rt := exec.Command("git", "-C", dir, "read-tree", parent+"^{tree}")
	rt.Env = env
	if out, err := rt.CombinedOutput(); err != nil {
		return "", fmt.Errorf("read-tree: %v\n%s", err, out)
	}
	ui := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	ui.Env = env
	if out, err := ui.CombinedOutput(); err != nil {
		return "", fmt.Errorf("update-index: %v\n%s", err, out)
	}
	wt := exec.Command("git", "-C", dir, "write-tree")
	wt.Env = env
	out, err = wt.Output()
	if err != nil {
		return "", fmt.Errorf("write-tree: %w", err)
	}
	tree := strings.TrimSpace(string(out))

	sha, err := CommitTree(dir, tree, []string{parent}, name, email, message)
	if err != nil {
		return "", err
	}
	if err := UpdateRefCAS(dir, branchRef, sha, parent); err != nil {
		return "", fmt.Errorf("branch moved during edit; reload and retry: %w", err)
	}
	return sha, nil
}

// CommitParents returns the parent SHAs of a commit.
func CommitParents(dir, sha string) ([]string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-list", "--parents", "-n1", sha).Output()
	if err != nil {
		return nil, fmt.Errorf("rev-list --parents %s: %w", sha, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		return nil, fmt.Errorf("no output for %s", sha)
	}
	return fields[1:], nil
}

// AuthorIdent returns a commit's author name, email, and ISO date.
func AuthorIdent(dir, sha string) (name, email, date string, err error) {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%an%x1f%ae%x1f%aI", sha).Output()
	if err != nil {
		return "", "", "", fmt.Errorf("log %s: %w", sha, err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\x1f", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("bad ident for %s", sha)
	}
	return parts[0], parts[1], parts[2], nil
}

// CommitMessage returns a commit's full message.
func CommitMessage(dir, sha string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%B", sha).Output()
	if err != nil {
		return "", fmt.Errorf("log %s: %w", sha, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// MergeTreeOnto replays commit's changes (relative to base) onto onto,
// returning the resulting tree. conflict=true when it cannot apply cleanly.
func MergeTreeOnto(dir, base, onto, commit string) (tree string, conflict bool, err error) {
	cmd := exec.Command("git", "-C", dir, "merge-tree", "--write-tree", "--merge-base="+base, onto, commit)
	out, runErr := cmd.Output()
	tree = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", true, nil
		}
		return "", false, fmt.Errorf("merge-tree: %w", runErr)
	}
	return tree, false, nil
}

// CommitTreeIdent creates a commit with distinct author and committer
// identities. Empty authorDate means now.
func CommitTreeIdent(dir, tree string, parents []string,
	authorName, authorEmail, authorDate, committerName, committerEmail, message string) (string, error) {
	args := []string{"-C", dir, "commit-tree", tree, "-m", message}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.Command("git", args...)
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName, "GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+committerName, "GIT_COMMITTER_EMAIL="+committerEmail,
	)
	if authorDate != "" {
		env = append(env, "GIT_AUTHOR_DATE="+authorDate)
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveTree returns the tree id of a commit.
func ResolveTree(dir, sha string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", sha+"^{tree}").Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s^{tree}: %w", sha, err)
	}
	return strings.TrimSpace(string(out)), nil
}
