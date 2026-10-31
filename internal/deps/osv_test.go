package deps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOSV answers querybatch from matches, keyed "ecosystem name version",
// and serves records by ID.
func fakeOSV(t *testing.T, matches map[string][]string, records map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/querybatch" {
			var req struct {
				Queries []osvQuery `json:"queries"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			var results []map[string]any
			for _, q := range req.Queries {
				var vulns []map[string]string
				for _, id := range matches[q.Package.Ecosystem+" "+q.Package.Name+" "+q.Version] {
					vulns = append(vulns, map[string]string{"id": id})
				}
				results = append(results, map[string]any{"vulns": vulns})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": results})
			return
		}
		body, ok := records[strings.TrimPrefix(r.URL.Path, "/v1/vulns/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAdvisories(t *testing.T) {
	srv := fakeOSV(t, map[string][]string{
		"PyPI flask 2.0.0":                      {"GHSA-aaaa", "PYSEC-1"},
		"Go golang.org/x/net v0.1.0":            {"GO-2", "GO-withdrawn"},
		"SwiftURL github.com/vapor/vapor 4.0.0": {"GHSA-vapor"},
	}, map[string]string{
		"GHSA-aaaa": `{"id":"GHSA-aaaa","summary":"Session  cookie\nleak","aliases":["PYSEC-1"],
			"database_specific":{"severity":"HIGH"},
			"affected":[{"package":{"ecosystem":"PyPI","name":"flask"},
				"ranges":[{"events":[{"introduced":"0"},{"fixed":"1.9.0"}]},{"events":[{"introduced":"2.0.0"},{"fixed":"2.3.2"}]},{"events":[{"introduced":"2.0.0"},{"fixed":"2.2.5"}]}]}]}`,
		"PYSEC-1":      `{"id":"PYSEC-1","summary":"duplicate","aliases":["GHSA-aaaa"]}`,
		"GO-2":         `{"id":"GO-2","details":"First line.\nSecond line.","affected":[{"package":{"ecosystem":"Go","name":"golang.org/x/net"},"ranges":[{"events":[{"fixed":"0.7.0"}]}]}]}`,
		"GO-withdrawn": `{"id":"GO-withdrawn","withdrawn":"2026-01-01T00:00:00Z"}`,
		"GHSA-vapor":   `{"id":"GHSA-vapor","summary":"no fix"}`,
	})
	c := NewClient("test")
	c.OSV = srv.URL
	got, err := c.Advisories(context.Background(), []Dep{
		{Ecosystem: EcoGo, Name: "golang.org/x/net", Current: "v0.1.0"},
		{Ecosystem: EcoPyPI, Name: "flask", Current: "2.0.0"},
		{Ecosystem: EcoPyPI, Name: "clean", Current: "1.0.0"},
		{Ecosystem: EcoSwift, Name: "github.com/vapor/vapor", Current: "4.0.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("advisories = %+v, want GO-2, GHSA-aaaa once, GHSA-vapor", got)
	}
	if a := got[0]; a.ID != "GO-2" || a.Summary != "First line." || a.Fixed != "0.7.0" || a.Severity != "" {
		t.Errorf("go advisory = %+v", a)
	}
	// The alias is folded in, the summary's whitespace collapsed, and the
	// nearest fix past 2.0.0 chosen over the branch fixes either side.
	if a := got[1]; a.ID != "GHSA-aaaa" || a.Summary != "Session cookie leak" || a.Severity != "high" || a.Fixed != "2.2.5" {
		t.Errorf("pypi advisory = %+v", a)
	}
	if a := got[2]; a.ID != "GHSA-vapor" || a.Fixed != "" {
		t.Errorf("swift advisory = %+v", a)
	}
}

func TestAdvisoriesFailWhenOSVIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("test")
	c.OSV = srv.URL
	if _, err := c.Advisories(context.Background(), []Dep{{Ecosystem: EcoGo, Name: "x", Current: "v1.0.0"}}); err == nil {
		t.Fatal("an unreachable OSV read as no advisories")
	}
}
