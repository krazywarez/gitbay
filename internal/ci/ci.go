// Package ci parses .gitbay/ci.yml, the per-repo build configuration:
//
//	jobs:
//	  test:
//	    steps:
//	      - go test ./...
//
// Each job becomes one build per push; each step is a shell command the
// runner executes with `sh -c`, stopping at the first failure.
package ci

import (
	"fmt"
	"path"
	"regexp"
	"sort"

	yaml "go.yaml.in/yaml/v3"
)

// ConfigPath is where the build configuration lives in a repository.
const ConfigPath = ".gitbay/ci.yml"

const (
	maxJobs     = 10
	maxSteps    = 50
	maxStepSize = 4096
	maxPaths    = 50
)

var jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// imageRef matches an OCI image reference conservatively: registry path
// segments, an optional :tag and an optional @sha256: digest. This string
// becomes an argument to `podman run`, and a repository's config file must
// not be able to turn it into anything else — so the pattern allows only
// what a reference needs and refuses whitespace and every shell character
// rather than trying to escape them (#144).
var imageRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-]*(:[0-9]+)?(/[a-zA-Z0-9][a-zA-Z0-9._\-]*)*(:[a-zA-Z0-9][a-zA-Z0-9._\-]{0,127})?(@sha256:[a-f0-9]{64})?$`)

type Job struct {
	Name     string
	Steps    []string
	Schedule string // cron expression; scheduled jobs run on schedule, not on push
	Tags     string // tag glob (e.g. "v*"); tag jobs run on matching tag pushes only
	// Paths and PathsIgnore only gate a job queued on push; a scheduled
	// or tag job ignores them.
	Paths       []string // globs; the job runs only when a changed file matches one
	PathsIgnore []string // globs; the job is skipped when every changed file matches one
	// Image is the container image the job's steps run in. Empty means
	// the runner's configured default (#144).
	Image string
}

// Parse returns the jobs in name order, or an error describing the first
// problem so the pusher can fix the file.
func Parse(raw []byte) ([]Job, error) {
	var doc struct {
		Jobs map[string]struct {
			Steps       []string `yaml:"steps"`
			Schedule    string   `yaml:"schedule"`
			Tags        string   `yaml:"tags"`
			Paths       []string `yaml:"paths"`
			PathsIgnore []string `yaml:"paths-ignore"`
			Image       string   `yaml:"image"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ConfigPath, err)
	}
	if len(doc.Jobs) == 0 {
		return nil, fmt.Errorf("%s defines no jobs", ConfigPath)
	}
	if len(doc.Jobs) > maxJobs {
		return nil, fmt.Errorf("%s defines %d jobs; max %d", ConfigPath, len(doc.Jobs), maxJobs)
	}
	var jobs []Job
	for name, j := range doc.Jobs {
		if !jobName.MatchString(name) {
			return nil, fmt.Errorf("bad job name %q: lowercase letters, digits, - and _; max 40 chars", name)
		}
		if len(j.Steps) == 0 {
			return nil, fmt.Errorf("job %q has no steps", name)
		}
		if len(j.Steps) > maxSteps {
			return nil, fmt.Errorf("job %q has %d steps; max %d", name, len(j.Steps), maxSteps)
		}
		for _, s := range j.Steps {
			if len(s) > maxStepSize {
				return nil, fmt.Errorf("job %q has a step over %d bytes", name, maxStepSize)
			}
		}
		if j.Schedule != "" {
			if _, err := ParseCron(j.Schedule); err != nil {
				return nil, fmt.Errorf("job %q: %v", name, err)
			}
		}
		if j.Tags != "" {
			if _, err := path.Match(j.Tags, "x"); err != nil {
				return nil, fmt.Errorf("job %q: bad tag pattern %q", name, j.Tags)
			}
			if j.Schedule != "" {
				return nil, fmt.Errorf("job %q: schedule and tags are mutually exclusive", name)
			}
		}
		if len(j.Paths) > maxPaths {
			return nil, fmt.Errorf("job %q has %d path patterns; max %d", name, len(j.Paths), maxPaths)
		}
		for _, p := range j.Paths {
			if _, err := path.Match(p, "x"); err != nil {
				return nil, fmt.Errorf("job %q: bad path pattern %q", name, p)
			}
		}
		if len(j.PathsIgnore) > maxPaths {
			return nil, fmt.Errorf("job %q has %d paths-ignore patterns; max %d", name, len(j.PathsIgnore), maxPaths)
		}
		for _, p := range j.PathsIgnore {
			if _, err := path.Match(p, "x"); err != nil {
				return nil, fmt.Errorf("job %q: bad paths-ignore pattern %q", name, p)
			}
		}
		if j.Image != "" && !imageRef.MatchString(j.Image) {
			return nil, fmt.Errorf("job %q: bad image %q: a reference like "+
				"docker.io/library/alpine:3.20, not a command line", name, j.Image)
		}
		jobs = append(jobs, Job{
			Name: name, Steps: j.Steps, Schedule: j.Schedule, Tags: j.Tags,
			Paths: j.Paths, PathsIgnore: j.PathsIgnore, Image: j.Image,
		})
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Name < jobs[k].Name })
	return jobs, nil
}
