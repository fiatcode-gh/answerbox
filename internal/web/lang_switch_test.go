package web

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	langSwitchForm = regexp.MustCompile(`<details class="lang-switch"[^>]*>.*?</details>`)
	langSwitchNext = regexp.MustCompile(`<input type="hidden" name="next" value="([^"]*)">`)
	// langSwitchLine is the switch with its line end.
	langSwitchLine = regexp.MustCompile(`<details class="lang-switch"[^\n]*?</details>\n`)
)

// wantLangSwitch is the switch line of PLAN 2.5, a menu as amended in PLAN
// section 7, for a page in l, with its next value replaced by NEXT.
func wantLangSwitch(l lang) string {
	label := map[lang]string{langEN: "Language", langID: "Bahasa"}[l]
	current := func(b lang) string {
		if b == l {
			return ` aria-current="true"`
		}
		return ""
	}
	return `<details class="lang-switch" id="lang-switch"><summary title="Bahasa / Language" aria-label="Bahasa / Language"></summary>` +
		`<form method="post" action="/lang" aria-label="` + label + `"><input type="hidden" name="next" value="NEXT">` +
		`<button type="submit" class="quiet" name="lang" value="id" lang="id"` + current(langID) + `>Indonesia</button>` +
		`<button type="submit" class="quiet" name="lang" value="en" lang="en"` + current(langEN) + `>English</button></form></details>`
}

func isHTMLPage(body string) bool {
	return strings.HasPrefix(body, "<!doctype html>")
}

// switchNext returns the decoded next value of the page's switch.
func switchNext(t *testing.T, name, body string) string {
	t.Helper()
	m := langSwitchNext.FindStringSubmatch(langSwitchForm.FindString(body))
	if m == nil {
		t.Fatalf("%s: no switch with a next field", name)
	}
	return html.UnescapeString(m[1])
}

// pageMain returns the page's <main>…</main>.
func pageMain(t *testing.T, body string) string {
	t.Helper()
	start, end := strings.Index(body, "<main"), strings.LastIndex(body, "</main>")
	if start < 0 || end < start {
		t.Fatalf("page has no <main>…</main>:\n%s", body)
	}
	return body[start : end+len("</main>")]
}

// withoutLangSwitch removes the page's one switch line, for tests that
// compare two pages whose switches return to different addresses. It fails
// unless the page holds exactly one switch, so it hides nothing else.
func withoutLangSwitch(t *testing.T, body string) string {
	t.Helper()
	locs := langSwitchLine.FindAllStringIndex(body, -1)
	if len(locs) != 1 || strings.Count(body, `class="lang-switch"`) != 1 {
		t.Fatalf("page holds %d switch forms, want 1:\n%s", len(locs), body)
	}
	return body[:locs[0][0]] + body[locs[0][1]:]
}

func TestLangSwitch_OnEveryPage(t *testing.T) {
	env, cases := pageCases(t)
	for _, c := range cases {
		for _, l := range []lang{langEN, langID} {
			body := env.fetch(t, c, l)
			if !isHTMLPage(body) {
				continue
			}
			if n := strings.Count(body, `id="lang-switch"`); n != 1 {
				t.Errorf("%s (%s): %d switches, want 1", c.name, l, n)
				continue
			}
			loc := langSwitchForm.FindStringIndex(body)
			if loc == nil {
				t.Errorf("%s (%s): no switch form", c.name, l)
				continue
			}
			mainStart, mainEnd := strings.Index(body, "<main"), strings.LastIndex(body, "</main>")
			if mainStart < 0 || mainEnd < 0 || (loc[0] > mainStart && loc[0] < mainEnd) {
				t.Errorf("%s (%s): switch at %d is not outside <main> (%d..%d)", c.name, l, loc[0], mainStart, mainEnd)
			}
			form := langSwitchNext.ReplaceAllString(body[loc[0]:loc[1]], `<input type="hidden" name="next" value="NEXT">`)
			if want := wantLangSwitch(l); form != want {
				t.Errorf("%s (%s): switch\n got %s\nwant %s", c.name, l, form, want)
			}
		}
	}
}

// requireLangCookie checks rec sets exactly one cookie, the language
// cookie of PLAN 2.2 with value l.
func requireLangCookie(t *testing.T, rec interface{ Result() *http.Response }, l lang) {
	t.Helper()
	lines := rec.Result().Header.Values("Set-Cookie")
	if len(lines) != 1 {
		t.Fatalf("Set-Cookie = %q, want one line", lines)
	}
	c, err := http.ParseSetCookie(lines[0])
	if err != nil {
		t.Fatalf("Set-Cookie %q: %v", lines[0], err)
	}
	if c.Name != langCookieName || c.Value != string(l) || c.Path != "/" || c.MaxAge != 31536000 ||
		!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Domain != "" || !c.Expires.IsZero() {
		t.Errorf("Set-Cookie = %q, want %s=%s; Path=/; Max-Age=31536000; HttpOnly; Secure; SameSite=Lax", lines[0], langCookieName, l)
	}
	for _, attr := range []string{"Max-Age=31536000", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(lines[0], attr) {
			t.Errorf("Set-Cookie %q lacks %s", lines[0], attr)
		}
	}
}

func TestLangSwitch_SetsCookieAndReturns(t *testing.T) {
	env := newTestEnv(t)
	for _, c := range []struct {
		l        lang
		htmlLang string
	}{{langID, `<html lang="id">`}, {langEN, `<html lang="en">`}} {
		rec := postForm(env.h, "/lang", url.Values{"lang": {string(c.l)}, "next": {"/admin/exams?archived=1"}}, nil)
		requireRedirect(t, rec, "/admin/exams?archived=1")
		requireLangCookie(t, rec, c.l)

		// The other language as Accept-Language: the cookie wins.
		other := map[lang]string{langID: "en", langEN: "id"}[c.l]
		page := do(env.h, "GET", "/", http.Header{"Cookie": {langCookieName + "=" + string(c.l)}, "Accept-Language": {other}})
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), c.htmlLang) {
			t.Errorf("GET / with the %s cookie = %d, lacks %s", c.l, page.Code, c.htmlLang)
		}
	}
}

func requireNoSetCookie(t *testing.T, what string, rec interface{ Result() *http.Response }) {
	t.Helper()
	if lines := rec.Result().Header.Values("Set-Cookie"); len(lines) != 0 {
		t.Errorf("%s: Set-Cookie %q, want none", what, lines)
	}
}

func TestLangSwitch_BadInput(t *testing.T) {
	env := newTestEnv(t)
	for _, form := range []url.Values{
		{"next": {"/"}},
		{"lang": {""}, "next": {"/"}},
		{"lang": {"ID"}, "next": {"/"}},
		{"lang": {"fr"}, "next": {"/"}},
		{"lang": {"id,en"}, "next": {"/"}},
	} {
		rec := postForm(env.h, "/lang", form, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `id="error"`) {
			t.Errorf("POST /lang %s = %d, want the 400 page:\n%s", form.Encode(), rec.Code, rec.Body.String())
		}
		requireNoSetCookie(t, form.Encode(), rec)
	}

	rec := postForm(env.h, "/lang", url.Values{"lang": {"id"}, "next": {"/" + strings.Repeat("a", 4<<10)}}, nil)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `id="error"`) {
		t.Errorf("POST /lang over 4 KiB = %d, want the 413 page", rec.Code)
	}
	requireNoSetCookie(t, "over 4 KiB", rec)
}

func TestLangSwitch_SafeReturn(t *testing.T) {
	for _, keep := range []string{
		"/",
		"/admin/exams/3/attempts?sort=name&dir=asc&name=a%20b",
		"/x#q3",
	} {
		if got := safeReturn(keep); got != keep {
			t.Errorf("safeReturn(%q) = %q, want it unchanged", keep, got)
		}
	}
	for _, refuse := range []string{
		"",
		"admin",
		"//evil.test",
		`/\evil.test`,
		`/a\b`,
		"https://evil.test/",
		"javascript:alert(1)",
		"/\t/evil.test",
		"/a\r\nSet-Cookie: x=1",
		"/a\x7f",
		"%2F%2Fevil.test",
		"http:/evil.test",
	} {
		if got := safeReturn(refuse); got != "/" {
			t.Errorf("safeReturn(%q) = %q, want /", refuse, got)
		}
	}

	env := newTestEnv(t)
	rec := postForm(env.h, "/lang", url.Values{"lang": {"id"}, "next": {"//evil.test"}}, nil)
	requireRedirect(t, rec, "/")
	rec = postForm(env.h, "/lang", url.Values{"lang": {"en"}}, nil)
	requireRedirect(t, rec, "/")
}

func TestLangSwitch_CrossSiteRefused(t *testing.T) {
	env := newTestEnv(t)
	rec := postForm(env.h, "/lang", url.Values{"lang": {"id"}, "next": {"/"}}, http.Header{"Sec-Fetch-Site": {"cross-site"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `id="error"`) {
		t.Errorf("cross-site POST /lang = %d, want the 403 page", rec.Code)
	}
	requireNoSetCookie(t, "cross-site", rec)
}

func TestLangSwitch_ReturnOfEveryPage(t *testing.T) {
	env, cases := pageCases(t)
	for _, c := range cases {
		body := env.fetch(t, c, langEN)
		if !isHTMLPage(body) {
			continue
		}
		next := switchNext(t, c.name, body)
		header := http.Header{"Cookie": {langCookieName + "=en"}}
		for _, v := range c.header.Values("Cookie") {
			header.Add("Cookie", v)
		}
		rec := do(env.h, "GET", next, header)
		if c.method == "GET" {
			if next != c.path {
				t.Errorf("%s: next = %q, want %q", c.name, next, c.path)
			}
			if rec.Code != c.status {
				t.Errorf("%s: GET %s = %d, want %d", c.name, next, rec.Code, c.status)
			}
			continue
		}
		if rec.Code != http.StatusOK || !isHTMLPage(rec.Body.String()) {
			t.Errorf("%s: %s %s returns to %q, which answers %d", c.name, c.method, c.path, next, rec.Code)
		}
	}
}

// postReturns lists the next value of each POST case whose page has a GET
// page of its own elsewhere, so a regression to the POST path is caught even
// when that path happened to answer GET.
func TestLangSwitch_ReturnOfPostPages(t *testing.T) {
	env, cases := pageCases(t)
	want := map[string]*regexp.Regexp{
		"landing-start-refused-name": regexp.MustCompile(`^/$`),
		"landing-start-refused-code": regexp.MustCompile(`^/$`),
		"landing-resume-refused":     regexp.MustCompile(`^/$`),
		"error-403-csrf":             regexp.MustCompile(`^/admin/exams$`),
		"error-403-cross-site":       regexp.MustCompile(`^/$`),
		"exam-schedule-refused":      regexp.MustCompile(`^/admin/exams/\d+$`),
		"exam-replace-frozen":        regexp.MustCompile(`^/admin/exams/\d+$`),
		"attempt-rename-refused":     regexp.MustCompile(`^/admin/exams/\d+/attempts/\d+$`),
		"attempt-override-refused":   regexp.MustCompile(`^/admin/exams/\d+/attempts/\d+$`),
		"editor-move-stale":          regexp.MustCompile(`^/admin/exams/\d+/edit$`),
	}
	seen := 0
	for _, c := range cases {
		re, ok := want[c.name]
		if !ok {
			continue
		}
		seen++
		if next := switchNext(t, c.name, env.fetch(t, c, langEN)); !re.MatchString(next) {
			t.Errorf("%s: next = %q, want %s", c.name, next, re)
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of the %d named POST cases", seen, len(want))
	}

	// A refusal that names the exam (it is not open yet) returns to its
	// code link.
	if got := (landingData{ExamTitle: "Ujian", Code: "AB CD&x"}).returnPath(); got != "/?code=AB+CD%26x" {
		t.Errorf("landing with an exam: returnPath = %q", got)
	}
}

func dbRowCounts(t *testing.T, env *testEnv) map[string]int {
	t.Helper()
	rows, err := env.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, n := range names {
		var c int
		if err := env.db.QueryRow(`SELECT count(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		counts[n] = c
	}
	return counts
}

func TestLangSwitch_ChangesNothingElse(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	student := env.start(t, "Ada")
	teacher, _, _ := env.signIn(t)
	before := dbRowCounts(t, env)
	if len(before) == 0 {
		t.Fatal("no tables")
	}
	for i := range 10 {
		header := http.Header{}
		for _, v := range slices.Concat(student.Values("Cookie"), teacher.Values("Cookie")) {
			header.Add("Cookie", v)
		}
		l := []lang{langID, langEN}[i%2]
		rec := postForm(env.h, "/lang", url.Values{"lang": {string(l)}, "next": {"/exam"}}, header)
		requireRedirect(t, rec, "/exam")
		requireLangCookie(t, rec, l)
	}
	after := dbRowCounts(t, env)
	for name, n := range before {
		if after[name] != n {
			t.Errorf("table %s: %d rows before, %d after", name, n, after[name])
		}
	}
	if len(after) != len(before) {
		t.Errorf("tables before %v, after %v", before, after)
	}
}

func TestLangSwitch_GetIsNotARoute(t *testing.T) {
	env := newTestEnv(t)
	rec := do(env.h, "GET", "/lang?lang=id&next=/", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `id="error"`) {
		t.Errorf("GET /lang = %d, want the 404 page", rec.Code)
	}
	requireNoSetCookie(t, "GET /lang", rec)
}
