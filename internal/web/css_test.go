package web

import (
	"io/fs"
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
	{"ink", "paper", 4.5},
	{"ink", "surface", 4.5},
	{"ink", "sunken", 4.5},
	{"ink", "accent-soft", 4.5},
	{"ink", "ok-bg", 4.5},
	{"ink", "danger-bg", 4.5},
	{"muted", "paper", 4.5},
	{"muted", "surface", 4.5},
	{"muted", "sunken", 4.5},
	{"accent", "paper", 4.5},
	{"accent", "surface", 4.5},
	{"accent-strong", "paper", 4.5},
	{"accent-strong", "surface", 4.5},
	{"on-accent", "accent", 4.5},
	{"on-accent", "accent-strong", 4.5},
	{"on-danger", "danger-solid", 4.5},
	{"on-danger", "danger-strong", 4.5},
	{"danger", "paper", 4.5},
	{"danger", "surface", 4.5},
	{"danger", "danger-bg", 4.5},
	{"ok", "paper", 4.5},
	{"ok", "surface", 4.5},
	{"ok", "ok-bg", 4.5},
	{"warn", "paper", 4.5},
	{"warn", "surface", 4.5},
	{"warn", "warn-bg", 4.5},
	{"edge", "surface", 3},
	{"edge", "paper", 3},
	{"edge", "accent-soft", 3},
	{"accent", "surface", 3},
	{"accent", "paper", 3},
	{"accent", "accent-soft", 3},
	{"danger-solid", "paper", 3},
	{"danger-solid", "surface", 3},
	{"ok", "surface", 3},
	{"warn", "surface", 3},
	{"danger", "surface", 3},
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

	if len(contrastPairs) != 37 {
		t.Fatalf("contrast table has %d rows, want the 37 of PLAN section 3.3", len(contrastPairs))
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
		for _, bg := range []string{"paper", "surface"} {
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
