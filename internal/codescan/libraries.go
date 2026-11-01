package codescan

import "strings"

// library is a family of rules that is about one library: opengrep-rules
// keeps those under <language>/<library>/, which a rule id carries as
// language.library. Its findings apply only to a repository whose manifests
// name one of packages, written ecosystem:name; a go: name ending in / is a
// module path prefix, and root: is an entry at the top of the tree.
// Directories that are about a language, a runtime or the standard library
// (lang, browser, audit, correctness, template, node-crypto, …) are not
// here, so their rules apply everywhere. Neither are languages whose
// manifests gitbay does not read.
type library struct {
	rules    string
	packages []string
}

var libraries = []library{
	{"go.aws-lambda", []string{"go:github.com/aws/aws-lambda-go"}},
	{"go.gorilla", []string{"go:github.com/gorilla/"}},
	{"go.gorm", []string{"go:gorm.io/gorm", "go:github.com/jinzhu/gorm"}},
	{"go.grpc", []string{"go:google.golang.org/grpc"}},
	{"go.jwt-go", []string{"go:github.com/dgrijalva/jwt-go", "go:github.com/golang-jwt/jwt/", "go:github.com/form3tech-oss/jwt-go"}},
	{"go.otto", []string{"go:github.com/robertkrimen/otto"}},

	{"javascript.ajv", []string{"npm:ajv"}},
	{"javascript.angular", []string{"npm:angular", "npm:@angular/core"}},
	{"javascript.apollo", []string{"npm:apollo-server", "npm:apollo-server-express", "npm:@apollo/server"}},
	{"javascript.argon2", []string{"npm:argon2"}},
	{"javascript.bluebird", []string{"npm:bluebird"}},
	{"javascript.chrome-remote-interface", []string{"npm:chrome-remote-interface"}},
	{"javascript.express", []string{"npm:express"}},
	{"javascript.fbjs", []string{"npm:fbjs"}},
	{"javascript.grpc", []string{"npm:grpc", "npm:@grpc/grpc-js"}},
	{"javascript.jose", []string{"npm:jose", "npm:node-jose"}},
	{"javascript.jquery", []string{"npm:jquery"}},
	{"javascript.jsonwebtoken", []string{"npm:jsonwebtoken"}},
	{"javascript.jwt-simple", []string{"npm:jwt-simple"}},
	{"javascript.monaco-editor", []string{"npm:monaco-editor"}},
	{"javascript.node-expat", []string{"npm:node-expat"}},
	{"javascript.passport-jwt", []string{"npm:passport-jwt"}},
	{"javascript.phantom", []string{"npm:phantom"}},
	{"javascript.playwright", []string{"npm:playwright", "npm:@playwright/test"}},
	{"javascript.puppeteer", []string{"npm:puppeteer", "npm:puppeteer-core"}},
	{"javascript.react", []string{"npm:react"}},
	{"javascript.sandbox", []string{"npm:sandbox"}},
	{"javascript.sax", []string{"npm:sax"}},
	{"javascript.sequelize", []string{"npm:sequelize"}},
	{"javascript.serialize-javascript", []string{"npm:serialize-javascript"}},
	{"javascript.shelljs", []string{"npm:shelljs"}},
	{"javascript.thenify", []string{"npm:thenify"}},
	{"javascript.vm2", []string{"npm:vm2"}},
	{"javascript.vue", []string{"npm:vue"}},
	{"javascript.wkhtmltoimage", []string{"npm:wkhtmltoimage"}},
	{"javascript.wkhtmltopdf", []string{"npm:wkhtmltopdf"}},
	{"javascript.xml2json", []string{"npm:xml2json"}},

	{"typescript.angular", []string{"npm:@angular/core"}},
	{"typescript.aws-cdk", []string{"npm:aws-cdk-lib", "npm:@aws-cdk/core"}},
	{"typescript.nestjs", []string{"npm:@nestjs/core"}},
	{"typescript.react", []string{"npm:react"}},
	{"typescript.react.portability.i18next", []string{"npm:i18next", "npm:react-i18next"}},

	{"python.airflow", []string{"pypi:apache-airflow"}},
	{"python.attr", []string{"pypi:attrs"}},
	{"python.bokeh", []string{"pypi:bokeh"}},
	{"python.boto3", []string{"pypi:boto3"}},
	{"python.click", []string{"pypi:click"}},
	{"python.cryptography", []string{"pypi:cryptography"}},
	{"python.distributed", []string{"pypi:distributed", "pypi:dask"}},
	{"python.django", []string{"pypi:django"}},
	{"python.docker", []string{"pypi:docker"}},
	{"python.fastapi", []string{"pypi:fastapi"}},
	{"python.flask", []string{"pypi:flask"}},
	{"python.jinja2", []string{"pypi:jinja2", "pypi:flask"}},
	{"python.jwt", []string{"pypi:pyjwt"}},
	{"python.pycryptodome", []string{"pypi:pycryptodome", "pypi:pycryptodomex"}},
	{"python.pymongo", []string{"pypi:pymongo"}},
	{"python.pyramid", []string{"pypi:pyramid"}},
	{"python.requests", []string{"pypi:requests"}},
	{"python.sh", []string{"pypi:sh"}},
	{"python.sqlalchemy", []string{"pypi:sqlalchemy", "pypi:flask-sqlalchemy"}},
	{"python.twilio", []string{"pypi:twilio"}},

	// Lints for writing Semgrep rules, which match any YAML with a list
	// of rules that have ids.
	{"yaml.semgrep", []string{"root:.semgrep", "root:.semgrep.yml", "root:.semgrep.yaml", "pypi:semgrep"}},
}

// libraryFor returns the most specific library a rule id belongs to. The
// id may carry a prefix from where the rules were mounted
// (opt.opengrep-rules.python.django…).
func libraryFor(rule string) (library, bool) {
	var best library
	found := false
	for _, l := range libraries {
		if ruleIn(rule, l.rules) && len(l.rules) > len(best.rules) {
			best, found = l, true
		}
	}
	return best, found
}

// ruleIn reports whether a rule id is family or within it: family is a
// whole id, a run of its dot-separated segments, or a suffix.
func ruleIn(rule, family string) bool {
	return rule == family || strings.HasPrefix(rule, family+".") ||
		strings.HasSuffix(rule, "."+family) || strings.Contains(rule, "."+family+".")
}

// name is how the issue and status refer to a library.
func (l library) name() string {
	return l.rules[strings.LastIndex(l.rules, ".")+1:]
}
