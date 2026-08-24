package cliconfig

import "testing"

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		url  string
		host string
		port int
		user string
		repo string
		ok   bool
	}{
		{"ssh://git@gitbay.example/alice/proj.git", "gitbay.example", 0, "git", "alice/proj", true},
		{"ssh://git@gitbay.example:2222/alice/proj.git", "gitbay.example", 2222, "git", "alice/proj", true},
		{"ssh://gitbay.example/alice/proj", "gitbay.example", 0, "", "alice/proj", true},
		{"git@gitbay.example:alice/proj.git", "gitbay.example", 0, "git", "alice/proj", true},
		{"git@gitbay.example:alice/proj", "gitbay.example", 0, "git", "alice/proj", true},
		{"https://gitbay.example/alice/proj.git", "", 0, "", "", false},
		{"/local/path/repo.git", "", 0, "", "", false},
	}
	for _, tc := range cases {
		inst, repo, ok := ParseRemoteURL(tc.url)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.url, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if inst.Host != tc.host || inst.Port != tc.port || inst.User != tc.user || repo != tc.repo {
			t.Errorf("%s: got host=%s port=%d user=%s repo=%s", tc.url, inst.Host, inst.Port, inst.User, repo)
		}
	}
}

func TestCloneURL(t *testing.T) {
	i := Instance{Host: "gitbay.example"}
	if got := i.CloneURL("a/b"); got != "ssh://git@gitbay.example/a/b.git" {
		t.Errorf("CloneURL = %s", got)
	}
	i = Instance{Host: "gitbay.example", Port: 2222, User: "u"}
	if got := i.CloneURL("a/b"); got != "ssh://u@gitbay.example:2222/a/b.git" {
		t.Errorf("CloneURL = %s", got)
	}
}
