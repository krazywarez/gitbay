package deps

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// EcoSwift is reported for advisories only. Scan has no Swift reader: a
// SwiftPM dependency is a git URL on any host, and finding its latest
// release would mean listing tags on hosts the repository names.
const EcoSwift = "swift"

// MaxResolved bounds the resolved set one repository sends for advisory
// matching. A lockfile lists the whole graph, so it is larger than MaxDeps.
const MaxResolved = 3000

// swiftResolvedDirs are where Xcode keeps Package.resolved inside a project
// or workspace bundle at the repository root.
var swiftResolvedDirs = map[string]string{
	".xcodeproj":   "/project.xcworkspace/xcshareddata/swiftpm/Package.resolved",
	".xcworkspace": "/xcshareddata/swiftpm/Package.resolved",
}

// Resolved returns every dependency whose exact version is known, for
// advisory matching: lockfiles where they exist, exact pins in manifests
// where they do not. A range is left out rather than reduced to its floor,
// since the floor is rarely what is installed and would match advisories
// the build does not have. root lists the names at the top of the tree,
// which is where Xcode bundles are looked for.
func Resolved(read ReadFile, root []string) []Dep {
	var out []Dep
	out = append(out, goRequires(read, true)...)
	out = append(out, npmResolved(read)...)
	out = append(out, cargoResolved(read)...)
	out = append(out, pythonResolved(read)...)
	out = append(out, swiftResolved(read, root)...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Current < out[j].Current
	})
	out = dedupe(out)
	if len(out) > MaxResolved {
		out = out[:MaxResolved]
	}
	return out
}

func dedupe(sorted []Dep) []Dep {
	var out []Dep
	for i, d := range sorted {
		if i > 0 && d == sorted[i-1] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// exact is pin restricted to a requirement that names one release: bare,
// or after `==` or `=`. `^1.2` is a range in npm and Cargo, and so is a bare
// `1.2` in Cargo, which is why Cargo callers check for `=` themselves.
func exact(spec string) string {
	s := strings.TrimSpace(spec)
	s = strings.TrimPrefix(s, "==")
	s = strings.TrimPrefix(s, "=")
	if s == "" || strings.ContainsAny(s[:1], "^~<>!") {
		return ""
	}
	return pin(s)
}

// npmResolved reads every install in package-lock.json, nested copies
// included, or the exact pins in package.json when there is no lockfile.
func npmResolved(read ReadFile) []Dep {
	if raw, err := read("package-lock.json"); err == nil {
		var lock struct {
			Packages map[string]struct {
				Version string `json:"version"`
				Link    bool   `json:"link"`
			} `json:"packages"`
		}
		if json.Unmarshal(raw, &lock) != nil {
			return nil
		}
		var out []Dep
		for path, entry := range lock.Packages {
			i := strings.LastIndex(path, "node_modules/")
			if i < 0 || entry.Link {
				continue
			}
			if v := pin(entry.Version); v != "" {
				out = append(out, Dep{Ecosystem: EcoNPM, Name: path[i+len("node_modules/"):], Current: v})
			}
		}
		return out
	}
	raw, err := read("package.json")
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(raw, &pkg) != nil {
		return nil
	}
	var out []Dep
	for _, set := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name, spec := range set {
			if v := exact(spec); v != "" {
				out = append(out, Dep{Ecosystem: EcoNPM, Name: name, Current: v})
			}
		}
	}
	return out
}

// cargoResolved reads the crates.io packages in Cargo.lock, or the `=`
// pins in Cargo.toml when there is no lockfile.
func cargoResolved(read ReadFile) []Dep {
	if raw, err := read("Cargo.lock"); err == nil {
		var lock struct {
			Package []struct {
				Name    string `toml:"name"`
				Version string `toml:"version"`
				Source  string `toml:"source"`
			} `toml:"package"`
		}
		if toml.Unmarshal(raw, &lock) != nil {
			return nil
		}
		var out []Dep
		for _, p := range lock.Package {
			// No source is a workspace member; a git source is not on
			// crates.io.
			if !strings.HasPrefix(p.Source, "registry+") && !strings.HasPrefix(p.Source, "sparse+") {
				continue
			}
			if v := pin(p.Version); v != "" {
				out = append(out, Dep{Ecosystem: EcoCargo, Name: p.Name, Current: v})
			}
		}
		return out
	}
	raw, err := read("Cargo.toml")
	if err != nil {
		return nil
	}
	var manifest struct {
		Dependencies      map[string]any `toml:"dependencies"`
		DevDependencies   map[string]any `toml:"dev-dependencies"`
		BuildDependencies map[string]any `toml:"build-dependencies"`
	}
	if toml.Unmarshal(raw, &manifest) != nil {
		return nil
	}
	var out []Dep
	for _, set := range []map[string]any{manifest.Dependencies, manifest.DevDependencies, manifest.BuildDependencies} {
		for name, spec := range set {
			req, ok := cargoRequirement(spec)
			if !ok || !strings.HasPrefix(strings.TrimSpace(req), "=") {
				continue
			}
			if v := exact(req); v != "" {
				out = append(out, Dep{Ecosystem: EcoCargo, Name: name, Current: v})
			}
		}
	}
	return out
}

// pythonResolved reads uv.lock or poetry.lock, or the `==` pins in
// requirements.txt and pyproject.toml when there is neither.
func pythonResolved(read ReadFile) []Dep {
	for _, lockfile := range []string{"uv.lock", "poetry.lock"} {
		raw, err := read(lockfile)
		if err != nil {
			continue
		}
		var lock struct {
			Package []struct {
				Name    string         `toml:"name"`
				Version string         `toml:"version"`
				Source  map[string]any `toml:"source"`
			} `toml:"package"`
		}
		if toml.Unmarshal(raw, &lock) != nil {
			return nil
		}
		var out []Dep
		for _, p := range lock.Package {
			if !fromPyPI(p.Source) {
				continue
			}
			if v := pin(p.Version); v != "" {
				out = append(out, Dep{Ecosystem: EcoPyPI, Name: normalizePyPI(p.Name), Current: v})
			}
		}
		return out
	}
	return pythonDeps(read, exact)
}

// fromPyPI reads a lockfile package's source. uv records
// `source = { registry = "..." }` for an index and `editable`, `virtual`,
// `path`, `directory` or `git` otherwise; poetry records no source for PyPI
// and a typed one otherwise.
func fromPyPI(source map[string]any) bool {
	if len(source) == 0 {
		return true
	}
	if _, ok := source["registry"]; ok {
		return true
	}
	typ, _ := source["type"].(string)
	return typ == "legacy"
}

// swiftResolved reads Package.resolved at the root and inside any Xcode
// project or workspace at the root. Version 1 files nest the pins under
// `object` and name the URL `repositoryURL`; versions 2 and 3 use `pins`
// and `location`. A pin to a branch or a revision has no version and is
// left out.
func swiftResolved(read ReadFile, root []string) []Dep {
	paths := []string{"Package.resolved"}
	for _, name := range root {
		for suffix, inside := range swiftResolvedDirs {
			if strings.HasSuffix(name, suffix) {
				paths = append(paths, name+inside)
			}
		}
	}
	sort.Strings(paths[1:])
	var out []Dep
	for _, path := range paths {
		raw, err := read(path)
		if err != nil {
			continue
		}
		type pinned struct {
			Location      string `json:"location"`
			RepositoryURL string `json:"repositoryURL"`
			State         struct {
				Version string `json:"version"`
			} `json:"state"`
		}
		var doc struct {
			Pins   []pinned `json:"pins"`
			Object struct {
				Pins []pinned `json:"pins"`
			} `json:"object"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for _, p := range append(doc.Pins, doc.Object.Pins...) {
			url := p.Location
			if url == "" {
				url = p.RepositoryURL
			}
			name := swiftURLName(url)
			if v := pin(p.State.Version); v != "" && name != "" {
				out = append(out, Dep{Ecosystem: EcoSwift, Name: name, Current: v})
			}
		}
	}
	return out
}

// swiftURLName reduces a package URL to the form OSV's SwiftURL ecosystem
// keys on: host and path, no scheme, user or .git suffix.
// `https://github.com/vapor/vapor.git` and `git@github.com:vapor/vapor`
// both become `github.com/vapor/vapor`. A local path has no name.
func swiftURLName(url string) string {
	u := strings.TrimSpace(url)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else if at := strings.Index(u, "@"); at >= 0 && strings.Contains(u[at:], ":") {
		u = strings.Replace(u[at+1:], ":", "/", 1)
	} else {
		return ""
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:]
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	if !strings.Contains(u, "/") || strings.HasPrefix(u, "/") {
		return ""
	}
	return u
}
