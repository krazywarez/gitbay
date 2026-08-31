package deps

import (
	"encoding/json"
	"strings"
)

// parseNPM takes the direct dependency names from package.json and their
// installed versions from package-lock.json where one exists. The lockfile
// is the honest source: package.json records ranges, and a range that
// already admits the newest release is not something to report.
func parseNPM(read ReadFile) []Dep {
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
	locked := npmLock(read)
	var out []Dep
	for _, set := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name, spec := range set {
			current := locked[name]
			if current == "" {
				current = pin(spec)
			}
			if current != "" {
				out = append(out, Dep{Ecosystem: EcoNPM, Name: name, Current: current})
			}
		}
	}
	return out
}

// npmLock reads installed versions out of a v2 or v3 lockfile, keyed by
// package name. Nested entries (a transitive copy under another package's
// node_modules) are ignored: only the top-level install is the direct one.
func npmLock(read ReadFile) map[string]string {
	raw, err := read("package-lock.json")
	if err != nil {
		return nil
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if json.Unmarshal(raw, &lock) != nil {
		return nil
	}
	out := map[string]string{}
	for path, entry := range lock.Packages {
		name, ok := strings.CutPrefix(path, "node_modules/")
		if !ok || strings.Contains(name, "node_modules/") {
			continue
		}
		if v := pin(entry.Version); v != "" {
			out[name] = v
		}
	}
	return out
}
