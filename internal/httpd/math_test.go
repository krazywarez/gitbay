package httpd

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

func TestMarkdownMath(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
		not       []string
	}{
		{"inline", "Euler: $e^{i\\pi}+1=0$.", []string{`<math><msup><mi>e</mi>`, `</math>.`}, nil},
		{"display inline", "so $$x^2$$ here", []string{`<math display="block"><msup>`}, nil},
		{"block", "text\n$$\n\\frac{a}{b}\n$$\nafter\n", []string{`<math display="block"><mfrac>`, `<p>after</p>`}, []string{"$$"}},
		{"one-line block", "$$x_1$$\n", []string{`<math display="block"><msub>`}, []string{"<p>"}},
		{"prices", "costs $5 and $10 today", []string{"costs $5 and $10 today"}, []string{"<math"}},
		{"space after open", "a $ x$ b", []string{"a $ x$ b"}, []string{"<math"}},
		{"space before close", "a $x $ b", []string{"a $x $ b"}, []string{"<math"}},
		{"digit after close", "$x$5", []string{"$x$5"}, []string{"<math"}},
		{"escaped dollars", `\$x\$`, []string{"$x$"}, []string{"<math"}},
		{"escaped dollar inside", `$a\$b$`, []string{`<mo>$</mo>`}, nil},
		{"code span", "`$x^2$`", []string{"<code>$x^2$</code>"}, []string{"<math"}},
		{"fenced code", "```\n$x^2$\n$$\ny\n$$\n```\n", []string{"$x^2$", "$$\ny\n$$"}, []string{"<math"}},
		{"indented code", "    $x$\n", []string{"$x$"}, []string{"<math"}},
		{"invalid inline", `see $\frac{a$ here`, []string{`see $\frac{a$ here`}, []string{"<math"}},
		{"invalid block", "$$\n\\frac{\n$$\n", []string{"<pre tabindex=\"0\">$$\\frac{\n$$</pre>"}, []string{"<math"}},
		{"unclosed block", "$$\nx\n", []string{"<pre"}, []string{"<math"}},
		{"markup is escaped", "$\\text{<b>&</b>}$", []string{`<mtext>&lt;b&gt;&amp;&lt;/b&gt;</mtext>`}, []string{"<b>"}},
	}
	for _, c := range cases {
		out := string(ugcHTML(c.src, "md"))
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: lacks %q:\n%s", c.name, w, out)
			}
		}
		for _, w := range c.not {
			if strings.Contains(out, w) {
				t.Errorf("%s: has %q:\n%s", c.name, w, out)
			}
		}
	}
}

func TestOrgMath(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
		not       []string
	}{
		{"dollar", "Euler: $e^x$ here", []string{`<math><msup><mi>e</mi><mi>x</mi></msup></math> here`}, nil},
		{"paren", `a \(x_1\) b`, []string{`<math><msub>`}, []string{`\(`}},
		{"bracket", `a \[x^2\] b`, []string{`<math display="block"><msup>`}, []string{`\[`}},
		{"double dollar", `a $$x$$ b`, []string{`<math display="block"><mi>x</mi></math>`}, nil},
		{"environment block", "\\begin{equation}\nx = \\frac{1}{2}\n\\end{equation}\n", []string{`<math display="block"><mrow><mi>x</mi><mo>=</mo><mfrac>`}, []string{`\begin`}},
		{"matrix block", "\\begin{pmatrix}\na & b \\\\\nc & d\n\\end{pmatrix}\n", []string{`<mtable><mtr><mtd><mi>a</mi></mtd>`}, nil},
		{"prices", "costs $5 and $10 today", []string{"costs $5 and $10 today"}, []string{"<math"}},
		{"verbatim", "=$x^2$= and ~$y$~", []string{"<code>$x^2$</code>", "<code>$y$</code>"}, []string{"<math"}},
		{"src block", "#+begin_src tex\n$x^2$\n#+end_src\n", []string{"<pre"}, []string{"<math"}},
		{"invalid", `see \(\frac{a\) here`, []string{`see \(\frac{a\) here`}, []string{"<math"}},
		{"invalid block", "\\begin{tabular}\nx\n\\end{tabular}\n", []string{`<pre tabindex="0">\begin{tabular}`}, []string{"<math"}},
	}
	for _, c := range cases {
		out := string(ugcHTML(c.src, "org"))
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: lacks %q:\n%s", c.name, w, out)
			}
		}
		for _, w := range c.not {
			if strings.Contains(out, w) {
				t.Errorf("%s: has %q:\n%s", c.name, w, out)
			}
		}
	}
}

// ugcPolicy admits the MathML texmath writes and nothing else, which is
// what an .html README or org's raw export would otherwise carry through.
func TestUGCPolicyMathML(t *testing.T) {
	hostile := `<math display="block" xmlns:xlink="http://www.w3.org/1999/xlink" style="x" onclick="x()">` +
		`<mi href="javascript:alert(1)" xlink:href="javascript:alert(2)" mathvariant="bold" style="color:red" onmouseover="x()">x</mi>` +
		`<mo stretchy="true" form="prefix">(</mo><mspace width="expression(alert(3))"></mspace><mspace width="1em"></mspace>` +
		`<semantics><annotation-xml encoding="text/html"><img src=x onerror="alert(4)"></annotation-xml></semantics>` +
		`<maction actiontype="statusline"><mi>y</mi></maction><mstyle mathcolor="red"><mi>z</mi></mstyle>` +
		`<mtext><style>*{}</style><script>alert(5)</script></mtext></math><math display="inline"></math>`
	out := ugcPolicy.Sanitize(hostile)
	for _, bad := range []string{"href", "xlink", "style", "onclick", "onmouseover", "onerror", "javascript",
		"annotation", "semantics", "maction", "mstyle", "mathcolor", "script", "expression",
		`mathvariant="bold"`, `stretchy="true"`, "form=", `display="inline"`} {
		if strings.Contains(out, bad) {
			t.Errorf("sanitized MathML keeps %q:\n%s", bad, out)
		}
	}
	for _, good := range []string{`<math display="block">`, `<mspace width="1em">`, "<mi>x</mi>", "<mi>y</mi>"} {
		if !strings.Contains(out, good) {
			t.Errorf("sanitized MathML lacks %q:\n%s", good, out)
		}
	}
}

// Hostile TeX is refused and shown as escaped source, in bounded time and
// size, on both syntaxes.
func TestHostileMath(t *testing.T) {
	long := strings.Repeat(`x+`, 1<<19) // 1 MiB in one expression
	deep := strings.Repeat("{", 50000) + "x" + strings.Repeat("}", 50000)
	for _, tex := range []string{
		`\href{javascript:alert(1)}{x}`, `\url{javascript:alert(1)}`, `\style{color:red}{x}`,
		`\color{red" onmouseover="alert(1)}{x}`, `\class{a"b}{x}`, `\def\a{\a\a}\a`,
		`\text{</math><script>alert(1)</script>}`, `\text{<img src=x onerror=alert(1)>}`,
		deep, long, strings.Repeat(`\sqrt{`, 10000) + "x",
	} {
		for _, doc := range []struct{ src, format string }{
			{"$" + tex + "$", "md"}, {"$$\n" + tex + "\n$$\n", "md"},
			{`\(` + tex + `\)`, "org"}, {"\\[" + tex + "\\]", "org"},
		} {
			start := time.Now()
			out := string(ugcHTML(doc.src, doc.format))
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("%.30q (%s) took %v", tex, doc.format, d)
			}
			if len(out) > 8*len(doc.src)+1024 {
				t.Errorf("%.30q (%s): %d bytes out for %d in", tex, doc.format, len(out), len(doc.src))
			}
			for _, bad := range []string{"<script", "<img", `href="`, `onmouseover="`, `style="`, `class="a`} {
				if strings.Contains(out, bad) {
					t.Errorf("%.30q (%s) emits %q:\n%.300s", tex, doc.format, bad, out)
				}
			}
		}
	}
	// A refused \text keeps its payload as visible text, escaped.
	out := string(ugcHTML(`$\href{javascript:alert(1)}{x}$`, "md"))
	if !strings.Contains(out, `$\href{javascript:alert(1)}{x}$`) || strings.Contains(out, "<math") || strings.Contains(out, "<a") {
		t.Errorf("refused \\href: %s", out)
	}
}

// The issue page renders its body's math through the shared path, and
// autolinking leaves text inside <math> alone.
func TestIssuePageRendersMath(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(repoID, uid, "math", `Area is $\pi r^2$, see $\text{#1}$.`, "md"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/alice/app/issues/1", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `<math><mi>π</mi><msup><mi>r</mi><mn>2</mn></msup></math>`) {
		t.Errorf("no MathML in the issue page:\n%s", body)
	}
	if !strings.Contains(body, `<mtext>#1</mtext>`) {
		t.Errorf("autolink rewrote text inside <math>:\n%s", body)
	}
}
