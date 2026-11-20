package web

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	htmltemplate "html/template"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"text/template/parse"
	"unicode"
)

// pendingTemplates are skipped by TestTemplates_NoLiteralText until the task
// that moves their text into the catalogue deletes their name here.
var pendingTemplates = []string{
	"admin_attempt.html",
	"admin_attempts.html",
	"admin_confirm.html",
	"admin_editor_fields.html",
	"admin_exam.html",
	"admin_exam_edit.html",
	"admin_exam_new.html",
	"admin_exam_settings.html",
	"admin_exams.html",
	"admin_header.html",
	"admin_login.html",
	"admin_projector.html",
	"admin_question.html",
	"admin_questions.html",
	"admin_revision.html",
	"admin_revisions.html",
	"admin_section.html",
	"admin_signed_in.html",
	"admin_teachers.html",
	"exam.html",
	"landing.html",
	"result.html",
}

// literalAllowed are the only words a template may hold outside an action,
// <code> and <pre>.
var literalAllowed = []string{
	"Answerbox",        // the product name, never translated
	"Indonesia",        // the switch's endonym
	"English",          // the switch's endonym
	"A", "B", "C", "D", // the letters of the landing page's benefit bubbles
}

// computedKeyFuncs may call T or N with a key built at run time; each has
// its own table test: errorPageMessage TestErrorPage_Texts, Date and Day
// TestDates.
var computedKeyFuncs = []string{"errorPageMessage", "Date", "Day"}

// enText is the English catalogue text of key; the test fails on an unknown
// key.
func enText(t testing.TB, key string) string {
	t.Helper()
	text, ok := lookup(langEN, key)
	if !ok {
		t.Fatalf("no English text for %q", key)
	}
	return text
}

// fatalRecorder is a testing.TB that records a fatal failure instead of
// stopping the test.
type fatalRecorder struct {
	testing.TB
	failed bool
}

func (f *fatalRecorder) Helper()               {}
func (f *fatalRecorder) Fatalf(string, ...any) { f.failed = true }

// useCatalogue replaces the catalogue for one test.
func useCatalogue(t *testing.T, en, id map[string]string) {
	t.Helper()
	saved := catalogue
	catalogue = map[lang]map[string]string{langEN: en, langID: id}
	t.Cleanup(func() { catalogue = saved })
}

func requireProblems(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, w) }) {
			t.Errorf("%s: no problem mentions %q; got %q", what, w, got)
		}
	}
	if len(want) == 0 && len(got) > 0 {
		t.Errorf("%s: %d problems:\n%s", what, len(got), strings.Join(got, "\n"))
	}
}

// --- catalogue rules ---

var keyRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)

func sameKeysProblems(bs []bundle) []string {
	var out []string
	owner := map[string]int{}
	for i, b := range bs {
		for _, side := range []struct {
			name        string
			this, other map[string]string
		}{{"en", b.en, b.id}, {"id", b.id, b.en}} {
			for k, v := range side.this {
				if _, ok := side.other[k]; !ok {
					out = append(out, fmt.Sprintf("bundle %d: %q has %s text only", i, k, side.name))
				}
				if v == "" {
					out = append(out, fmt.Sprintf("bundle %d: %q has an empty %s text", i, k, side.name))
				}
			}
		}
		for k := range b.en {
			if !keyRe.MatchString(k) {
				out = append(out, fmt.Sprintf("bundle %d: %q is not a key", i, k))
			}
			if j, ok := owner[k]; ok {
				out = append(out, fmt.Sprintf("%q is in bundles %d and %d", k, j, i))
			}
			owner[k] = i
		}
	}
	return out
}

func TestMessages_SameKeys(t *testing.T) {
	requireProblems(t, "catalogue", sameKeysProblems(bundles))
	requireProblems(t, "broken", sameKeysProblems([]bundle{
		{en: map[string]string{"a.one_side": "x", "a.empty": "", "Bad Key": "x", "a.twice": "x"}, id: map[string]string{"a.empty": "y", "Bad Key": "x", "a.twice": "x"}},
		{en: map[string]string{"a.twice": "x", "a.id_only_not": "x"}, id: map[string]string{"a.twice": "x", "a.id_only": "y", "a.id_only_not": "x"}},
	}), `"a.one_side" has en text only`, `"a.empty" has an empty en text`, `"Bad Key" is not a key`,
		`"a.twice" is in bundles 0 and 1`, `"a.id_only" has id text only`)
}

var verbRe = regexp.MustCompile(`%(\[(\d+)\])?[-+# 0]*(\d+)?(\.\d+)?([a-zA-Z%])?`)

// verbs lists a text's formatting verbs as "index:verb", sorted; malformed
// verbs are reported as problems.
func verbs(text string) (list []string, problems []string) {
	indexed, plain := 0, 0
	for _, m := range verbRe.FindAllStringSubmatch(text, -1) {
		switch {
		case m[5] == "%" && m[1] == "":
			continue
		case m[5] == "" || !strings.Contains("sdvq", m[5]):
			problems = append(problems, fmt.Sprintf("malformed verb %q", m[0]))
			continue
		}
		index := m[2]
		if index == "" {
			plain++
			index = strconv.Itoa(plain)
		} else {
			indexed++
		}
		list = append(list, index+":"+m[5])
	}
	if plain > 0 && (indexed > 0 || plain > 1) {
		problems = append(problems, "two or more arguments need indexed verbs")
	}
	slices.Sort(list)
	return list, problems
}

func sameVerbsProblems(bs []bundle) []string {
	var out []string
	for _, b := range bs {
		for k, en := range b.en {
			id, ok := b.id[k]
			if !ok {
				continue
			}
			ev, ep := verbs(en)
			iv, ip := verbs(id)
			for _, p := range append(ep, ip...) {
				out = append(out, fmt.Sprintf("%q: %s", k, p))
			}
			if !slices.Equal(ev, iv) {
				out = append(out, fmt.Sprintf("%q: verbs differ: en %v, id %v", k, ev, iv))
			}
		}
	}
	return out
}

func TestMessages_SameVerbs(t *testing.T) {
	requireProblems(t, "catalogue", sameVerbsProblems(bundles))
	requireProblems(t, "broken", sameVerbsProblems([]bundle{{
		en: map[string]string{"a.order": "%[1]s of %[2]d", "a.kind": "%s", "a.bad": "x %[1]", "a.plain": "%s and %d", "a.ok": "%[2]d / %[1]s 100%%"},
		id: map[string]string{"a.order": "%[1]s dari %[1]d", "a.kind": "%d", "a.bad": "x %[1]", "a.plain": "%s dan %d", "a.ok": "%[1]s / %[2]d 100%%"},
	}}), `"a.order": verbs differ`, `"a.kind": verbs differ`, `"a.bad": malformed verb`, `"a.plain": two or more arguments need indexed verbs`)
	if p := sameVerbsProblems([]bundle{{en: map[string]string{"a.ok": "%[2]d / %[1]s 100%%"}, id: map[string]string{"a.ok": "%[1]s / %[2]d 100%%"}}}); len(p) > 0 {
		t.Errorf("reordered verbs refused: %q", p)
	}
}

var markupTagRe = regexp.MustCompile(`<[^>]*>`)

func htmlKeysProblems(bs []bundle) []string {
	var out []string
	for _, b := range bs {
		for k, en := range b.en {
			id := b.id[k]
			isHTML := strings.HasSuffix(k, "_html")
			for _, text := range []string{en, id} {
				if strings.Contains(text, "<") != isHTML {
					out = append(out, fmt.Sprintf("%q: an _html key exactly when the text holds <: %q", k, text))
				}
			}
			if isHTML && !slices.Equal(markupTagRe.FindAllString(en, -1), markupTagRe.FindAllString(id, -1)) {
				out = append(out, fmt.Sprintf("%q: tags differ between en and id", k))
			}
		}
	}
	return out
}

func TestMessages_HTMLKeys(t *testing.T) {
	requireProblems(t, "catalogue", htmlKeysProblems(bundles))
	requireProblems(t, "broken", htmlKeysProblems([]bundle{{
		en: map[string]string{"a.plain": "x <em>y</em>", "a.empty_html": "x", "a.tags_html": "<em>x</em>", "a.ok_html": `<a href="%[1]s">x</a>`},
		id: map[string]string{"a.plain": "x <em>y</em>", "a.empty_html": "x", "a.tags_html": "<strong>x</strong>", "a.ok_html": `<a href="%[1]s">y</a>`},
	}}), `"a.plain": an _html key`, `"a.empty_html": an _html key`, `"a.tags_html": tags differ`)
}

func pluralPairsProblems(bs []bundle) []string {
	var out []string
	for _, b := range bs {
		for _, m := range []map[string]string{b.en, b.id} {
			for k := range m {
				for _, pair := range [][2]string{{".one", ".other"}, {".other", ".one"}} {
					if stem, ok := strings.CutSuffix(k, pair[0]); ok {
						if _, ok := m[stem+pair[1]]; !ok {
							out = append(out, fmt.Sprintf("%q has no %s form", k, pair[1]))
						}
					}
				}
			}
		}
	}
	return out
}

func TestMessages_PluralPairs(t *testing.T) {
	requireProblems(t, "catalogue", pluralPairsProblems(bundles))
	requireProblems(t, "broken", pluralPairsProblems([]bundle{{
		en: map[string]string{"a.n.one": "%d x", "a.m.other": "%d x"},
		id: map[string]string{"a.n.one": "%d x", "a.m.other": "%d x"},
	}}), `"a.n.one" has no .other form`, `"a.m.other" has no .one form`)
}

// --- templates ---

func templateFiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := fs.ReadFile(templateFS, p)
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimPrefix(p, "templates/")] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no templates")
	}
	return out
}

// keyFuncsProblems parses src and reports every t, tn or th call whose first
// argument is not a string literal or names no suitable catalogue key.
func keyFuncsProblems(name, src string, has func(key string) bool) []string {
	stub := func(...any) string { return "" }
	tmpl, err := template.New(name).Funcs(template.FuncMap{"t": stub, "tn": stub, "th": stub, "lang": stub, "static": stub, "chars": stub}).Parse(src)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", name, err)}
	}
	var out []string
	var walk func(n parse.Node)
	walk = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ListNode:
			if n != nil {
				for _, c := range n.Nodes {
					walk(c)
				}
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.IfNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.TemplateNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			if n != nil {
				for _, c := range n.Cmds {
					walk(c)
				}
			}
		case *parse.CommandNode:
			for _, a := range n.Args {
				walk(a)
			}
			if len(n.Args) == 0 {
				return
			}
			fn, ok := n.Args[0].(*parse.IdentifierNode)
			if !ok || (fn.Ident != "t" && fn.Ident != "tn" && fn.Ident != "th") {
				return
			}
			if len(n.Args) < 2 {
				out = append(out, fmt.Sprintf("%s: %s without a key", name, n))
				return
			}
			lit, ok := n.Args[1].(*parse.StringNode)
			if !ok {
				out = append(out, fmt.Sprintf("%s: %s: the key is not a string literal", name, n))
				return
			}
			key, html := lit.Text, strings.HasSuffix(lit.Text, "_html")
			switch {
			case fn.Ident == "th" && !html:
				out = append(out, fmt.Sprintf("%s: th %q: not an _html key", name, key))
			case fn.Ident != "th" && html:
				out = append(out, fmt.Sprintf("%s: %s %q: an _html key needs th", name, fn.Ident, key))
			case fn.Ident == "tn" && !(has(key+".one") && has(key+".other")):
				out = append(out, fmt.Sprintf("%s: tn %q: no plural forms", name, key))
			case fn.Ident != "tn" && !has(key):
				out = append(out, fmt.Sprintf("%s: %s %q: no such key", name, fn.Ident, key))
			}
		}
	}
	for _, tt := range tmpl.Templates() {
		if tt.Tree != nil {
			walk(tt.Tree.Root)
		}
	}
	return out
}

func catalogueHas(key string) bool {
	_, ok := lookup(langEN, key)
	return ok
}

func TestTemplates_KeysExist(t *testing.T) {
	for name, src := range templateFiles(t) {
		requireProblems(t, name, keyFuncsProblems(name, src, catalogueHas))
	}
	has := func(k string) bool { return k == "a.b" || k == "a.n.one" || k == "a.n.other" || k == "a.c_html" }
	requireProblems(t, "broken", keyFuncsProblems("x", `{{define "main"}}{{t "a.b"}}{{tn "a.n" 2}}{{th "a.c_html"}}`+
		`{{t "a.nope"}}{{t .X}}{{if .Y}}<p title="{{t (printf "%s" "a.b")}}">{{th "a.b"}}{{end}}{{range .Z}}{{t "a.c_html"}}{{tn "a.m" 1}}{{end}}{{end}}`, has),
		`t "a.nope": no such key`, `t .X: the key is not a string literal`, `the key is not a string literal`,
		`th "a.b": not an _html key`, `t "a.c_html": an _html key needs th`, `tn "a.m": no plural forms`)
	if p := keyFuncsProblems("x", `{{t "a.b"}}{{tn "a.n" 1}}{{th "a.c_html" 3}}`, has); len(p) > 0 {
		t.Errorf("good calls refused: %q", p)
	}
}

var (
	templateCommentRe = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	templateActionRe  = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	markupRe          = regexp.MustCompile(`(?s)<!--.*?-->|<[^>]*>`)
	tagNameRe         = regexp.MustCompile(`^</?([a-zA-Z][a-zA-Z0-9-]*)`)
	attrRe            = regexp.MustCompile(`([^\s=/<>"']+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	allowedWordRe     = regexp.MustCompile(`\b(?:` + strings.Join(literalAllowed, "|") + `)\b`)
)

func hasLetter(s string) bool { return strings.IndexFunc(s, unicode.IsLetter) >= 0 }

// literalTextProblems reports the words a template holds outside its actions
// (PLAN 2.3): text outside <code> and <pre> other than the allowed words,
// letters in a literal aria-label, placeholder, title or alt, and a space or
// upper-case letter in a literal data-* value.
func literalTextProblems(name, src string) []string {
	src = templateCommentRe.ReplaceAllString(src, "")
	src = templateActionRe.ReplaceAllString(src, "\uFFFC")
	var out []string
	inCode := 0
	checkText := func(text string) {
		if inCode > 0 {
			return
		}
		text = allowedWordRe.ReplaceAllString(html.UnescapeString(text), "")
		if hasLetter(text) {
			out = append(out, fmt.Sprintf("%s: literal text %q", name, strings.TrimSpace(text)))
		}
	}
	last := 0
	for _, loc := range markupRe.FindAllStringIndex(src, -1) {
		checkText(src[last:loc[0]])
		last = loc[1]
		tag := src[loc[0]:loc[1]]
		if strings.HasPrefix(tag, "<!") {
			continue
		}
		if m := tagNameRe.FindStringSubmatch(tag); m != nil {
			if el := strings.ToLower(m[1]); el == "code" || el == "pre" {
				if strings.HasPrefix(tag, "</") {
					inCode--
				} else {
					inCode++
				}
			}
		}
		for _, a := range attrRe.FindAllStringSubmatch(tag, -1) {
			attr, value := strings.ToLower(a[1]), a[2]+a[3]+a[4]
			value = strings.ReplaceAll(value, "\uFFFC", "")
			switch {
			case attr == "aria-label" || attr == "placeholder" || attr == "title" || attr == "alt":
				if hasLetter(html.UnescapeString(value)) {
					out = append(out, fmt.Sprintf("%s: literal %s %q", name, attr, value))
				}
			case strings.HasPrefix(attr, "data-"):
				if strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsUpper(r) }) {
					out = append(out, fmt.Sprintf("%s: literal %s %q", name, attr, value))
				}
			}
		}
	}
	checkText(src[last:])
	return out
}

func TestTemplates_NoLiteralText(t *testing.T) {
	checked := 0
	for name, src := range templateFiles(t) {
		if slices.Contains(pendingTemplates, name) {
			continue
		}
		checked++
		requireProblems(t, name, literalTextProblems(name, src))
	}
	if checked < 4 {
		t.Errorf("checked %d templates, want at least base, brand, error and section_head", checked)
	}
	requireProblems(t, "broken", literalTextProblems("x", `<p>Hello</p><p title="Close">{{t "a.b"}}</p>`+
		`<input placeholder="Your name" aria-label="{{t "a.b"}}"><img alt="Logo" src="x"><div data-state="Saved now"></div>`+
		`<span data-kind="Big">{{.X}}</span> after`),
		`literal text "Hello"`, `literal title "Close"`, `literal placeholder "Your name"`, `literal alt "Logo"`,
		`literal data-state "Saved now"`, `literal data-kind "Big"`, `literal text "after"`)
	requireProblems(t, "good", literalTextProblems("x", `{{/* a comment with words */}}<!doctype html><html lang="{{lang}}">`+
		`<p>{{t "a.b"}} · {{.N}} &middot; 12</p><p><code>shuffle: none</code></p><pre>## Bagian
1. Soal</pre>`+
		`<span class="wordmark">Answerbox</span><button value="id">Indonesia</button><span>A</span>`+
		`<div data-state="idle" data-x="{{t "a.b"}}" aria-label="{{t "a.b"}}"></div>`))
}

// --- Go source ---

// goKeysProblems reports every call of a method named T or N in files whose
// first argument is a string literal naming no catalogue key, or is not a
// literal outside the computedKeyFuncs.
func goKeysProblems(fset *token.FileSet, files []*ast.File, has func(key string) bool) []string {
	var out []string
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "T" && sel.Sel.Name != "N") {
					return true
				}
				pos := fset.Position(call.Pos())
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					if !slices.Contains(computedKeyFuncs, fd.Name.Name) {
						out = append(out, fmt.Sprintf("%s: %s: %s with a computed key outside computedKeyFuncs", pos, fd.Name.Name, sel.Sel.Name))
					}
					return true
				}
				key, err := strconv.Unquote(lit.Value)
				if err != nil {
					out = append(out, fmt.Sprintf("%s: %v", pos, err))
					return true
				}
				if sel.Sel.Name == "N" && !(has(key+".one") && has(key+".other")) || sel.Sel.Name == "T" && !has(key) {
					out = append(out, fmt.Sprintf("%s: %s %q: no such key", pos, sel.Sel.Name, key))
				}
				return true
			})
		}
	}
	return out
}

func TestGo_KeysExist(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) < 10 {
		t.Fatalf("parsed %d files", len(files))
	}
	requireProblems(t, "internal/web", goKeysProblems(fset, files, catalogueHas))

	src := `package x
func handler(l localizer, k string) {
	l.T("a.b")
	l.N("a.n", 2)
	l.T("a.nope")
	l.N("a.b", 1)
	l.T(k)
}
func Date(l localizer, k string) string { return l.T("a." + k) }
`
	f, err := parser.ParseFile(fset, "broken.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	has := func(k string) bool { return k == "a.b" || k == "a.n.one" || k == "a.n.other" }
	got := goKeysProblems(fset, []*ast.File{f}, has)
	requireProblems(t, "broken", got, `T "a.nope": no such key`, `N "a.b": no such key`, `handler: T with a computed key`)
	if len(got) != 3 {
		t.Errorf("broken: want 3 problems, got %q", got)
	}
}

// --- localizer and template functions ---

func TestLocalizer(t *testing.T) {
	useCatalogue(t,
		map[string]string{"a.pct": "100% of %s", "a.name": "Hello %s", "a.n.one": "%d attempt", "a.n.other": "%d attempts", "a.of.one": "%[1]d of %[2]s", "a.of.other": "%[1]d of %[2]s, many"},
		map[string]string{"a.pct": "100% dari %s", "a.name": "Halo %s", "a.n.one": "%d pengerjaan", "a.n.other": "%d pengerjaan", "a.of.one": "%[1]d dari %[2]s", "a.of.other": "%[1]d dari %[2]s"})
	en, id := localizer{langEN}, localizer{langID}
	for _, tc := range []struct{ got, want string }{
		{en.T("a.nope"), "a.nope"},
		{en.T("a.pct"), "100% of %s"},
		{id.T("a.name", "Sari"), "Halo Sari"},
		{en.N("a.n", 1), "1 attempt"},
		{en.N("a.n", 0), "0 attempts"},
		{en.N("a.n", 2), "2 attempts"},
		{id.N("a.n", 1), "1 pengerjaan"},
		{en.N("a.of", 3, "x"), "3 of x, many"},
		{en.T("a.name", "<b>"), "Hello <b>"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}

	var fake fatalRecorder
	enText(&fake, "a.nope")
	if !fake.failed {
		t.Error("enText accepted an unknown key")
	}
	if got := enText(t, "a.name"); got != "Hello %s" {
		t.Errorf("enText = %q", got)
	}
}

func execFuncs(t *testing.T, l lang, src string, data any) (string, error) {
	t.Helper()
	tmpl, err := htmltemplate.New("x").Funcs(langFuncs(l)).Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	return buf.String(), err
}

func TestTemplateFuncs(t *testing.T) {
	useCatalogue(t,
		map[string]string{
			"a.pick": "✗ Student's pick", "a.amp": "a & b <c>", "a.arg": "%s", "a.count": "%d left",
			"a.dont": "don't", "a.n.one": "%d try", "a.n.other": "%d tries", "a.em_html": "<em>%s</em> & co",
		},
		map[string]string{
			"a.pick": "✗ Pilihan siswa", "a.amp": "a & b <c>", "a.arg": "%s", "a.count": "sisa %d",
			"a.dont": "jangan", "a.n.one": "%d kali", "a.n.other": "%d kali", "a.em_html": "<em>%s</em> & kawan",
		})
	for _, src := range []string{`{{t "a.em_html"}}`, `{{t "a.nope"}}`, `{{th "a.pick"}}`, `{{th "a.nope_html"}}`, `{{tn "a.nope" 2}}`} {
		if _, err := execFuncs(t, langEN, src, nil); err == nil {
			t.Errorf("%s: no error", src)
		}
	}

	hostile := "<b>Tom's 1+1</b>"
	plain, err := execFuncs(t, langEN, `{{.}}`, hostile)
	if err != nil {
		t.Fatal(err)
	}
	const escaped = "&lt;b&gt;Tom&#39;s 1&#43;1&lt;/b&gt;"
	if plain != escaped {
		t.Fatalf("{{.}} = %q, the reference changed", plain)
	}
	for _, tc := range []struct {
		l    lang
		src  string
		data any
		want string
	}{
		{langEN, `<span>{{t "a.pick"}}</span>`, nil, "<span>✗ Student's pick</span>"},
		{langID, `<span>{{t "a.pick"}}</span>`, nil, "<span>✗ Pilihan siswa</span>"},
		{langEN, `<p>{{t "a.amp"}}</p>`, nil, "<p>a &amp; b &lt;c&gt;</p>"},
		{langEN, `<p>{{t "a.arg" .}}</p>`, hostile, "<p>" + escaped + "</p>"},
		{langEN, `<p>{{th "a.em_html" .}}</p>`, hostile, "<p><em>" + escaped + "</em> & co</p>"},
		{langID, `<p>{{th "a.em_html" .}}</p>`, hostile, "<p><em>" + escaped + "</em> & kawan</p>"},
		{langEN, `<p>{{t "a.count" .}}</p>`, 3, "<p>3 left</p>"},
		{langID, `<p>{{t "a.count" .}}</p>`, int64(7), "<p>sisa 7</p>"},
		{langEN, `<p>{{tn "a.n" 1}}</p><p>{{tn "a.n" 2}}</p>`, nil, "<p>1 try</p><p>2 tries</p>"},
		{langEN, `<p>{{t "a.arg" .}}</p>`, htmltemplate.HTML("<i>x</i>"), "<p>&lt;i&gt;x&lt;/i&gt;</p>"},
		{langEN, `<p>{{t "a.arg" .}}</p>`, fmt.Errorf("<x>"), "<p>&lt;x&gt;</p>"},
		{langEN, `<p title="{{t "a.dont"}}"></p>`, nil, `<p title="don&#39;t"></p>`},
		{langEN, `<p title="{{t "a.arg" .}}"></p>`, `a" onclick="x`, `<p title="a&#34; onclick=&#34;x"></p>`},
		{langEN, `<html lang="{{lang}}">`, nil, `<html lang="en">`},
		{langID, `<html lang="{{lang}}">`, nil, `<html lang="id">`},
	} {
		got, err := execFuncs(t, tc.l, tc.src, tc.data)
		if err != nil {
			t.Errorf("%s %s: %v", tc.l, tc.src, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %s with %v = %q, want %q", tc.l, tc.src, tc.data, got, tc.want)
		}
	}
}
