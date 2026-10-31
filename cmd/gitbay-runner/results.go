package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// resultsDir is where a code scan's steps write SARIF, relative to the
// workspace.
const resultsDir = ".gitbay-results"

// maxResults matches the server's upload limit (codescan.MaxUpload).
const maxResults = 8 << 20

// mergeResults reads every .sarif file in dir and joins their runs into
// one document, the shape runner results takes. Steps that wrote nothing
// is an error: a scan that produced no results did not run.
func mergeResults(dir string) ([]byte, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.sarif"))
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no .sarif files in %s", resultsDir)
	}
	sort.Strings(names)
	var runs []json.RawMessage
	total := 0
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		if total += len(raw); total > maxResults {
			return nil, fmt.Errorf("results are over %d bytes", maxResults)
		}
		var doc struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
		}
		if doc.Runs == nil {
			return nil, errors.New(filepath.Base(name) + ": no runs")
		}
		runs = append(runs, doc.Runs...)
	}
	return json.Marshal(map[string]any{"version": "2.1.0", "runs": runs})
}

// uploadResults sends the merged SARIF, retrying as reportDone does.
func (r *runner) uploadResults(id int64, dir string) error {
	doc, err := mergeResults(dir)
	if err != nil {
		return err
	}
	return reportWithRetry(func() (string, error) {
		return r.ssh(bytes.NewReader(doc), "runner", "results", fmt.Sprint(id))
	}, id, retryDelays)
}
