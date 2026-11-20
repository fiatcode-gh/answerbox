package web

import (
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// cssRule is one style rule of app.css: its selector and its declarations,
// with tokens telling whether it is one of the two token blocks.
type cssRule struct {
	selector string
	decls    []string
	tokens   bool
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parseCSS splits the stylesheet into style rules. At-rule blocks (@media)
// are entered; the first top-level :root rule and the :root rule inside
// @media (prefers-color-scheme: dark) are marked as token blocks.
func parseCSS(t *testing.T, css string) []cssRule {
	t.Helper()
	css = cssComment.ReplaceAllString(css, "")
	type block struct {
		prelude string
		rule    int // index into rules, -1 for an at-rule block
	}
	var (
		rules     []cssRule
		stack     []block
		buf       strings.Builder
		lightSeen bool
	)
	inDark := func() bool {
		for _, b := range stack {
			if b.rule < 0 && strings.Contains(b.prelude, "prefers-color-scheme: dark") {
				return true
			}
		}
		return false
	}
	for _, c := range css {
		switch c {
		case '{':
			prelude := strings.TrimSpace(buf.String())
			buf.Reset()
			if strings.HasPrefix(prelude, "@") {
				stack = append(stack, block{prelude: prelude, rule: -1})
				continue
			}
			r := cssRule{selector: prelude}
			if prelude == ":root" {
				switch {
				case len(stack) == 0 && !lightSeen:
					r.tokens, lightSeen = true, true
				case inDark():
					r.tokens = true
				}
			}
			rules = append(rules, r)
			stack = append(stack, block{prelude: prelude, rule: len(rules) - 1})
		case ';', '}':
			text := strings.TrimSpace(buf.String())
			buf.Reset()
			if len(stack) == 0 {
				if c == '}' {
					t.Fatal("app.css: unbalanced }")
				}
				continue
			}
			top := stack[len(stack)-1]
			if text != "" && top.rule >= 0 {
				rules[top.rule].decls = append(rules[top.rule].decls, text)
			}
			if c == '}' {
				stack = stack[:len(stack)-1]
			}
		default:
			buf.WriteRune(c)
		}
	}
	if len(stack) != 0 {
		t.Fatal("app.css: unbalanced {")
	}
	return rules
}

// cssTokens returns the --ge-* values of the light and the dark theme; dark
// is light overridden by the dark block.
func cssTokens(t *testing.T) (light, darkOnly map[string]string) {
	t.Helper()
	light, darkOnly = map[string]string{}, map[string]string{}
	blocks := 0
	for _, r := range parseCSS(t, readStatic(t, "app.css")) {
		if !r.tokens {
			continue
		}
		into := light
		if blocks > 0 {
			into = darkOnly
		}
		blocks++
		for _, d := range r.decls {
			name, value, ok := strings.Cut(d, ":")
			if name = strings.TrimSpace(name); ok && strings.HasPrefix(name, "--ge-") {
				into[strings.TrimPrefix(name, "--ge-")] = strings.TrimSpace(value)
			}
		}
	}
	if blocks != 2 {
		t.Fatalf("app.css has %d token blocks, want 2 (:root and :root in the dark media query)", blocks)
	}
	return light, darkOnly
}

func isColour(v string) bool { return strings.HasPrefix(v, "#") || strings.HasPrefix(v, "rgb(") }

var hex6 = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	if !hex6.MatchString(hex) {
		t.Fatalf("%q is not a 6-digit hex colour", hex)
	}
	var l [3]float64
	for i := range l {
		n, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		c := float64(n) / 255
		if c <= 0.04045 {
			l[i] = c / 12.92
		} else {
			l[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// contrastPairs is the table of PLAN section 3.3: text pairs need 4.5,
// control edges, filled controls and the focus ring 3.
var contrastPairs = []struct {
	fg, bg string
	min    float64
}{
	{"ink", "bg", 4.5},
	{"ink", "surface", 4.5},
	{"ink", "sunken", 4.5},
	{"ink", "brand-soft", 4.5},
	{"ink", "gold-soft", 4.5},
	{"ink", "danger-soft", 4.5},
	{"muted", "bg", 4.5},
	{"muted", "surface", 4.5},
	{"muted", "sunken", 4.5},
	{"brand", "bg", 4.5},
	{"brand", "surface", 4.5},
	{"brand-strong", "bg", 4.5},
	{"brand-strong", "surface", 4.5},
	{"brand-strong", "brand-soft", 4.5},
	{"on-brand", "brand", 4.5},
	{"on-brand", "brand-strong", 4.5},
	{"gold-ink", "gold-soft", 4.5},
	{"gold-ink", "surface", 4.5},
	{"gold-ink", "bg", 4.5},
	{"on-gold", "gold", 4.5},
	{"danger", "bg", 4.5},
	{"danger", "surface", 4.5},
	{"danger", "danger-soft", 4.5},
	{"on-danger", "danger", 4.5},
	{"on-danger", "danger-strong", 4.5},
	{"closed-ink", "closed-bg", 4.5},
	{"chalk", "board", 4.5},
	{"chalk-muted", "board", 4.5},
	{"board", "chalk", 4.5},
	{"board", "chalk-strong", 4.5},
	{"band-ink", "band", 4.5},
	{"band-muted", "band", 4.5},
	{"band-link", "band", 4.5},
	{"gold", "band", 4.5},
	{"band-ink", "band-code", 4.5},
	{"edge", "surface", 3},
	{"edge", "bg", 3},
	{"brand", "surface", 3},
	{"brand", "bg", 3},
	{"brand", "brand-soft", 3},
	{"focus", "bg", 3},
	{"focus", "surface", 3},
	{"focus", "brand-soft", 3},
	{"gold", "board", 3},
	{"chalk-muted", "board", 3},
	{"chalk", "board", 3},
	{"danger", "surface", 3},
	{"danger", "bg", 3},
	{"gold", "band", 3},
	{"band-muted", "band", 3},
}

var markColours = regexp.MustCompile(`<rect [^>]*fill="(#[0-9a-fA-F]{6})"[^>]*/><path [^>]*stroke="(#[0-9a-fA-F]{6})"`)

func TestCSS_TokensContrastAA(t *testing.T) {
	light, darkOnly := cssTokens(t)

	colours := func(m map[string]string) []string {
		var names []string
		for k, v := range m {
			if isColour(v) {
				names = append(names, k)
			}
		}
		slices.Sort(names)
		return names
	}
	if l, d := colours(light), colours(darkOnly); !slices.Equal(l, d) {
		t.Errorf("colour tokens differ between the themes:\nlight %v\ndark  %v", l, d)
	}
	dark := map[string]string{}
	for k, v := range light {
		dark[k] = v
	}
	for k, v := range darkOnly {
		dark[k] = v
	}

	if len(contrastPairs) != 50 {
		t.Fatalf("contrast table has %d rows, want the 50 of PLAN section 3.3", len(contrastPairs))
	}
	themes := []struct {
		name   string
		tokens map[string]string
	}{{"light", light}, {"dark", dark}}
	for _, p := range contrastPairs {
		for _, th := range themes {
			fg, bg := th.tokens[p.fg], th.tokens[p.bg]
			if fg == "" || bg == "" {
				t.Errorf("%s: token %s or %s is missing", th.name, p.fg, p.bg)
				continue
			}
			r := contrast(t, fg, bg)
			t.Logf("%-5s %-13s on %-13s %5.2f (min %.1f)", th.name, p.fg, p.bg, r, p.min)
			if r < p.min {
				t.Errorf("%s: %s %s on %s %s = %.2f, below %.1f", th.name, p.fg, fg, p.bg, bg, r, p.min)
			}
		}
	}

	m := markColours.FindStringSubmatch(readStatic(t, "mark.svg"))
	if m == nil {
		t.Fatal("mark.svg: no rect fill followed by a path stroke")
	}
	fill, tick := m[1], m[2]
	if tick != "#ffffff" {
		t.Errorf("mark tick = %s, want #ffffff", tick)
	}
	for _, th := range themes {
		for _, bg := range []string{"bg", "surface"} {
			r := contrast(t, fill, th.tokens[bg])
			t.Logf("%-5s mark %s on %-13s %5.2f (min 3.0)", th.name, fill, bg, r)
			if r < 3 {
				t.Errorf("%s: mark %s on %s = %.2f, below 3", th.name, fill, bg, r)
			}
		}
	}
	r := contrast(t, tick, fill)
	t.Logf("      mark tick %s on %s %5.2f (min 3.0)", tick, fill, r)
	if r < 3 {
		t.Errorf("mark tick %s on %s = %.2f, below 3", tick, fill, r)
	}
}

var fontFace = regexp.MustCompile(`@font-face\s*\{([^}]*)\}`)

func TestCSS_FontFace(t *testing.T) {
	css := cssComment.ReplaceAllString(readStatic(t, "app.css"), "")
	if n := strings.Count(css, "@font-face"); n != 1 {
		t.Fatalf("app.css has %d @font-face rules, want 1", n)
	}
	m := fontFace.FindStringSubmatch(css)
	if m == nil {
		t.Fatal("app.css: @font-face has no block")
	}
	for _, want := range []string{
		`font-family: "Atkinson Hyperlegible Next"`,
		"font-weight: 200 800",
		"font-display: swap",
		`url("fonts/AtkinsonHyperlegibleNext-latin.woff2") format("woff2")`,
	} {
		if !strings.Contains(m[1], want) {
			t.Errorf("@font-face lacks %q:\n%s", want, m[1])
		}
	}
}

// iconTokens are the icons of PLAN section 3.8, in table order, then the
// language switch's icon (PLAN section 7).
var iconTokens = []string{
	"arrow", "down", "check", "clock", "alert", "person", "resume", "play", "stop", "copy", "screen",
	"chart", "pencil", "history", "download", "upload", "plus", "archive", "lock", "calendar", "eye", "table",
	"languages",
}

func TestCSS_IconTokens(t *testing.T) {
	light, darkOnly := cssTokens(t)
	if len(iconTokens) != 23 {
		t.Fatalf("icon list has %d names, want 23", len(iconTokens))
	}
	for _, name := range iconTokens {
		v, ok := light["i-"+name]
		if !ok {
			t.Errorf("--ge-i-%s is not in the first token block", name)
			continue
		}
		if !strings.HasPrefix(v, `url("data:image/svg+xml,`) {
			t.Errorf("--ge-i-%s = %.60q…, want a url(\"data:image/svg+xml,…\")", name, v)
		}
		if strings.ContainsAny(v, ";{#") {
			t.Errorf("--ge-i-%s holds ; { or #: %q", name, v)
		}
		if _, ok := darkOnly["i-"+name]; ok {
			t.Errorf("--ge-i-%s is redefined in the dark block", name)
		}
	}
}

// TestCSS_TokensUsed keeps the token block free of leftovers: every token of
// the first block is read by a rule outside the two token blocks.
func TestCSS_TokensUsed(t *testing.T) {
	light, _ := cssTokens(t)
	var used strings.Builder
	for _, r := range parseCSS(t, readStatic(t, "app.css")) {
		if r.tokens {
			continue
		}
		for _, d := range r.decls {
			used.WriteString(d)
			used.WriteByte('\n')
		}
	}
	text := used.String()
	for _, name := range slices.Sorted(maps.Keys(light)) {
		ref := regexp.MustCompile(`var\(\s*--ge-` + regexp.QuoteMeta(name) + `\s*[,)]`)
		if !ref.MatchString(text) {
			t.Errorf("--ge-%s is defined but no rule outside the token blocks reads it", name)
		}
	}
	if len(light) < 50 {
		t.Fatalf("checked only %d tokens", len(light))
	}
}

var colourValue = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(|\b(?:white|black|red|green|blue|gray|grey|yellow|orange)\b`)

func TestCSS_ColoursOnlyInTokens(t *testing.T) {
	decls := 0
	for _, r := range parseCSS(t, readStatic(t, "app.css")) {
		if r.tokens {
			continue
		}
		for _, d := range r.decls {
			decls++
			_, value, _ := strings.Cut(d, ":")
			if m := colourValue.FindString(value); m != "" {
				t.Errorf("%s { %s }: colour %q outside the token blocks", r.selector, d, m)
			}
		}
	}
	if decls < 100 {
		t.Fatalf("checked only %d declarations", decls)
	}
}

var (
	selectorNoise = regexp.MustCompile(`\[[^\]]*\]|"[^"]*"|'[^']*'`)
	selectorName  = regexp.MustCompile(`[.#](-?[A-Za-z_][-A-Za-z0-9_]*)`)
)

func TestCSS_NoDeadSelectors(t *testing.T) {
	var corpus strings.Builder
	for _, glob := range []struct {
		fsys    fs.FS
		pattern string
	}{{templateFS, "templates/*.html"}, {staticFS, "static/*.js"}} {
		paths, err := fs.Glob(glob.fsys, glob.pattern)
		if err != nil || len(paths) == 0 {
			t.Fatalf("glob %s: %v %v", glob.pattern, paths, err)
		}
		for _, p := range paths {
			b, err := fs.ReadFile(glob.fsys, p)
			if err != nil {
				t.Fatal(err)
			}
			corpus.Write(b)
			corpus.WriteByte('\n')
		}
	}
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range goFiles {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		corpus.Write(b)
		corpus.WriteByte('\n')
	}
	text := corpus.String()

	seen := map[string]bool{}
	for _, r := range parseCSS(t, readStatic(t, "app.css")) {
		for _, m := range selectorName.FindAllStringSubmatch(selectorNoise.ReplaceAllString(r.selector, ""), -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			word := regexp.MustCompile(`(?:^|[^-A-Za-z0-9_])` + regexp.QuoteMeta(name) + `(?:[^-A-Za-z0-9_]|$)`)
			if !word.MatchString(text) {
				t.Errorf("selector %q: nothing emits %q", r.selector, name)
			}
		}
	}
	if len(seen) < 50 {
		t.Fatalf("checked only %d class and id names", len(seen))
	}
}
