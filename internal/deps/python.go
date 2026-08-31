package deps

import (
	"strings"

	"github.com/BurntSushi/toml"
)

// parsePython reads requirements.txt and pyproject.toml. Only requirements
// that name one release are reported: a bare `requests` or a `>=2,<3` range
// says the project accepts whatever is current, so there is nothing to tell
// its maintainer.
func parsePython(read ReadFile) []Dep {
	seen := map[string]bool{}
	var out []Dep
	add := func(name, version string) {
		name = normalizePyPI(name)
		if name == "" || name == "python" || seen[name] {
			return
		}
		if v := pin(version); v != "" {
			seen[name] = true
			out = append(out, Dep{Ecosystem: EcoPyPI, Name: name, Current: v})
		}
	}
	if raw, err := read("requirements.txt"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if i := strings.Index(line, "#"); i >= 0 {
				line = line[:i]
			}
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "-") {
				continue
			}
			name, version := splitPEP508(line)
			add(name, version)
		}
	}
	if raw, err := read("pyproject.toml"); err == nil {
		var doc struct {
			Project struct {
				Dependencies []string `toml:"dependencies"`
			} `toml:"project"`
			Tool struct {
				Poetry struct {
					Dependencies    map[string]any `toml:"dependencies"`
					DevDependencies map[string]any `toml:"dev-dependencies"`
				} `toml:"poetry"`
			} `toml:"tool"`
		}
		if toml.Unmarshal(raw, &doc) == nil {
			for _, spec := range doc.Project.Dependencies {
				name, version := splitPEP508(spec)
				add(name, version)
			}
			poetry := doc.Tool.Poetry
			for _, set := range []map[string]any{poetry.Dependencies, poetry.DevDependencies} {
				for name, spec := range set {
					// Poetry shares Cargo's two forms: a bare string, or a
					// table that may point somewhere other than PyPI.
					if req, ok := cargoRequirement(spec); ok {
						add(name, req)
					}
				}
			}
		}
	}
	return out
}

// splitPEP508 separates the distribution name from its version specifier,
// dropping extras and environment markers: `django[bcrypt]==5.0 ; sys_platform
// != "win32"` becomes ("django", "==5.0").
func splitPEP508(spec string) (name, version string) {
	if i := strings.Index(spec, ";"); i >= 0 {
		spec = spec[:i]
	}
	spec = strings.TrimSpace(spec)
	i := strings.IndexAny(spec, "[<>=!~ ")
	if i < 0 {
		return spec, ""
	}
	name, version = spec[:i], spec[i:]
	if j := strings.Index(version, "]"); j >= 0 {
		version = version[j+1:]
	}
	return name, strings.TrimSpace(version)
}

// normalizePyPI applies PEP 503 name normalization, so `Flask_SQLAlchemy`
// and `flask-sqlalchemy` are one dependency.
func normalizePyPI(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, ".", "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}
