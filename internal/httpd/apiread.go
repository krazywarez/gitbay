package httpd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
)

// apiRead is the conditional-request half of the API: the same commands as
// /api/v1/cmd, reached with GET so a response can carry an ETag and a
// client can revalidate instead of refetching. A phone on a slow network
// re-renders a screen for a 304 rather than a full body.
//
// It dispatches the same registry — no second implementation, no chance of
// the two surfaces disagreeing — and admits only commands the registry
// marks ReadOnly, so a GET can never mutate.
//
//	GET /api/v1/read?argv=repo&argv=tree&argv=owner/name
func (s *Server) apiRead(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.apiAuth(w, r)
	if !ok {
		return
	}
	argv := r.URL.Query()["argv"]
	if len(argv) == 0 {
		apiError(w, http.StatusBadRequest, "argv is required: ?argv=repo&argv=show&argv=owner/name")
		return
	}
	cmd, _, found := control.Lookup(argv)
	if !found {
		apiError(w, http.StatusNotFound, "unknown command "+argv[0])
		return
	}
	if !cmd.ReadOnly {
		// Not 405: the command exists, it is simply not a read. Saying so
		// is more useful than implying the URL is wrong.
		apiError(w, http.StatusBadRequest,
			joinArgv(cmd.Path)+" changes state; POST it to /api/v1/cmd")
		return
	}
	if allowed, wait := s.apiLimit.allow(s.limitKey(r, user), false); !allowed {
		tooManyRequests(w, wait)
		return
	}

	var stdout, stderr bytes.Buffer
	ctx := &control.Ctx{
		User:     user,
		Source:   "api",
		Scope:    "full",
		Store:    s.st,
		Cfg:      s.cfg,
		Stdin:    strings.NewReader(""),
		Stdout:   &stdout,
		Stderr:   &stderr,
		JSON:     true,
		ViaAPI:   true,
		ReadOnly: true,
	}
	code := control.Dispatch(ctx, argv)

	var body map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &body); err != nil || body == nil {
		body = map[string]any{"protocol_version": protocol.Version, "output": stdout.String()}
	}
	body["exit_code"] = code
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		body["stderr"] = msg
	}
	payload, err := json.Marshal(body)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Responses are authorized per account, so the ETag is salted with the
	// caller: two users asking the same question may get different answers,
	// and neither should ever be served the other's.
	sum := sha256.Sum256(append([]byte(s.limitKey(r, user)+"\x00"), payload...))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	// private keeps this out of shared caches; no-cache requires a
	// revalidation rather than forbidding storage, which is what makes the
	// 304 worth having.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	status := statusForExit(code)
	w.WriteHeader(status)
	w.Write(payload)
}

// etagMatches handles the comma-separated If-None-Match list, and the weak
// prefix a cache may add.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func joinArgv(path []string) string { return strings.Join(path, " ") }
