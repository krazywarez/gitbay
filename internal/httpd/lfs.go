package httpd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/lfs"
	"gitbay.org/gitbay/internal/store"
)

// Git LFS server: the batch API plus basic-transfer endpoints. SSH clients
// arrive with a token minted by git-lfs-authenticate; anonymous HTTPS
// clients may download from public repositories, mirroring the smart-http
// read-only rule. Uploads always require an upload token.

const lfsMediaType = "application/vnd.git-lfs+json"

func (s *Server) lfsStore() lfs.BlobStore {
	root := s.cfg.LFS.Root
	if root == "" {
		root = filepath.Join(s.cfg.Server.Root, "lfs")
	}
	return lfs.LocalStore{Root: root}
}

func (s *Server) lfsMaxObject() int64 {
	if s.cfg.LFS.MaxObjectBytes > 0 {
		return s.cfg.LFS.MaxObjectBytes
	}
	return 512 << 20
}

func (s *Server) lfsSecret() ([]byte, error) {
	v, err := s.st.LFSSecret(lfs.NewSecret)
	return []byte(v), err
}

// lfsAuth resolves what the request may do to the repo: "upload",
// "download", or "" for no access. Tokens are repo-scoped; without one,
// public repos allow anonymous download only.
func (s *Server) lfsAuth(r *http.Request, repo store.Repo) string {
	auth := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
		secret, err := s.lfsSecret()
		if err != nil {
			return ""
		}
		repoID, op, ok := lfs.Verify(secret, tok, time.Now())
		if !ok || repoID != repo.ID {
			return ""
		}
		return op
	}
	if repo.Visibility == "public" {
		return "download"
	}
	return ""
}

func lfsError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

type lfsBatchReq struct {
	Operation string   `json:"operation"`
	Transfers []string `json:"transfers"`
	Objects   []struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"objects"`
}

type lfsAction struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

type lfsObject struct {
	OID           string               `json:"oid"`
	Size          int64                `json:"size"`
	Authenticated bool                 `json:"authenticated,omitempty"`
	Actions       map[string]lfsAction `json:"actions,omitempty"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// lfsBatch answers POST /{owner}/{repo}/info/lfs/objects/batch.
func (s *Server) lfsBatch(w http.ResponseWriter, r *http.Request) {
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if err != nil {
		lfsError(w, http.StatusNotFound, "repository not found")
		return
	}
	granted := s.lfsAuth(r, repo)
	if granted == "" {
		// Not naming whether the repo exists, per the enumeration rule.
		lfsError(w, http.StatusNotFound, "repository not found")
		return
	}
	var req lfsBatchReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		lfsError(w, http.StatusBadRequest, "bad batch request")
		return
	}
	if req.Operation != "download" && req.Operation != "upload" {
		lfsError(w, http.StatusBadRequest, "operation must be download or upload")
		return
	}
	if req.Operation == "upload" && granted != "upload" {
		lfsError(w, http.StatusForbidden, "upload requires write access (authenticate over SSH)")
		return
	}
	if len(req.Objects) > 1000 {
		lfsError(w, http.StatusUnprocessableEntity, "too many objects in one batch")
		return
	}

	// The token in transfer hrefs is operation-scoped and freshly minted,
	// so anonymous downloads work without the client sending one back.
	secret, err := s.lfsSecret()
	if err != nil {
		lfsError(w, http.StatusInternalServerError, "lfs secret unavailable")
		return
	}
	transferToken := lfs.Sign(secret, repo.ID, req.Operation, time.Now())
	base := fmt.Sprintf("%s/%s/%s.git/info/lfs/objects",
		strings.TrimSuffix(s.cfg.Server.SiteURL, "/"), repo.OwnerName, repo.Name)
	authHeader := map[string]string{"Authorization": "Bearer " + transferToken}

	blobs := s.lfsStore()
	out := struct {
		Transfer string      `json:"transfer"`
		Objects  []lfsObject `json:"objects"`
	}{Transfer: "basic"}
	for _, o := range req.Objects {
		obj := lfsObject{OID: o.OID, Size: o.Size, Authenticated: true}
		switch {
		case !lfs.OIDPat.MatchString(o.OID) || o.Size < 0:
			obj.Error = &struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}{422, "malformed object"}
		case req.Operation == "download":
			if size, ok := blobs.Exists(o.OID); ok {
				obj.Size = size
				obj.Actions = map[string]lfsAction{"download": {
					Href: base + "/" + o.OID, Header: authHeader, ExpiresIn: int(lfs.TokenTTL.Seconds()),
				}}
			} else {
				obj.Error = &struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}{404, "object not found"}
			}
		default: // upload
			if o.Size > s.lfsMaxObject() {
				obj.Error = &struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}{422, fmt.Sprintf("object exceeds the %d byte limit", s.lfsMaxObject())}
			} else if _, ok := blobs.Exists(o.OID); !ok {
				// Present objects get no actions: the client skips them.
				obj.Actions = map[string]lfsAction{"upload": {
					Href: base + "/" + o.OID, Header: authHeader, ExpiresIn: int(lfs.TokenTTL.Seconds()),
				}}
			}
		}
		out.Objects = append(out.Objects, obj)
	}
	w.Header().Set("Content-Type", lfsMediaType)
	json.NewEncoder(w).Encode(out)
}

// lfsDownload answers GET /{owner}/{repo}/info/lfs/objects/{oid}.
func (s *Server) lfsDownload(w http.ResponseWriter, r *http.Request) {
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if err != nil || s.lfsAuth(r, repo) == "" {
		lfsError(w, http.StatusNotFound, "not found")
		return
	}
	rc, size, err := s.lfsStore().Get(r.PathValue("oid"))
	if err != nil {
		lfsError(w, http.StatusNotFound, "object not found")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.Copy(w, rc)
}

// lfsUpload answers PUT /{owner}/{repo}/info/lfs/objects/{oid}.
func (s *Server) lfsUpload(w http.ResponseWriter, r *http.Request) {
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if err != nil || s.lfsAuth(r, repo) != "upload" {
		lfsError(w, http.StatusNotFound, "not found")
		return
	}
	oid := r.PathValue("oid")
	if r.ContentLength < 0 || r.ContentLength > s.lfsMaxObject() {
		lfsError(w, http.StatusRequestEntityTooLarge, "object too large or length unknown")
		return
	}
	if _, ok := s.lfsStore().Exists(oid); ok {
		w.WriteHeader(http.StatusOK) // already have it; idempotent
		return
	}
	if err := s.lfsStore().Put(oid, r.Body, r.ContentLength); err != nil {
		lfsError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}
