package store

import (
	"sync"
	"testing"
)

func settingsFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, repoID
}

func TestUpdateRepoSettingsRoundTrip(t *testing.T) {
	s, repoID := settingsFixture(t)
	got, err := s.UpdateRepoSettings(repoID, func(set *RepoSettings) { set.RequireApprovals = 2 })
	if err != nil {
		t.Fatal(err)
	}
	if got.RequireApprovals != 2 {
		t.Fatalf("returned %+v", got)
	}
	repo, err := s.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Settings.RequireApprovals != 2 {
		t.Fatalf("stored %+v", repo.Settings)
	}

	// Turning a flag back off must persist: the JSON tags are omitempty,
	// so "false" is an absent key, and a patch-style write would drop it.
	if _, err := s.UpdateRepoSettings(repoID, func(set *RepoSettings) { set.RequireChecks = true }); err != nil {
		t.Fatal(err)
	}
	got, err = s.UpdateRepoSettings(repoID, func(set *RepoSettings) { set.RequireChecks = false })
	if err != nil {
		t.Fatal(err)
	}
	if got.RequireChecks {
		t.Fatal("require_checks stayed on")
	}
	repo, _ = s.RepoByID(repoID)
	if repo.Settings.RequireChecks || repo.Settings.RequireApprovals != 2 {
		t.Fatalf("stored %+v", repo.Settings)
	}
}

func TestUpdateRepoSettingsMissingRepo(t *testing.T) {
	s, _ := settingsFixture(t)
	if _, err := s.UpdateRepoSettings(9999, func(*RepoSettings) {}); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Concurrent updates to different fields both survive. Read-modify-write
// through the caller lost one of them: each read the same blob and the
// later write put back what it had read for the other's field.
func TestUpdateRepoSettingsConcurrent(t *testing.T) {
	s, repoID := settingsFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.UpdateRepoSettings(repoID, func(set *RepoSettings) { set.RequireApprovals = 3 })
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := s.UpdateRepoSettings(repoID, func(set *RepoSettings) { set.RequireResolved = true })
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	repo, err := s.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Settings.RequireApprovals != 3 || !repo.Settings.RequireResolved {
		t.Fatalf("one update was lost: %+v", repo.Settings)
	}
}

// The protected-branch list is read and rewritten inside the update, so
// two admins protecting different branches at once both land.
func TestProtectedBranchesConcurrent(t *testing.T) {
	s, repoID := settingsFixture(t)
	var wg sync.WaitGroup
	for _, branch := range []string{"main", "release"} {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			s.UpdateRepoSettings(repoID, func(set *RepoSettings) {
				set.ProtectedBranches = append(set.ProtectedBranches, b)
			})
		}(branch)
	}
	wg.Wait()
	repo, _ := s.RepoByID(repoID)
	if len(repo.Settings.ProtectedBranches) != 2 {
		t.Fatalf("protected branches = %v, want both", repo.Settings.ProtectedBranches)
	}
}
