package web

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// wordedAttrs are the attributes whose value is wording: it differs between
// the languages and is read as text.
var wordedAttrs = []string{
	"aria-label", "placeholder", "title", "alt",
	"data-label", "data-done", "data-failed", "data-live-text", "data-stale-text",
	"data-refuse-letters", "data-refuse-digits", "data-refuse-other", "data-refuse-link",
}

// pageAttrRe matches one attribute of a tag, with or without a value.
var pageAttrRe = regexp.MustCompile(`\s+([^\s=/>"']+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+)))?`)

type pageAttr struct {
	name, value string
	valued      bool
}

// pageTag is one tag of a page: its name ("/p" for a closing tag) and its
// attributes in source order.
type pageTag struct {
	name  string
	attrs []pageAttr
}

func (t pageTag) attr(name string) string {
	for _, a := range t.attrs {
		if a.name == name {
			return a.value
		}
	}
	return ""
}

// worded tells whether the value of attribute a of tag t is wording.
func (t pageTag) worded(a pageAttr) bool {
	return slices.Contains(wordedAttrs, a.name) ||
		t.name == "html" && a.name == "lang" ||
		t.attr("id") == "exam-text" && strings.HasPrefix(a.name, "data-")
}

// pageTags lists the tags of body, comments left out.
func pageTags(body string) []pageTag {
	var out []pageTag
	for _, raw := range markupRe.FindAllString(body, -1) {
		if strings.HasPrefix(raw, "<!--") {
			continue
		}
		m := tagNameRe.FindStringSubmatch(raw)
		if m == nil {
			out = append(out, pageTag{name: raw})
			continue
		}
		tag := pageTag{name: strings.ToLower(m[1])}
		if strings.HasPrefix(raw, "</") {
			tag.name = "/" + tag.name
		}
		for _, a := range pageAttrRe.FindAllStringSubmatch(strings.TrimSuffix(raw[len(m[0]):], ">"), -1) {
			tag.attrs = append(tag.attrs, pageAttr{name: strings.ToLower(a[1]), value: a[2] + a[3] + a[4], valued: strings.Contains(a[0], "=")})
		}
		out = append(out, tag)
	}
	return out
}

// skeleton is body without its wording: every tag with every attribute,
// worded values blanked, and the aria-current that marks the switch's
// current language left out.
func skeleton(body string) []string {
	var out []string
	for _, tag := range pageTags(body) {
		var b strings.Builder
		b.WriteString("<" + tag.name)
		for _, a := range tag.attrs {
			if tag.name == "button" && tag.attr("name") == "lang" && a.name == "aria-current" {
				continue
			}
			b.WriteString(" " + a.name)
			switch {
			case !a.valued:
			case tag.worded(a):
				b.WriteString(`="…"`)
			default:
				b.WriteString(`="` + a.value + `"`)
			}
		}
		out = append(out, b.String()+">")
	}
	return out
}

// skeletonProblems reports where the skeletons of en and id part.
func skeletonProblems(name, en, id string) []string {
	se, si := skeleton(en), skeleton(id)
	for i := range max(len(se), len(si)) {
		var e, d string
		if i < len(se) {
			e = se[i]
		}
		if i < len(si) {
			d = si[i]
		}
		if e != d {
			return []string{fmt.Sprintf("%s: tag %d differs:\n  en %s\n  id %s", name, i, e, d)}
		}
	}
	return nil
}

func TestPages_SameSkeleton(t *testing.T) {
	env, cases := pageCases(t)
	for _, c := range cases {
		requireProblems(t, c.name, skeletonProblems(c.name, env.fetch(t, c, langEN), env.fetch(t, c, langID)))
	}

	for _, bad := range []struct{ en, id string }{
		{`<p class="a">x</p>`, `<p class="b">x</p>`},
		{`<p id="a">x</p>`, `<p id="b">x</p>`},
		{`<form action="/a">`, `<form action="/b">`},
		{`<input type="hidden" name="a" value="1">`, `<input type="hidden" name="a" value="2">`},
		{`<input type="hidden" name="a">`, `<input type="hidden" name="b">`},
		{`<input name="a" required>`, `<input name="a">`},
		{`<div data-state="idle">`, `<div data-state="diam">`},
		{`<p>x</p><input>`, `<p>x</p>`},
		{`<p><strong>x</strong> y</p>`, `<p>y <em>x</em></p>`},
		{`<button name="other" aria-current="true">`, `<button name="other">`},
		{`<div id="other" data-saved="Saved">`, `<div id="other" data-saved="Tersimpan">`},
	} {
		if len(skeletonProblems("x", bad.en, bad.id)) == 0 {
			t.Errorf("bad sample passed: en %s, id %s", bad.en, bad.id)
		}
	}
	requireProblems(t, "good", skeletonProblems("good",
		`<!doctype html><html lang="en"><title>Exam</title><p class="a" title="Close" aria-label="Close">Hello <!-- x --></p><input placeholder="Your name" required>`+
			`<div id="exam-text" hidden data-saved="Saved"></div><button name="lang" value="id"></button><button name="lang" value="en" aria-current="true">English</button>`+
			`<pre id="format-example"># Title</pre><img alt="Logo"><span data-label="Copy" data-refuse-link="No link"></span>`,
		`<!doctype html><html lang="id"><title>Ujian</title><p class="a" title="Tutup" aria-label="Tutup">Halo</p><input placeholder="Namamu" required>`+
			`<div id="exam-text" hidden data-saved="Tersimpan"></div><button name="lang" value="id" aria-current="true"></button><button name="lang" value="en">English</button>`+
			`<pre id="format-example"># Judul</pre><img alt="Logo"><span data-label="Salin" data-refuse-link="Bukan link"></span>`))
}

// englishSentinels are words no Indonesian page may hold.
var englishSentinels = []string{"Exam", "Question", "Answer", "Save", "Saved", "Start", "Submit", "Open", "Close", "Results", "Settings", "Remove", "Cancel", "Teacher", "Student", "Score"}

// englishAllowed are the words an Indonesian page may hold although they are
// English, each with the reason (PLAN 2.12).
var englishAllowed = map[string]string{
	"Bahasa / Language": "the switch's bilingual name, the same in every language (PLAN 7)",
	".zip":              "a file extension, never translated (annex 00 section 2)",
	`"shuffle:"`:        "an exam-file keyword quoted in a file-rule message (PLAN 2.12)",
	`"monitor:"`:        "an exam-file keyword quoted in a file-rule message (PLAN 2.12)",
}

var (
	pageCodeRe     = regexp.MustCompile(`(?is)<code\b[^>]*>.*?</code>`)
	pagePreRe      = regexp.MustCompile(`(?is)<pre\b[^>]*>.*?</pre>`)
	pageTextareaRe = regexp.MustCompile(`(?is)<textarea\b[^>]*>.*?</textarea>`)
	fixtureMarkRe  = regexp.MustCompile(`^(#+ |\d+\. |[-=] )|\s*\*$`)
	formatVerbRe   = regexp.MustCompile(`%(\[\d+\])?[a-z]|\{done\}|\{total\}|\{n\}`)
)

// fixtureTexts are the exam content, names and emails of the page fixture,
// longest first: the pages show them as they are.
func fixtureTexts() []string {
	set := map[string]bool{}
	for _, src := range []string{fixtureMath, fixtureLanguage, fixturePhysics, fixtureBroken, fixtureSimple("Ujian Sejarah"), fixtureSimple("Ujian Biologi"), fixtureSimple("Ujian Kimia")} {
		for _, line := range strings.Split(src, "\n") {
			if line = strings.TrimSpace(fixtureMarkRe.ReplaceAllString(line, "")); line != "" {
				set[line] = true
			}
		}
	}
	for _, s := range []string{
		fixtureAdminEmail, fixtureAdminName, fixtureWatiEmail, fixtureWatiName, fixtureJokoEmail, fixtureResumeTyped, fixtureUploadedName,
		"Ujian Fisika Baru", "Jawab dengan singkat.", "Tono", "Sari", "Budi", "Rudi", "Rina", "Batavia", "Bandung", "Jakarta", "bukan surel", "zaki",
	} {
		set[s] = true
	}
	out := slices.Collect(func(yield func(string) bool) {
		for s := range set {
			if !yield(s) {
				return
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) || len(out[i]) == len(out[j]) && out[i] < out[j] })
	return out
}

// pageWording is what a reader of body reads: the text outside tags, <code>,
// <pre> and <textarea>, plus the values of the worded attributes; without the
// fixture's own content.
func pageWording(body string, fixture []string) string {
	var b strings.Builder
	for _, tag := range pageTags(body) {
		for _, a := range tag.attrs {
			if a.valued && tag.worded(a) && !(tag.name == "html" && a.name == "lang") {
				b.WriteString(html.UnescapeString(a.value) + "\n")
			}
		}
	}
	text := pageTextareaRe.ReplaceAllString(pagePreRe.ReplaceAllString(pageCodeRe.ReplaceAllString(body, "\n"), "\n"), "\n")
	b.WriteString(html.UnescapeString(markupRe.ReplaceAllString(text, "\n")))
	out := b.String()
	for _, s := range fixture {
		out = strings.ReplaceAll(out, s, "\n")
	}
	return out
}

// englishFragments are the parts of every English catalogue text that has a
// different Indonesian text, markup and formatting verbs taken out, of four
// or more characters.
func englishFragments(bs []bundle) []string {
	set := map[string]bool{}
	for _, b := range bs {
		for k, en := range b.en {
			if en == b.id[k] {
				continue
			}
			for _, part := range formatVerbRe.Split(markupTagRe.ReplaceAllString(en, "\x00"), -1) {
				for _, frag := range strings.Split(part, "\x00") {
					frag = strings.TrimSpace(frag)
					if utf8.RuneCountInString(frag) >= 4 && hasLetter(frag) {
						set[frag] = true
					}
				}
			}
		}
	}
	return slices.Sorted(func(yield func(string) bool) {
		for s := range set {
			if !yield(s) {
				return
			}
		}
	})
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// containsWord tells whether s holds word with no letter or digit right
// before or after it.
func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if (start == 0 || !isWordRune(before)) && (end == len(s) || !isWordRune(after)) {
			return true
		}
		i = start + 1
	}
}

// englishLeftProblems reports the English catalogue fragments and sentinel
// words left in the wording of an Indonesian page.
func englishLeftProblems(name, wording string, fragments []string, allowed map[string]string) []string {
	for w := range allowed {
		wording = strings.ReplaceAll(wording, w, " ")
	}
	var out []string
	for _, f := range fragments {
		if containsWord(wording, f) {
			out = append(out, fmt.Sprintf("%s: English text %q", name, f))
		}
	}
	for _, w := range englishSentinels {
		if containsWord(wording, w) {
			out = append(out, fmt.Sprintf("%s: English word %q", name, w))
		}
	}
	return out
}

func TestIndonesianPages_NoEnglishLeft(t *testing.T) {
	env, cases := pageCases(t)
	fixture, fragments := fixtureTexts(), englishFragments(bundles)
	for _, c := range cases {
		requireProblems(t, c.name, englishLeftProblems(c.name, pageWording(env.fetch(t, c, langID), fixture), fragments, englishAllowed))
	}

	bs := []bundle{{
		en: map[string]string{"a.open": "Open the exam", "a.count": "%[1]d of %[2]d answered", "a.em_html": "<em>Careful</em> now", "a.same": "Email", "a.short": "Go"},
		id: map[string]string{"a.open": "Buka ujian", "a.count": "%[1]d dari %[2]d terjawab", "a.em_html": "<em>Hati-hati</em> ya", "a.same": "Email", "a.short": "Ke"},
	}}
	frags := englishFragments(bs)
	if want := []string{"Careful", "Open the exam", "answered"}; !slices.Equal(frags, want) {
		t.Errorf("fragments = %q, want %q", frags, want)
	}
	page := `<html lang="id"><title>Open the exam</title><p>3 dari 4 answered</p><p title="Careful">x</p>` +
		`<div id="exam-text" data-y="Submit"></div><span data-label="Close"></span><p>Score</p>` +
		`<p><code>Start</code></p><pre>Save</pre><textarea>Results</textarea><p>Email · Google · Opened · Tono Settings</p>`
	got := englishLeftProblems("x", pageWording(page, []string{"Tono Settings"}), frags, map[string]string{"Google": "a name"})
	requireProblems(t, "broken", got, `English text "Open the exam"`, `English text "answered"`, `English text "Careful"`,
		`English word "Submit"`, `English word "Close"`, `English word "Score"`, `English word "Open"`)
	for _, w := range []string{"Start", "Save", "Results", "Settings", "Email", "Google"} {
		if slices.ContainsFunc(got, func(p string) bool { return strings.HasSuffix(p, `"`+w+`"`) }) {
			t.Errorf("broken: %q reported although it is code, a field, fixture content or allowed: %q", w, got)
		}
	}
}

// responseShape is what a response must keep in both languages.
func responseShape(status int, h http.Header) string {
	var cookies []string
	for _, c := range (&http.Response{Header: h}).Cookies() {
		cookies = append(cookies, c.Name)
	}
	slices.Sort(cookies)
	return fmt.Sprintf("status %d, Location %q, Content-Type %q, cookies %q", status, h.Get("Location"), h.Get("Content-Type"), cookies)
}

func TestPages_StatusSameInBothLanguages(t *testing.T) {
	env, cases := pageCases(t)
	for _, c := range cases {
		en, id := env.response(t, c, langEN), env.response(t, c, langID)
		if e, i := responseShape(en.Code, en.Header()), responseShape(id.Code, id.Header()); e != i {
			t.Errorf("%s: en %s; id %s", c.name, e, i)
		}
	}

	base := http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Set-Cookie": {"a=1; Path=/"}}
	for _, bad := range []struct {
		status int
		header http.Header
	}{
		{422, base},
		{200, http.Header{"Content-Type": {"text/csv"}, "Set-Cookie": {"a=1; Path=/"}}},
		{200, http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Set-Cookie": {"b=1; Path=/"}}},
		{200, http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Set-Cookie": {"a=1; Path=/"}, "Location": {"/x"}}},
	} {
		if responseShape(200, base) == responseShape(bad.status, bad.header) {
			t.Errorf("bad sample passed: %d %v", bad.status, bad.header)
		}
	}
	if responseShape(200, base) != responseShape(200, http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Set-Cookie": {"a=2; Path=/; HttpOnly"}}) {
		t.Error("a cookie's value counted")
	}
}
