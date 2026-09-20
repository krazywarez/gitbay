package policy

import (
	"strings"
	"testing"
)

func TestValidateOwnerName(t *testing.T) {
	valid := []string{"alice", "krz", "a", "user-1", "a.b_c", "0day"}
	for _, n := range valid {
		if err := ValidateOwnerName(n); err != nil {
			t.Errorf("ValidateOwnerName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []string{
		"",
		"Alice",    // uppercase
		"-lead",    // bad first char
		".hidden",  // bad first char
		"a b",      // space
		"repo.git", // .git suffix
		"..",       //
		"login",    // reserved
		"admin",    // reserved
		"static",   // reserved
		"api",      // reserved
		"register", // reserved
	}
	for _, n := range invalid {
		if err := ValidateOwnerName(n); err == nil {
			t.Errorf("ValidateOwnerName(%q) = nil, want error", n)
		}
	}
}

func TestRepoNameAllowsReservedWords(t *testing.T) {
	// Repo routes are namespaced under the owner, so reserved words are fine.
	if err := ValidateName("api"); err != nil {
		t.Errorf("ValidateName(\"api\") = %v, want nil", err)
	}
	if err := ValidateName("repo.git"); err == nil {
		t.Error("ValidateName(\"repo.git\") = nil, want error")
	}
	if err := ValidateName("activity.atom"); err == nil {
		t.Error("ValidateName(\"activity.atom\") = nil, want error")
	}
}

func TestRepoNameAllowsLeadingDot(t *testing.T) {
	// .gitbay holds an owner's profile content; a leading dot marks a
	// repository as infrastructure rather than a project.
	for _, n := range []string{".gitbay", ".dotfiles", ".a"} {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range []string{".", "..", ".git", "repo.git", "..a", ".-a"} {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
	// The ceiling is 63 characters, the dot included.
	if err := ValidateName("." + strings.Repeat("a", 62)); err != nil {
		t.Errorf("63-character dotted name rejected: %v", err)
	}
	if err := ValidateName("." + strings.Repeat("a", 63)); err == nil {
		t.Error("64-character dotted name accepted")
	}
}

func TestOwnerNameRefusesLeadingDot(t *testing.T) {
	// The dot is a repository affordance. An owner is a top-level route.
	for _, n := range []string{".gitbay", ".hidden", ".a"} {
		if err := ValidateOwnerName(n); err == nil {
			t.Errorf("ValidateOwnerName(%q) = nil, want error", n)
		}
	}
}
