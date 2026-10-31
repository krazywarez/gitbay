package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeResults(t *testing.T) {
	dir := t.TempDir()
	if _, err := mergeResults(dir); err == nil {
		t.Error("an empty results directory merged")
	}
	os.WriteFile(filepath.Join(dir, "a.sarif"), []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"a"}}}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.sarif"), []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"b"}}}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`ignored`), 0o644)
	raw, err := mergeResults(dir)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runs []struct {
			Tool struct {
				Driver struct{ Name string } `json:"driver"`
			} `json:"tool"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Runs) != 2 || doc.Runs[1].Tool.Driver.Name != "b" {
		t.Fatalf("merged %s: %v", raw, err)
	}
	os.WriteFile(filepath.Join(dir, "c.sarif"), []byte(`not json`), 0o644)
	if _, err := mergeResults(dir); err == nil {
		t.Error("a broken file merged")
	}
}
