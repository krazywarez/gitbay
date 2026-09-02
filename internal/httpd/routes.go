package httpd

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Route is one entry in the explicit route table. The view-only guarantee is
// structural: Handler() consults web.mode when building the table, and the
// route test asserts no mutating route exists in view_only mode.
type Route struct {
	Method  string
	Pattern string // without method prefix
	// Mutating marks routes that can change server state. The git transport
	// POSTs are not mutating: upload-pack is a pure read, and the
	// receive-pack endpoint is a static refusal that writes nothing.
	Mutating bool
	Handler  http.HandlerFunc
}

// Routes returns the route table for the configured web.mode.
func (s *Server) Routes() []Route {
	// Git smart transport (anonymous, public repos only).
	routes := []Route{
		{Method: "GET", Pattern: "/{owner}/{repo}/info/refs", Handler: s.infoRefs},
		{Method: "POST", Pattern: "/{owner}/{repo}/git-upload-pack", Handler: s.uploadPack},
		{Method: "POST", Pattern: "/{owner}/{repo}/git-receive-pack", Handler: s.receivePackRefusal},
		// Git LFS: token-authenticated transport (minted over SSH), not
		// web-session routes — anonymous download for public repos only.
		{Method: "POST", Pattern: "/{owner}/{repo}/info/lfs/objects/batch", Handler: s.lfsBatch},
		{Method: "GET", Pattern: "/{owner}/{repo}/info/lfs/objects/{oid}", Handler: s.lfsDownload},
		{Method: "PUT", Pattern: "/{owner}/{repo}/info/lfs/objects/{oid}", Handler: s.lfsUpload},
	}

	// Web UI, read-only. These exist in every mode.
	routes = append(routes,
		Route{Method: "GET", Pattern: "/{$}", Handler: s.index},
		Route{Method: "GET", Pattern: "/explore", Handler: s.explore},
		Route{Method: "GET", Pattern: "/privacy", Handler: s.privacy},
		Route{Method: "GET", Pattern: "/static/style.css", Handler: s.stylesheet},
		// Literal per-file routes: a {name} wildcard is ambiguous against
		// /{owner}/{repo}/... patterns in ServeMux precedence.
		Route{Method: "GET", Pattern: "/static/fonts/plex-sans.woff2", Handler: s.font},
		Route{Method: "GET", Pattern: "/static/fonts/plex-mono-400.woff2", Handler: s.font},
		Route{Method: "GET", Pattern: "/static/fonts/plex-mono-500.woff2", Handler: s.font},
		Route{Method: "GET", Pattern: "/favicon.svg", Handler: s.favicon},
		Route{Method: "GET", Pattern: "/{owner}", Handler: s.ownerPage},
		Route{Method: "GET", Pattern: "/{owner}/{repo}", Handler: s.repoHome},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/tree/{ref}/{path...}", Handler: s.tree},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/blob/{ref}/{path...}", Handler: s.blob},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/blame/{ref}/{path...}", Handler: s.blame},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/search", Handler: s.search},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/milestones", Handler: s.milestones},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/wiki", Handler: s.wiki},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/wiki/_raw/{path...}", Handler: s.wikiRaw},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/wiki/{page}", Handler: s.wiki},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/releases", Handler: s.releases},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/builds", Handler: s.builds},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/badge/build.svg", Handler: s.buildBadge},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/badge/build.png", Handler: s.buildBadgePNG},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/builds/{n}", Handler: s.build},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/releases/download/{tag}/{name}", Handler: s.releaseAsset},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/raw/{ref}/{path...}", Handler: s.raw},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/log", Handler: s.log},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/log/{ref}", Handler: s.log},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/commit/{sha}", Handler: s.commit},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/refs", Handler: s.refs},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/archive/{file}", Handler: s.archive},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/issues", Handler: s.issues},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/issues/{n}", Handler: s.issue},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/mrs", Handler: s.mrs},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/mrs/{n}", Handler: s.mr},
	)

	// The JSON API is its own opt-in surface, independent of web.mode.
	if s.cfg.API.Enabled {
		routes = append(routes,
			Route{Method: "POST", Pattern: "/api/v1/cmd", Mutating: true, Handler: s.apiCmd},
			Route{Method: "GET", Pattern: "/api/v1/read", Handler: s.apiRead},
		)
	}

	// Account-mode routes exist only when web.mode = "accounts". In
	// view_only they are never registered — the structural guarantee.
	if s.cfg.Web.Mode == "accounts" {
		routes = append(routes,
			Route{Method: "GET", Pattern: "/login", Handler: s.login, Mutating: true}, // consumes a one-time token
			Route{Method: "POST", Pattern: "/logout", Mutating: true,
				Handler: s.checkOrigin(s.logout)},
			Route{Method: "GET", Pattern: "/new", Handler: s.requireUser(s.newRepoForm)},
			Route{Method: "GET", Pattern: "/settings", Handler: s.requireUser(s.accountForm)},
			Route{Method: "GET", Pattern: "/admin", Handler: s.requireUser(s.adminPage)},
			Route{Method: "POST", Pattern: "/{owner}", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.orgSubmit))},
			Route{Method: "POST", Pattern: "/settings", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.accountSubmit))},
		)
		// Web signup fronts the same registration path as SSH register,
		// so it exists only when registration is open or invite.
		if s.cfg.Registration.Mode != "closed" {
			routes = append(routes,
				Route{Method: "GET", Pattern: "/register", Handler: s.signupForm},
				Route{Method: "POST", Pattern: "/register", Mutating: true,
					Handler: s.checkOrigin(s.signupSubmit)},
			)
		}
		routes = append(routes,
			Route{Method: "POST", Pattern: "/new", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.newRepoSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/pin", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.pinToggle))},
			Route{Method: "GET", Pattern: "/{owner}/{repo}/issues/new",
				Handler: s.requireUser(s.issueCreateForm)},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/new", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueCreateSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/comment", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueCommentSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/edit", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueEditSubmit))},
			// Triage: each runs the matching issue command.
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/state", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueStateSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/label", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueLabelSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/assign", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueAssignSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/milestone", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueMilestoneSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/releases", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.releaseSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/builds", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.buildTriggerSubmit))},
			Route{Method: "GET", Pattern: "/{owner}/{repo}/settings",
				Handler: s.requireUser(s.settingsForm)},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/settings", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.settingsSubmit))},
			Route{Method: "GET", Pattern: "/{owner}/{repo}/mrs/new",
				Handler: s.requireUser(s.mrCreateForm)},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/new", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrCreateSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/edit", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrEditSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/comment", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrCommentSubmit))},
			// Review loop: each runs the matching mr command.
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/review", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrReviewSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/merge", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrMergeSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/close", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrCloseSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/retarget", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrRetargetSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/thread", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrThreadSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/diff-comment", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrDiffCommentSubmit))},
			Route{Method: "GET", Pattern: "/{owner}/{repo}/edit/{ref}/{path...}",
				Handler: s.requireUser(s.editForm)},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/edit/{ref}/{path...}", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.editSubmit))},
		)
	}
	return routes
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.Routes() {
		mux.HandleFunc(r.Method+" "+r.Pattern, r.Handler)
	}
	var h http.Handler = mux
	if len(s.cfg.GoImport) > 0 {
		h = s.goImportHandler(mux)
	}
	// Always wrapped: custom pages domains work with or without the
	// built-in [pages] domain.
	return s.pagesRouter(s.securityHeaders(h))
}

// securityHeaders sets defensive response headers on every reply. The CSP
// is strict where it can be: no scripts at all (the UI needs none), no
// plugins, no embedding. Inline styles are allowed because label chips
// carry their color inline (chroma is class-based so the syntax palette
// can follow the color scheme). Images may load from anywhere so external README images
// still render; they are the one thing a reader-facing forge can't police
// without a proxy.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'none'; style-src 'self' 'unsafe-inline'; " +
		"img-src * data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", csp)
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.cfg.HTTP.TLS != "off" {
			hd.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) goImportHandler(mux http.Handler) http.Handler {
	// Vanity Go modules: ?go-get=1 requests under a configured module
	// path answer with the go-import meta tag before normal routing.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("go-get") == "1" {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			requested := host + strings.TrimSuffix(r.URL.Path, "/")
			for module, repo := range s.cfg.GoImport {
				if requested == module || strings.HasPrefix(requested, module+"/") {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta name="go-import" content="%s git %s/%s.git"></head><body>%s</body></html>`,
						module, s.cfg.Server.SiteURL, repo, module)
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
