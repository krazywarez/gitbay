package deps

import (
	"fmt"
	"os"
	"testing"
)

// tree serves manifests from a map, standing in for a git tree.
func tree(files map[string]string) ReadFile {
	return func(path string) ([]byte, error) {
		body, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(body), nil
	}
}

func found(t *testing.T, deps []Dep) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, d := range deps {
		m[d.Ecosystem+":"+d.Name] = d.Current
	}
	return m
}

func TestParseGoMod(t *testing.T) {
	deps := parseGoMod(tree(map[string]string{"go.mod": `
module gitbay.org/gitbay

go 1.27.0

require (
	github.com/BurntSushi/toml v1.6.0
	golang.org/x/crypto v0.55.0
	github.com/vendored/thing v0.1.0
)

require github.com/spf13/cobra v1.10.2

require (
	github.com/gorilla/css v1.0.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/vendored/thing => ./vendor/thing
`}))
	got := found(t, deps)
	want := map[string]string{
		"go:github.com/BurntSushi/toml": "v1.6.0",
		"go:golang.org/x/crypto":        "v0.55.0",
		"go:github.com/spf13/cobra":     "v1.10.2",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("go.mod deps = %v, want %v", got, want)
	}
}

func TestParseNPMPrefersLockfile(t *testing.T) {
	files := map[string]string{
		"package.json": `{
			"dependencies": {"react": "^18.0.0", "left-pad": "1.3.0", "local": "file:../local"},
			"devDependencies": {"@types/node": "^20.0.0", "typescript": "*"}
		}`,
		"package-lock.json": `{
			"packages": {
				"": {"version": "1.0.0"},
				"node_modules/react": {"version": "18.2.0"},
				"node_modules/@types/node": {"version": "20.11.5"},
				"node_modules/react/node_modules/scheduler": {"version": "0.23.0"}
			}
		}`,
	}
	got := found(t, parseNPM(tree(files)))
	want := map[string]string{
		"npm:react":       "18.2.0", // lockfile beats the ^18.0.0 floor
		"npm:@types/node": "20.11.5",
		"npm:left-pad":    "1.3.0",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("npm deps = %v, want %v", got, want)
	}

	// Without a lockfile the declared floor is what there is to compare.
	delete(files, "package-lock.json")
	if got := found(t, parseNPM(tree(files)))["npm:react"]; got != "18.0.0" {
		t.Errorf("react without lockfile = %q, want 18.0.0", got)
	}
}

func TestParseCargo(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": `
[dependencies]
serde = "1.0.100"
tokio = { version = "1.20", features = ["full"] }
helper = { path = "../helper" }
upstream = { git = "https://example.invalid/x" }

[dev-dependencies]
criterion = "0.5"
`,
		"Cargo.lock": `
[[package]]
name = "serde"
version = "1.0.197"

[[package]]
name = "tokio"
version = "1.36.0"
`,
	}
	got := found(t, parseCargo(tree(files)))
	want := map[string]string{
		"cargo:serde":     "1.0.197",
		"cargo:tokio":     "1.36.0",
		"cargo:criterion": "0.5", // not in the lock, so the requirement stands
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("cargo deps = %v, want %v", got, want)
	}
}

func TestParsePython(t *testing.T) {
	files := map[string]string{
		"requirements.txt": `
# comment
requests==2.31.0
django[bcrypt]==5.0.1 ; python_version >= "3.10"
flask>=2,<3
uvicorn
-r other.txt
`,
		"pyproject.toml": `
[project]
dependencies = ["httpx==0.27.0", "pydantic>=2"]

[tool.poetry.dependencies]
python = "^3.11"
rich = "^13.7.0"
`,
	}
	got := found(t, parsePython(tree(files)))
	want := map[string]string{
		"pypi:requests": "2.31.0",
		"pypi:django":   "5.0.1",
		"pypi:httpx":    "0.27.0",
		"pypi:pydantic": "2",
		"pypi:rich":     "13.7.0",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("python deps = %v, want %v", got, want)
	}
}

func TestScanSortsAndCaps(t *testing.T) {
	files := map[string]string{
		"go.mod":       "module x\n\nrequire github.com/a/b v1.0.0\n",
		"package.json": `{"dependencies": {"z": "1.0.0"}}`,
	}
	got := Scan(tree(files))
	if len(got) != 2 {
		t.Fatalf("Scan = %v, want 2 deps", got)
	}
	if got[0].Ecosystem != EcoGo || got[1].Ecosystem != EcoNPM {
		t.Errorf("Scan order = %s, %s", got[0].Ecosystem, got[1].Ecosystem)
	}
}

func TestScanEmptyTree(t *testing.T) {
	if got := Scan(tree(nil)); len(got) != 0 {
		t.Errorf("Scan of empty tree = %v", got)
	}
}
