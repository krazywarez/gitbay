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
)

var jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

type Job struct {
	Name     string
	Steps    []string
	Schedule string // cron expression; scheduled jobs run on schedule, not on push
}

// Parse returns the jobs in name order, or an error describing the first
// problem so the pusher can fix the file.
func Parse(raw []byte) ([]Job, error) {
	var doc struct {
		Jobs map[string]struct {
			Steps    []string `yaml:"steps"`
			Schedule string   `yaml:"schedule"`
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
		jobs = append(jobs, Job{Name: name, Steps: j.Steps, Schedule: j.Schedule})
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Name < jobs[k].Name })
	return jobs, nil
}
