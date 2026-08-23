package policy

import "testing"

func TestValidateOwnerName(t *testing.T) {
	valid := []string{"alice", "krz", "a", "user-1", "a.b_c", "0day"}
	for _, n := range valid {
		if err := ValidateOwnerName(n); err != nil {
			t.Errorf("ValidateOwnerName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []string{
		"",
		"Alice",     // uppercase
		"-lead",     // bad first char
		".hidden",   // bad first char
		"a b",       // space
		"repo.git",  // .git suffix
		"..",        //
		"login",     // reserved
		"admin",     // reserved
		"static",    // reserved
		"api",       // reserved
		"register",  // reserved
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
}
