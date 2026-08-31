package deps

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// The body is read as a rendered table on the web and as the plain text of
// the notification mail, so every row has to line up on its own.
func TestBodyAlignsColumns(t *testing.T) {
	body := Body("main", []store.DepReport{
		{Ecosystem: EcoGo, Name: "go.yaml.in/yaml/v3", Current: "v3.0.4", Latest: "v3.0.5"},
		{Ecosystem: EcoGo, Name: "golang.org/x/net", Current: "v0.57.0", Latest: "v0.58.0"},
		{Ecosystem: EcoNPM, Name: "react", Current: "18.2.0", Latest: "19.0.0"},
	})
	var pipes []int
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "|") {
			pipes = append(pipes, -1) // section break
			continue
		}
		pipes = append(pipes, len(line))
	}
	// Within a run of table lines every line is the same length.
	var run []int
	for _, n := range append(pipes, -1) {
		if n == -1 {
			for _, w := range run {
				if w != run[0] {
					t.Fatalf("ragged table in:\n%s", body)
				}
			}
			run = nil
			continue
		}
		run = append(run, n)
	}
	if !strings.Contains(body, "| `golang.org/x/net`   | v0.57.0 | v0.58.0 |") {
		t.Errorf("row not padded to its column:\n%s", body)
	}
}
