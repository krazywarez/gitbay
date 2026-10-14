package control

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestReleaseListPagesAndHidesTagTitle(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for _, tag := range []string{"v1", "v2", "v3"} {
		title := tag
		if tag == "v2" {
			title = "Second"
		}
		if _, err := st.CreateRelease(repo.ID, tag, title, "", uid, "md"); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--limit", "2"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(c.Stdout.(*bytes.Buffer).String()), "\n")
	if len(lines) != 3 || lines[0] != "v3\t\t0 asset(s)" || lines[1] != "v2\tSecond\t0 asset(s)" || !strings.HasPrefix(lines[2], "next\t") {
		t.Fatalf("page 1:\n%s", strings.Join(lines, "\n"))
	}
	cursor := strings.TrimPrefix(lines[2], "next\t")

	c, errOut = pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--cursor", cursor}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := c.Stdout.(*bytes.Buffer).String(); got != "v1\t\t0 asset(s)\n" {
		t.Fatalf("page 2: %q", got)
	}
}

// The cursor names the pointed-to release by (created_at, id), not by a
// live lookup of that id: deleting it between page requests must not
// blank the next page.
func TestReleaseListPageSurvivesTheCursorReleaseBeingDeleted(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for _, tag := range []string{"v1", "v2", "v3"} {
		if _, err := st.CreateRelease(repo.ID, tag, tag, "", uid, "md"); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--limit", "2"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(c.Stdout.(*bytes.Buffer).String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "next\t") {
		t.Fatalf("page 1:\n%s", strings.Join(lines, "\n"))
	}
	cursor := strings.TrimPrefix(lines[2], "next\t")

	rels, err := st.ListReleases(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	var v2ID int64
	for _, r := range rels {
		if r.Tag == "v2" {
			v2ID = r.ID
		}
	}
	if v2ID == 0 {
		t.Fatal("v2 not found")
	}
	if err := st.DeleteRelease(v2ID); err != nil {
		t.Fatal(err)
	}

	c, errOut = pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--cursor", cursor}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := c.Stdout.(*bytes.Buffer).String(); got != "v1\t\t0 asset(s)\n" {
		t.Fatalf("page 2 after deleting the cursor release: %q", got)
	}
}

// A merge records the commit it landed as: the head for a fast-forward,
// the new commit for a squash (#331).
func TestMergeRecordsTheLandedCommit(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.verified(f.alice)
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--strategy", "squash")
	tip := strings.TrimSpace(f.git(f.dir, "rev-parse", "main"))
	if mr := f.mr(); mr.MergedSHA != tip || tip == f.headSHA {
		t.Fatalf("merged_sha %q, main %q, head %q", mr.MergedSHA, tip, f.headSHA)
	}
}

// land opens a merge request from a new branch holding one commit on top
// of the repository's main and merges it with strategy, returning its
// number.
func (f *queueFixture) land(branch, title, strategy string) int64 {
	f.t.Helper()
	f.git(f.src, "fetch", "-q", f.dir, "main:refs/remotes/bare/main")
	f.git(f.src, "checkout", "-q", "-b", branch, "bare/main")
	f.write(branch+".txt", title+"\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", title)
	sha := strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.dir, "fetch", "-q", f.src, "refs/heads/"+branch+":refs/heads/"+branch)
	id, err := f.st.CreateMR(f.repo.ID, f.alice.ID, f.repo.ID, branch, "main", title, "", sha, "md", false)
	if err != nil {
		f.t.Fatal(err)
	}
	mr, err := f.st.MRByID(id)
	if err != nil {
		f.t.Fatal(err)
	}
	f.git(f.dir, "update-ref", mrHeadRef(mr.Number), sha)
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), strconv.FormatInt(mr.Number, 10), "--strategy", strategy)
	return mr.Number
}

// release notes lists the merge requests landed between the previous
// release's tag and a ref, oldest first, one line each; --since names
// the range's start instead (#331).
func TestReleaseNotesListsMergedMRsSinceThePreviousRelease(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.verified(f.alice)
	f.git(f.dir, "tag", "v0", f.targetSH)
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--strategy", "ff")

	// No release yet: the whole history of the ref.
	if out := f.mustRun(f.alice, "release", "notes", f.repo.Path(), "main"); out != "- t (!1) by @alice\n" {
		t.Fatalf("no previous release:\n%s", out)
	}

	f.git(f.dir, "tag", "v1", "main")
	f.mustRun(f.alice, "release", "create", f.repo.Path(), "v1")
	f.land("two", "two", "squash")
	f.land("three", "three", "ff")
	// An open merge request and one into another branch are not changes.
	f.branch("dev", f.targetSH)
	f.git(f.src, "checkout", "-q", "-b", "side", f.targetSH)
	f.write("side.txt", "s\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "side")
	side := strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.dir, "fetch", "-q", f.src, "refs/heads/side:refs/heads/side")
	if _, err := f.st.CreateMR(f.repo.ID, f.alice.ID, f.repo.ID, "side", "dev", "side", "", side, "md", false); err != nil {
		t.Fatal(err)
	}
	f.git(f.dir, "update-ref", mrHeadRef(4), side)
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "4", "--strategy", "ff")

	if out := f.mustRun(f.alice, "release", "notes", f.repo.Path(), "main"); out != "- two (!2) by @alice\n- three (!3) by @alice\n" {
		t.Fatalf("since v1:\n%s", out)
	}
	// --since overrides the previous release; a row merged before 0079
	// has no merged_sha and is matched on its head.
	if _, err := f.st.DB.Exec("UPDATE merge_requests SET merged_sha = '' WHERE number = 1"); err != nil {
		t.Fatal(err)
	}
	if out := f.mustRun(f.alice, "release", "notes", f.repo.Path(), "main", "--since", "v0"); out != "- t (!1) by @alice\n- two (!2) by @alice\n- three (!3) by @alice\n" {
		t.Fatalf("--since v0:\n%s", out)
	}
	// Regenerating for a tag that already has a release starts at the one before it.
	f.git(f.dir, "tag", "v2", "main")
	f.mustRun(f.alice, "release", "create", f.repo.Path(), "v2")
	if out := f.mustRun(f.alice, "release", "notes", f.repo.Path(), "v2"); out != "- two (!2) by @alice\n- three (!3) by @alice\n" {
		t.Fatalf("v2 with its own release:\n%s", out)
	}

	out := f.mustRun(f.alice, "release", "notes", f.repo.Path(), "v2", "--json")
	var env struct {
		Data struct {
			Since string `json:"since"`
			Items []struct {
				Number   int64  `json:"number"`
				Title    string `json:"title"`
				Author   string `json:"author"`
				MergedAt string `json:"merged_at"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	d := env.Data
	if d.Since != "v1" || len(d.Items) != 2 || d.Items[0].Number != 2 || d.Items[1].Title != "three" ||
		d.Items[0].Author != "alice" || d.Items[0].MergedAt == "" {
		t.Fatalf("json: %s", out)
	}

	// An unknown ref or --since is not found.
	if code, _, _ := f.run(f.alice, "release", "notes", f.repo.Path(), "nope"); code != protocol.ExitNotFound {
		t.Fatalf("unknown ref: exit %d", code)
	}
	if code, _, _ := f.run(f.alice, "release", "notes", f.repo.Path(), "main", "--since", "nope"); code != protocol.ExitNotFound {
		t.Fatalf("unknown --since: exit %d", code)
	}
}
