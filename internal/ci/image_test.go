package ci

import "strings"

import "testing"

// An image reference reaches `podman run` as an argument, so ci.Parse is
// where a repository's config file is stopped from turning it into
// something else (#144).
func TestParseImageValidation(t *testing.T) {
	good := []string{
		"alpine",
		"alpine:3.20",
		"docker.io/library/alpine:3.20",
		"ghcr.io/krz/builder:v1.2.3",
		"registry.example.test:5000/team/img:tag",
		"alpine@sha256:" + strings.Repeat("a", 64),
	}
	for _, img := range good {
		if _, err := Parse([]byte("jobs:\n  t:\n    image: " + img + "\n    steps:\n      - echo ok\n")); err != nil {
			t.Errorf("Parse rejected a valid image %q: %v", img, err)
		}
	}
	bad := []string{
		"alpine; rm -rf /",
		"alpine && curl evil.test",
		"alpine $(whoami)",
		"alpine `id`",
		"--privileged",
		"-v /:/host",
		"alpine --volume=/etc:/etc",
		"alpine\nrm -rf /",
		"alpine image with spaces",
		"'alpine'",
		"$IMAGE",
	}
	for _, img := range bad {
		_, err := Parse([]byte("jobs:\n  t:\n    image: " + quoteYAML(img) + "\n    steps:\n      - echo ok\n"))
		if err == nil {
			t.Errorf("Parse accepted %q as an image", img)
			continue
		}
		if !strings.Contains(err.Error(), "bad image") {
			t.Errorf("image %q refused for the wrong reason: %v", img, err)
		}
	}
}

// No image means the runner's default, not an error.
func TestParseImageOptional(t *testing.T) {
	jobs, err := Parse([]byte("jobs:\n  t:\n    steps:\n      - echo ok\n"))
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Image != "" {
		t.Errorf("Image = %q, want empty", jobs[0].Image)
	}
}

func quoteYAML(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
