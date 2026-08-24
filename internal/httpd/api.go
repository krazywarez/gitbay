package httpd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// apiRequest is the wire form of one command invocation. argv is real
// argv — no shell, no tokenizer, no quoting rules.
type apiRequest struct {
	Argv  []string `json:"argv"`
	Stdin string   `json:"stdin,omitempty"`
}

const maxAPIBody = 1 << 20

// apiCmd fronts the same control-command registry the SSH dispatcher uses:
// every command, current and future, is reachable here with identical
// semantics. Exit codes map onto HTTP statuses; the body is the command's
// JSON envelope with exit_code added.
func (s *Server) apiCmd(w http.ResponseWriter, r *http.Request) {
	user, scope, ok := s.apiAuth(w, r)
	if !ok {
		return
	}

	var req apiRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAPIBody)).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "body must be JSON: {\"argv\": [...], \"stdin\": \"...\"}")
		return
	}
	if len(req.Argv) == 0 {
		apiError(w, http.StatusBadRequest, "argv is required")
		return
	}
	switch req.Argv[0] {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
		apiError(w, http.StatusBadRequest, "git transport does not run over the JSON API; use git with an SSH remote")
		return
	}

	var stdout, stderr bytes.Buffer
	ctx := &control.Ctx{
		User:     user,
		Scope:    "full", // key scopes are an SSH concept; token scope is below
		Store:    s.st,
		Cfg:      s.cfg,
		Stdin:    strings.NewReader(req.Stdin),
		Stdout:   &stdout,
		Stderr:   &stderr,
		JSON:     true,
		ViaAPI:   true,
		ReadOnly: scope == "read",
	}
	code := control.Dispatch(ctx, req.Argv)

	status := map[int]int{
		protocol.ExitOK:       http.StatusOK,
		protocol.ExitUsage:    http.StatusBadRequest,
		protocol.ExitNotFound: http.StatusNotFound,
		protocol.ExitDenied:   http.StatusForbidden,
	}[code]
	if status == 0 {
		status = http.StatusInternalServerError
	}

	// Commands normally emit exactly one JSON envelope; inject exit_code.
	// A few (mr diff, help) write raw text instead — wrap those.
	var body map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &body); err != nil || body == nil {
		body = map[string]any{
			"protocol_version": protocol.Version,
			"output":           stdout.String(),
		}
	}
	body["exit_code"] = code
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		body["stderr"] = msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// apiAuth resolves the bearer token; failures are uniform 401s.
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) (store.User, string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="gitbay api"`)
		apiError(w, http.StatusUnauthorized, "missing bearer token; mint one over SSH: token create --name <n>")
		return store.User{}, "", false
	}
	user, scope, err := s.st.APITokenUser(store.HashToken(strings.TrimSpace(token)))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusUnauthorized, "invalid or expired token")
			return store.User{}, "", false
		}
		apiError(w, http.StatusInternalServerError, "internal error")
		return store.User{}, "", false
	}
	return user, scope, true
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"protocol_version": protocol.Version,
		"error":            msg,
	})
}
