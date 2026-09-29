package texmath

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestConvert(t *testing.T) {
	cases := []struct {
		tex, want string
	}{
		{`x`, `<math><mi>x</mi></math>`},
		{`12.5+x`, `<math><mn>12.5</mn><mo>+</mo><mi>x</mi></math>`},
		{`a-b`, `<math><mi>a</mi><mo>−</mo><mi>b</mi></math>`},
		{`x^2`, `<math><msup><mi>x</mi><mn>2</mn></msup></math>`},
		{`x_i^2`, `<math><msubsup><mi>x</mi><mi>i</mi><mn>2</mn></msubsup></math>`},
		{`x^{2n}`, `<math><msup><mi>x</mi><mrow><mn>2</mn><mi>n</mi></mrow></msup></math>`},
		{`\frac{a}{b}`, `<math><mfrac><mrow><mi>a</mi></mrow><mrow><mi>b</mi></mrow></mfrac></math>`},
		{`\frac12`, `<math><mfrac><mn>1</mn><mn>2</mn></mfrac></math>`},
		{`\sqrt{x}`, `<math><msqrt><mrow><mi>x</mi></mrow></msqrt></math>`},
		{`\sqrt[3]{x}`, `<math><mroot><mrow><mi>x</mi></mrow><mrow><mn>3</mn></mrow></mroot></math>`},
		{`\alpha\Gamma`, `<math><mi>α</mi><mi mathvariant="normal">Γ</mi></math>`},
		{`a\le b`, `<math><mi>a</mi><mo>≤</mo><mi>b</mi></math>`},
		{`(a)`, `<math><mo stretchy="false">(</mo><mi>a</mi><mo stretchy="false">)</mo></math>`},
		{`\left( x \right.`, `<math><mrow><mo>(</mo><mi>x</mi></mrow></math>`},
		{`\left\{ x \middle| y \right\}`, `<math><mrow><mo>{</mo><mi>x</mi><mo>|</mo><mi>y</mi><mo>}</mo></mrow></math>`},
		{`\sin x`, `<math><mi>sin</mi><mspace width="0.1667em"></mspace><mi>x</mi></math>`},
		{`\sin(x)`, `<math><mi>sin</mi><mo stretchy="false">(</mo><mi>x</mi><mo stretchy="false">)</mo></math>`},
		{`\text{if } x`, `<math><mtext>if </mtext><mi>x</mi></math>`},
		{`\mathbb{R}\mathbf{v}`, `<math><mrow><mi>ℝ</mi></mrow><mrow><mi>𝐯</mi></mrow></math>`},
		{`\mathrm{d}x`, `<math><mrow><mi mathvariant="normal">d</mi></mrow><mi>x</mi></math>`},
		{`\hat{x}`, `<math><mover accent="true"><mrow><mi>x</mi></mrow><mo>^</mo></mover></math>`},
		{`a\,b`, `<math><mi>a</mi><mspace width="0.1667em"></mspace><mi>b</mi></math>`},
		{`\binom{n}{k}`, `<math><mrow><mo>(</mo><mfrac linethickness="0"><mrow><mi>n</mi></mrow><mrow><mi>k</mi></mrow></mfrac><mo>)</mo></mrow></math>`},
		{`\begin{pmatrix}a&b\\c&d\end{pmatrix}`, `<math><mrow><mo>(</mo><mtable><mtr><mtd><mi>a</mi></mtd><mtd><mi>b</mi></mtd></mtr><mtr><mtd><mi>c</mi></mtd><mtd><mi>d</mi></mtd></mtr></mtable><mo>)</mo></mrow></math>`},
		{`\begin{matrix}a\\\end{matrix}`, `<math><mtable><mtr><mtd><mi>a</mi></mtd></mtr></mtable></math>`},
		{`x % comment` + "\n" + `+1`, `<math><mi>x</mi><mo>+</mo><mn>1</mn></math>`},
		{`a<b`, `<math><mi>a</mi><mo>&lt;</mo><mi>b</mi></math>`},
	}
	for _, c := range cases {
		got, err := Convert(c.tex, false)
		if err != nil {
			t.Errorf("Convert(%q): %v", c.tex, err)
			continue
		}
		if got != c.want {
			t.Errorf("Convert(%q)\n got %s\nwant %s", c.tex, got, c.want)
		}
	}
}

func TestConvertDisplayLimits(t *testing.T) {
	got, err := Convert(`\sum_{i=1}^n i`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `<math display="block"><munderover><mo>∑</mo>`) {
		t.Errorf("display sum: %s", got)
	}
	got, _ = Convert(`\sum_{i=1}^n i`, false)
	if !strings.HasPrefix(got, `<math><msubsup><mo>∑</mo>`) {
		t.Errorf("inline sum: %s", got)
	}
	got, _ = Convert(`\lim_{x\to 0} f`, true)
	if !strings.HasPrefix(got, `<math display="block"><munder><mi>lim</mi>`) {
		t.Errorf("display lim: %s", got)
	}
}

func TestConvertRefuses(t *testing.T) {
	for _, tex := range []string{
		`\frac{a}`, `x^`, `{x`, `x}`, `x^1^2`, `\left( x`, `\right)`,
		`\begin{pmatrix}a\end{bmatrix}`, `\begin{tabular}x\end{tabular}`,
		`a & b`, `a \\ b`, `\unknown`,
		`\href{javascript:alert(1)}{x}`, `\url{javascript:alert(1)}`,
		`\style{color:red}{x}`, `\color{red}{x}`, `\class{a}{x}`, `\htmlId{a}{x}`,
		`\def\a{x}\a`, `\newcommand{\a}{x}`, `\require{html}`, `\unicode{x}`,
		`\includegraphics{x}`, `\input{/etc/passwd}`,
	} {
		if out, err := Convert(tex, false); err == nil {
			t.Errorf("Convert(%q) = %s, want an error", tex, out)
		}
	}
}

func TestConvertBounds(t *testing.T) {
	deep := strings.Repeat("{", 100000) + "x" + strings.Repeat("}", 100000)
	fracs := strings.Repeat(`\frac{`, MaxDepth+1) + "x"
	for _, tex := range []string{deep, fracs, strings.Repeat("x", MaxInput+1)} {
		start := time.Now()
		if _, err := Convert(tex, false); err == nil {
			t.Errorf("Convert(%.20q...) succeeded", tex)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("Convert(%.20q...) took %v", tex, d)
		}
	}
	// The largest accepted input: output stays linear in it.
	tex := strings.Repeat(`{}`, MaxInput/2)
	start := time.Now()
	out, err := Convert(tex, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 16*MaxInput {
		t.Errorf("output %d bytes for %d bytes of input", len(out), len(tex))
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
}

var tagAttr = regexp.MustCompile(`<([a-z]+)((?: [a-z]+="[^"]*")*)>`)
var attrPair = regexp.MustCompile(` ([a-z]+)="([^"]*)"`)

// Every element and attribute in the output is one Elements and Attrs name,
// so the sanitizer's allowlist built from them is complete.
func TestConvertEmitsOnlyListed(t *testing.T) {
	allowed := map[string]bool{}
	for _, e := range Elements {
		allowed[e] = true
	}
	var inputs []string
	for name := range symbols {
		inputs = append(inputs, `\`+name)
	}
	for name := range functions {
		inputs = append(inputs, `\`+name+`_a^b x`)
	}
	for name := range accents {
		inputs = append(inputs, `\`+name+`{x}`)
	}
	for name := range spaces {
		inputs = append(inputs, `a\`+name+` b`)
	}
	for name := range fonts {
		inputs = append(inputs, `\`+name+`{Ab1}`)
	}
	for name := range environments {
		inputs = append(inputs, `\begin{`+name+`}a&b\\c&d\end{`+name+`}`)
	}
	inputs = append(inputs, `\binom12`, `\sqrt[3]{x}`, `\left(\middle|\right)`, `\text{a}`,
		`\operatorname{rank}A`, `\operatorname*{arg\,max}_x`, `x_1^2`, `\sum_1^2`, `\sum_1`, `\sum^2`, `(a)~'`)
	for _, in := range inputs {
		for _, display := range []bool{false, true} {
			out, err := Convert(in, display)
			if err != nil {
				if strings.HasPrefix(in, `\begin{`) {
					continue // equation and friends take no & or \\
				}
				t.Errorf("Convert(%q): %v", in, err)
				continue
			}
			for _, m := range tagAttr.FindAllStringSubmatch(out, -1) {
				if !allowed[m[1]] {
					t.Errorf("%q emits <%s>", in, m[1])
				}
				for _, a := range attrPair.FindAllStringSubmatch(m[2], -1) {
					want, ok := Attrs[m[1]][a[1]]
					if !ok || (want != "<length>" && want != a[2]) {
						t.Errorf("%q emits %s %s=%q", in, m[1], a[1], a[2])
					}
				}
			}
		}
	}
}
