package httpd

import (
	"encoding/json"
	"net/http"

	"gitbay.org/gitbay/internal/buildinfo"
)

// healthz answers from inside the process: whether the database answers,
// and which build is serving. No auth, no repository data; a monitor or
// an uptime check reads it. 503 when the database does not answer, so a
// checker that only reads the status code still learns the truth.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	dbErr := ""
	if err := s.st.DB.Ping(); err != nil {
		dbErr = err.Error()
	} else if _, err := s.st.Version(); err != nil {
		dbErr = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if dbErr != "" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"ok":     dbErr == "",
		"commit": buildinfo.String(),
		"db":     map[string]any{"ok": dbErr == "", "error": dbErr},
	})
}
