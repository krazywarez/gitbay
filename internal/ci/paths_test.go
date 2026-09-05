package ci

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{".gitbay/wiki/**", ".gitbay/wiki/Home.md", true},
		{".gitbay/wiki/**", ".gitbay/wiki/sub/Page.md", true}, // nested: the case a reader assumes works
		{".gitbay/wiki/**", ".gitbay/wiki", true},
		{".gitbay/wiki/**", "other/Home.md", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/sub/a.md", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.file); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

func TestSelected(t *testing.T) {
	cases := []struct {
		name  string
		job   Job
		files []string
		want  bool
	}{
		{"paths hit", Job{Paths: []string{"src/**"}}, []string{"src/x.go"}, true},
		{"paths miss", Job{Paths: []string{"src/**"}}, []string{"docs/x.md"}, false},
		{"paths-ignore covers every file", Job{PathsIgnore: []string{"docs/**"}},
			[]string{"docs/a.md", "docs/b.md"}, false},
		{"paths-ignore covers some files", Job{PathsIgnore: []string{"docs/**"}},
			[]string{"docs/a.md", "src/x.go"}, true},
		{"neither key", Job{}, []string{"anything.txt"}, true},
	}
	for _, c := range cases {
		if got := Selected(c.job, c.files); got != c.want {
			t.Errorf("%s: Selected() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHasDiffBase(t *testing.T) {
	cases := []struct {
		old  string
		want bool
	}{
		{"", false},
		{"0000000000000000000000000000000000000000", false},
		{"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", true},
	}
	for _, c := range cases {
		if got := HasDiffBase(c.old); got != c.want {
			t.Errorf("HasDiffBase(%q) = %v, want %v", c.old, got, c.want)
		}
	}
}

func TestParsePaths(t *testing.T) {
	raw := []byte("jobs:\n  unit:\n    steps:\n      - echo hi\n    paths:\n      - src/**\n    paths-ignore:\n      - docs/**\n")
	jobs, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(jobs) != 1 || len(jobs[0].Paths) != 1 || jobs[0].Paths[0] != "src/**" ||
		len(jobs[0].PathsIgnore) != 1 || jobs[0].PathsIgnore[0] != "docs/**" {
		t.Fatalf("Parse did not round-trip paths: %+v", jobs)
	}

	bad := []byte("jobs:\n  unit:\n    steps:\n      - echo hi\n    paths:\n      - \"[\"\n")
	if _, err := Parse(bad); err == nil {
		t.Fatal("Parse accepted a malformed path pattern")
	}
}
