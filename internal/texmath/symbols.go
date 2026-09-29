package texmath

type sym struct {
	tag     string
	text    string
	upright bool // mathvariant="normal": TeX sets capital Greek upright
	fixed   bool // stretchy="false": a bracket typed without \left
	limits  bool
}

func mi(s string) sym      { return sym{tag: "mi", text: s} }
func mo(s string) sym      { return sym{tag: "mo", text: s} }
func upright(s string) sym { return sym{tag: "mi", text: s, upright: true} }
func bracket(s string) sym { return sym{tag: "mo", text: s, fixed: true} }
func large(s string) sym   { return sym{tag: "mo", text: s, limits: true} }

var symbols = map[string]sym{
	// Greek
	"alpha": mi("α"), "beta": mi("β"), "gamma": mi("γ"), "delta": mi("δ"),
	"epsilon": mi("ϵ"), "varepsilon": mi("ε"), "zeta": mi("ζ"), "eta": mi("η"),
	"theta": mi("θ"), "vartheta": mi("ϑ"), "iota": mi("ι"), "kappa": mi("κ"),
	"lambda": mi("λ"), "mu": mi("μ"), "nu": mi("ν"), "xi": mi("ξ"),
	"omicron": mi("ο"), "pi": mi("π"), "varpi": mi("ϖ"), "rho": mi("ρ"),
	"varrho": mi("ϱ"), "sigma": mi("σ"), "varsigma": mi("ς"), "tau": mi("τ"),
	"upsilon": mi("υ"), "phi": mi("ϕ"), "varphi": mi("φ"), "chi": mi("χ"),
	"psi": mi("ψ"), "omega": mi("ω"),
	"Gamma": upright("Γ"), "Delta": upright("Δ"), "Theta": upright("Θ"),
	"Lambda": upright("Λ"), "Xi": upright("Ξ"), "Pi": upright("Π"),
	"Sigma": upright("Σ"), "Upsilon": upright("Υ"), "Phi": upright("Φ"),
	"Psi": upright("Ψ"), "Omega": upright("Ω"),

	// Letter-like
	"infty": mi("∞"), "partial": mi("∂"), "nabla": mi("∇"), "emptyset": mi("∅"),
	"varnothing": mi("∅"), "hbar": mi("ℏ"), "ell": mi("ℓ"), "Re": mi("ℜ"),
	"Im": mi("ℑ"), "aleph": mi("ℵ"), "wp": mi("℘"), "imath": mi("ı"), "jmath": mi("ȷ"),

	// Binary operators
	"pm": mo("±"), "mp": mo("∓"), "times": mo("×"), "div": mo("÷"),
	"cdot": mo("⋅"), "ast": mo("∗"), "star": mo("⋆"), "circ": mo("∘"),
	"bullet": mo("∙"), "cap": mo("∩"), "cup": mo("∪"), "setminus": mo("∖"),
	"wedge": mo("∧"), "land": mo("∧"), "vee": mo("∨"), "lor": mo("∨"),
	"oplus": mo("⊕"), "ominus": mo("⊖"), "otimes": mo("⊗"), "odot": mo("⊙"),
	"sqcup": mo("⊔"), "sqcap": mo("⊓"), "dagger": mo("†"), "ddagger": mo("‡"),
	"lnot": mo("¬"), "neg": mo("¬"),

	// Relations
	"le": mo("≤"), "leq": mo("≤"), "ge": mo("≥"), "geq": mo("≥"),
	"ne": mo("≠"), "neq": mo("≠"), "ll": mo("≪"), "gg": mo("≫"),
	"approx": mo("≈"), "equiv": mo("≡"), "sim": mo("∼"), "simeq": mo("≃"),
	"cong": mo("≅"), "propto": mo("∝"), "in": mo("∈"), "notin": mo("∉"),
	"ni": mo("∋"), "subset": mo("⊂"), "supset": mo("⊃"), "subseteq": mo("⊆"),
	"supseteq": mo("⊇"), "perp": mo("⊥"), "parallel": mo("∥"), "mid": mo("∣"),
	"vdash": mo("⊢"), "models": mo("⊨"), "coloneqq": mo("≔"), "prec": mo("≺"),
	"succ": mo("≻"), "preceq": mo("⪯"), "succeq": mo("⪰"), "doteq": mo("≐"),

	// Arrows
	"to": mo("→"), "rightarrow": mo("→"), "leftarrow": mo("←"), "gets": mo("←"),
	"leftrightarrow": mo("↔"), "Rightarrow": mo("⇒"), "Leftarrow": mo("⇐"),
	"Leftrightarrow": mo("⇔"), "implies": mo("⟹"), "impliedby": mo("⟸"),
	"iff": mo("⟺"), "mapsto": mo("↦"), "longrightarrow": mo("⟶"),
	"longleftarrow": mo("⟵"), "Longrightarrow": mo("⟹"), "uparrow": mo("↑"),
	"downarrow": mo("↓"), "hookrightarrow": mo("↪"), "rightharpoonup": mo("⇀"),

	// Logic and sets
	"forall": mo("∀"), "exists": mo("∃"), "nexists": mo("∄"), "therefore": mo("∴"),
	"because": mo("∵"), "top": mo("⊤"), "bot": mo("⊥"), "angle": mo("∠"),
	"prime": mo("′"), "degree": mo("°"),

	// Dots
	"ldots": mo("…"), "dots": mo("…"), "cdots": mo("⋯"), "vdots": mo("⋮"),
	"ddots": mo("⋱"),

	// Brackets typed without \left
	"{": bracket("{"), "}": bracket("}"), "|": bracket("‖"),
	"langle": bracket("⟨"), "rangle": bracket("⟩"), "lvert": bracket("|"),
	"rvert": bracket("|"), "lVert": bracket("‖"), "rVert": bracket("‖"),
	"vert": bracket("|"), "Vert": bracket("‖"), "lfloor": bracket("⌊"),
	"rfloor": bracket("⌋"), "lceil": bracket("⌈"), "rceil": bracket("⌉"),
	"lbrace": bracket("{"), "rbrace": bracket("}"), "backslash": mo("\\"),

	// Escaped characters
	"%": mo("%"), "$": mo("$"), "#": mo("#"), "&": mo("&"), "_": mo("_"),

	// Large operators
	"sum": large("∑"), "prod": large("∏"), "coprod": large("∐"),
	"bigcup": large("⋃"), "bigcap": large("⋂"), "bigvee": large("⋁"),
	"bigwedge": large("⋀"), "bigoplus": large("⨁"), "bigotimes": large("⨂"),
	"bigsqcup": large("⨆"),
	"int":      mo("∫"), "iint": mo("∬"), "iiint": mo("∭"), "oint": mo("∮"),
}

// spaces are the spacing commands and their widths.
var spaces = map[string]string{
	",": "0.1667em", "thinspace": "0.1667em",
	":": "0.2222em", ">": "0.2222em", "medspace": "0.2222em",
	";": "0.2778em", "thickspace": "0.2778em",
	"!": "-0.1667em", "negthinspace": "-0.1667em",
	" ": "0.3333em", "enspace": "0.5em",
	"quad": "1em", "qquad": "2em",
}

// functions are the named functions, set upright; true takes limits under
// and over in display style.
var functions = map[string]bool{
	"sin": false, "cos": false, "tan": false, "cot": false, "sec": false, "csc": false,
	"arcsin": false, "arccos": false, "arctan": false, "sinh": false, "cosh": false,
	"tanh": false, "coth": false, "log": false, "ln": false, "lg": false, "exp": false,
	"dim": false, "deg": false, "hom": false, "ker": false, "arg": false,
	"lim": true, "liminf": true, "limsup": true, "max": true, "min": true,
	"sup": true, "inf": true, "det": true, "gcd": true, "Pr": true,
}

type accent struct {
	mark   string
	accent bool // mover accent="true": the mark sits close, like an accent
	under  bool
}

var accents = map[string]accent{
	"hat": {mark: "^", accent: true}, "widehat": {mark: "^", accent: true},
	"tilde": {mark: "~", accent: true}, "widetilde": {mark: "~", accent: true},
	"bar": {mark: "¯", accent: true}, "overline": {mark: "‾", accent: true},
	"vec": {mark: "→", accent: true}, "overrightarrow": {mark: "→", accent: true},
	"dot": {mark: "˙", accent: true}, "ddot": {mark: "¨", accent: true},
	"acute": {mark: "´", accent: true}, "grave": {mark: "`", accent: true},
	"breve": {mark: "˘", accent: true}, "check": {mark: "ˇ", accent: true},
	"overbrace":  {mark: "⏞"},
	"underline":  {mark: "_", under: true},
	"underbrace": {mark: "⏟", under: true},
}

var fonts = map[string]string{
	"mathrm": "rm", "mathbf": "bf", "boldsymbol": "bf", "bm": "bf",
	"mathbb": "bb", "mathcal": "cal", "mathscr": "cal", "mathfrak": "frak",
	"mathsf": "sf", "mathtt": "tt", "mathit": "it",
}

var delimiters = map[string]string{
	"{": "{", "}": "}", "lbrace": "{", "rbrace": "}", "|": "‖",
	"langle": "⟨", "rangle": "⟩", "lvert": "|", "rvert": "|", "vert": "|",
	"lVert": "‖", "rVert": "‖", "Vert": "‖", "lfloor": "⌊", "rfloor": "⌋",
	"lceil": "⌈", "rceil": "⌉", "backslash": "\\",
}

// alphabet is where a font's capitals, small letters and digits start in
// Mathematical Alphanumeric Symbols (0 where the font has none), plus the
// letters Unicode had already encoded elsewhere and left as holes.
type alphabet struct {
	upper, lower, digit rune
	holes               map[rune]rune
}

var alphabets = map[string]alphabet{
	"bf": {0x1D400, 0x1D41A, 0x1D7CE, nil},
	"it": {0x1D434, 0x1D44E, 0, map[rune]rune{'h': 'ℎ'}},
	"bb": {0x1D538, 0x1D552, 0x1D7D8, map[rune]rune{
		'C': 'ℂ', 'H': 'ℍ', 'N': 'ℕ', 'P': 'ℙ', 'Q': 'ℚ', 'R': 'ℝ', 'Z': 'ℤ'}},
	"cal": {0x1D49C, 0x1D4B6, 0, map[rune]rune{
		'B': 'ℬ', 'E': 'ℰ', 'F': 'ℱ', 'H': 'ℋ', 'I': 'ℐ', 'L': 'ℒ', 'M': 'ℳ',
		'R': 'ℛ', 'e': 'ℯ', 'g': 'ℊ', 'o': 'ℴ'}},
	"frak": {0x1D504, 0x1D51E, 0, map[rune]rune{
		'C': 'ℭ', 'H': 'ℌ', 'I': 'ℑ', 'R': 'ℜ', 'Z': 'ℨ'}},
	"sf": {0x1D5A0, 0x1D5BA, 0x1D7E2, nil},
	"tt": {0x1D670, 0x1D68A, 0x1D7F6, nil},
}

func alphanumeric(font string, r rune) rune {
	a, ok := alphabets[font]
	if !ok {
		return r
	}
	if h, ok := a.holes[r]; ok {
		return h
	}
	switch {
	case r >= 'A' && r <= 'Z':
		return a.upper + r - 'A'
	case r >= 'a' && r <= 'z':
		return a.lower + r - 'a'
	case r >= '0' && r <= '9' && a.digit != 0:
		return a.digit + r - '0'
	}
	return r
}
