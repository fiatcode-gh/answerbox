package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
	"github.com/fiatcode-gh/answerbox/internal/oidcfake"
)

// shown is a text as a page writes it, in Indonesian and in English.
type shown struct{ id, en string }

func catalogueText(t *testing.T, l lang, key string) string {
	t.Helper()
	text, ok := lookup(l, key)
	if !ok {
		t.Fatalf("no %s text for %q", l, key)
	}
	return text
}

// tx is key as the template function t writes it with args.
func tx(t *testing.T, key string, args ...any) shown {
	t.Helper()
	write := func(l lang) string {
		return string(formatMarkup(markupEscaper.Replace(catalogueText(t, l, key)), args))
	}
	return shown{write(langID), write(langEN)}
}

// thx is the _html key as th writes it with args.
func thx(t *testing.T, key string, args ...any) shown {
	t.Helper()
	write := func(l lang) string { return string(formatMarkup(catalogueText(t, l, key), args)) }
	return shown{write(langID), write(langEN)}
}

// gox is key worded by a handler with args and written by {{.}}.
func gox(t *testing.T, key string, args ...any) shown {
	t.Helper()
	write := func(l lang) string {
		catalogueText(t, l, key)
		return argEscaper.Replace(localizer{l}.T(key, args...))
	}
	return shown{write(langID), write(langEN)}
}

// in puts both texts into wrap at "{}".
func (s shown) in(wrap string) shown {
	return shown{strings.ReplaceAll(wrap, "{}", s.id), strings.ReplaceAll(wrap, "{}", s.en)}
}

// requireIndonesian checks that the Indonesian body holds every text in
// Indonesian and, where the languages differ, not in English.
func requireIndonesian(t *testing.T, label, body string, texts ...shown) {
	t.Helper()
	for _, s := range texts {
		if !strings.Contains(body, s.id) {
			t.Errorf("%s: missing %q", label, s.id)
		}
		if s.en != s.id && !strings.Contains(s.id, s.en) && strings.Contains(body, s.en) {
			t.Errorf("%s: still shows the English %q", label, s.en)
		}
	}
}

// langHeader is h with the language cookie of l added.
func langHeader(h http.Header, l lang) http.Header {
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}
	out.Add("Cookie", langCookieName+"="+string(l))
	return out
}

// fixturePages fetches the named page-fixture cases in l.
func fixturePages(t *testing.T, l lang, names ...string) map[string]string {
	t.Helper()
	env, cases := pageCases(t)
	out := map[string]string{}
	for _, name := range names {
		i := slices.IndexFunc(cases, func(c pageCase) bool { return c.name == name })
		if i < 0 {
			t.Fatalf("no fixture case %q", name)
		}
		out[name] = env.fetch(t, cases[i], l)
	}
	return out
}

func alertDiv(s shown) shown { return s.in(`<div role="alert" class="alert">{}</div>`) }

func TestLoginPage_Indonesian(t *testing.T) {
	pages := fixturePages(t, langID, "login", "login-not-allowed", "login-failed")
	login := pages["login"]
	requireIndonesian(t, "login", login,
		tx(t, "login.title").in("<title>{} · Answerbox</title>"),
		tx(t, "login.heading").in("<h1>{}</h1>"),
		tx(t, "login.lede").in("<p>{}</p>"),
		tx(t, "login.google").in(`<a id="google-sign-in" class="button" href="/admin/auth/start">{}</a>`),
		tx(t, "login.student_link").in(`<p><a href="/">{}</a></p>`))
	if strings.Contains(login, `role="alert"`) {
		t.Errorf("login page without ?error= shows an alert")
	}
	requireIndonesian(t, "not allowed", pages["login-not-allowed"], alertDiv(gox(t, "login.not_allowed")))
	requireIndonesian(t, "failed", pages["login-failed"], alertDiv(gox(t, "login.failed")))

	env := newTestEnv(t)
	if body := do(env.h, "GET", "/admin/login?error=other", langHeader(nil, langID)).Body.String(); strings.Contains(body, `role="alert"`) {
		t.Errorf("an unknown ?error= word shows an alert")
	}

	signin, cb := env.startAndApprove(t, oidcfake.Identity{Email: testAdminEmail, Name: testAdminName})
	rec := do(env.h, "GET", cb.RequestURI(), langHeader(http.Header{"Cookie": {signinCookieName + "=" + signin}}, langID))
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d", rec.Code)
	}
	requireIndonesian(t, "signed in", rec.Body.String(),
		tx(t, "login.signed_in").in("<title>{} · Answerbox</title>"),
		tx(t, "login.signed_in").in("<h1>{}</h1>"),
		tx(t, "login.continue").in(`<a href="/admin/exams">{}</a>`))
}

var examRowRe = regexp.MustCompile(`<tr data-exam-id="[0-9]+">`)

func TestExamsList_Indonesian(t *testing.T) {
	id := fixturePages(t, langID, "exams", "exams-archived")
	en := fixturePages(t, langEN, "exams", "exams-archived")
	list := id["exams"]
	requireIndonesian(t, "header", list,
		tx(t, "nav.label").in(`<nav class="container" aria-label="{}">`),
		tx(t, "nav.exams").in(` aria-current="page">{}</a>`),
		tx(t, "nav.teachers").in(`>{}</a></li>`),
		tx(t, "nav.logout").in(`<button type="submit" class="quiet">{}</button>`))
	th := `<th scope="col">{}</th>`
	requireIndonesian(t, "exams", list,
		tx(t, "exams.title").in("<title>{} · Answerbox</title>"),
		tx(t, "exams.title").in("<h1>{}</h1>"),
		tx(t, "exams.import_link").in(`href="#import">{}</a>`),
		tx(t, "exams.new").in(`href="/admin/exams/new">{}</a>`),
		tx(t, "exams.col.title").in(th), tx(t, "exams.col.code").in(th), tx(t, "exams.col.owner").in(th),
		tx(t, "exams.col.status").in(th), tx(t, "exams.col.questions").in(th), tx(t, "exams.col.attempts").in(th),
		tx(t, "exams.col.actions").in(th),
		tx(t, "exams.badge.open").in(`<span class="badge badge-ok">{}</span>`),
		tx(t, "exams.badge.closed").in(`<span class="badge badge-muted">{}</span>`),
		tx(t, "exams.open").in(`<button type="submit" class="secondary">{}</button>`),
		tx(t, "exams.close").in(`<button type="submit" class="secondary">{}</button>`),
		tx(t, "exams.results").in(`/attempts">{}</a></td>`),
		tx(t, "common.times_zone", "UTC").in(`<p class="zone-note">{}</p>`),
		tx(t, "exams.import.heading").in("<h2>{}</h2>"),
		tx(t, "exams.import.lede").in(`<p class="import-lede">{}</p>`),
		tx(t, "exams.import.label").in(`<label for="import-file">{}</label>`),
		tx(t, "exams.import.submit").in(`<button type="submit" class="secondary">{}</button>`),
		tx(t, "exams.format.summary").in("<summary>{}</summary>"),
		tx(t, "exams.format.example_lede").in("<p>{}</p>"),
		tx(t, "exams.format_example").in("<pre id=\"format-example\">\n{}</pre>"),
		thx(t, "exams.format.title_html").in("<li>{}</li>"),
		thx(t, "exams.format.settings_html").in("<li>{}</li>"),
		thx(t, "exams.format.numbers_html").in("<li>{}</li>"),
		thx(t, "exams.format.sections_html").in("<li>{}</li>"),
		thx(t, "exams.format.options_html").in("<li>{}</li>"),
		thx(t, "exams.format.correct_html").in("<li>{}</li>"),
		thx(t, "exams.format.markup_html").in("<li>{}</li>"),
		thx(t, "exams.format.images_html").in("<li>{}</li>"),
		tx(t, "exams.format.limit").in("<li>{}</li>"))
	opens := strings.TrimSuffix(catalogueText(t, langID, "exams.opens"), "%s")
	closes := strings.TrimSuffix(catalogueText(t, langID, "exams.closes"), "%s")
	if !strings.Contains(list, `<small class="schedule">`+opens) || !strings.Contains(list, `<small class="schedule">`+closes) {
		t.Errorf("exams list: the schedule times are not in Indonesian")
	}
	requireIndonesian(t, "exams archived", id["exams-archived"],
		tx(t, "exams.badge.archived").in(`<span class="badge badge-archived">{}</span>`),
		tx(t, "exams.hide_archived").in(`<a id="hide-archived" href="/admin/exams">{}</a>`))
	for name := range id {
		if got, want := examRowRe.FindAllString(id[name], -1), examRowRe.FindAllString(en[name], -1); len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("%s: rows %q, English %q", name, got, want)
		}
	}

	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	listIn := func() string {
		rec := do(env.h, "GET", "/admin/exams", langHeader(cookie, langID))
		if rec.Code != http.StatusOK {
			t.Fatalf("exams list = %d", rec.Code)
		}
		return rec.Body.String()
	}
	requireIndonesian(t, "no exams", listIn(), tx(t, "exams.empty").in(`<p class="empty">{}</p>`))
	examID, err := env.exams.Import(context.Background(), env.owner, []byte(fixtureSimple("Ujian Kimia")))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.exams.Archive(context.Background(), env.owner, examID); err != nil {
		t.Fatal(err)
	}
	requireIndonesian(t, "all archived", listIn(),
		tx(t, "exams.all_archived").in(`<p id="all-archived" class="empty">{} <a id="show-archived"`),
		tx(t, "exams.show_archived").in(`<a id="show-archived" href="/admin/exams?archived=1">{}</a>`))
}

// noCorrectSecond is an exam whose question 2 has no correct option.
const noCorrectSecond = "# Kuis\n\n1. Satu?\n- a *\n- b\n\n2. Dua?\n- a\n- b\n"

func fileError(t *testing.T, text string) *examfile.Error {
	t.Helper()
	u, err := examfile.ReadUpload([]byte(text))
	if err == nil {
		_, err = examfile.Parse(u, "/media")
	}
	fe, ok := err.(*examfile.Error)
	if !ok {
		t.Fatalf("%q: error %v is not a file rule", text, err)
	}
	return fe
}

func TestImport_RefusedIndonesian(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	fe := fileError(t, noCorrectSecond)
	if fe.Line == 0 {
		t.Fatalf("the refusal names no line: %+v", fe)
	}
	post := func(l lang) string {
		rec := serve(env.h, uploadRequest(t, "/admin/exams", csrf, "kuis.md", []byte(noCorrectSecond), langHeader(cookie, l)))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: import = %d, want 422", l, rec.Code)
		}
		return rec.Body.String()
	}
	idSentence := localizer{langID}.FileError(fe)
	if !strings.HasPrefix(idSentence, "baris "+strconv.Itoa(fe.Line)+": ") {
		t.Fatalf("Indonesian sentence %q lacks the line", idSentence)
	}
	idAlert := argEscaper.Replace(localizer{langID}.T("exams.import_failed", idSentence))
	if body := post(langID); !strings.Contains(body, `<div role="alert" class="alert">`+idAlert+`</div>`) {
		t.Errorf("Indonesian import refusal lacks %q:\n%s", idAlert, body)
	}
	if idAlert == argEscaper.Replace("Import failed: "+idSentence) {
		t.Errorf("the Indonesian prefix is the English one")
	}
	enAlert := argEscaper.Replace("Import failed: " + fe.Msg)
	if body := post(langEN); !strings.Contains(body, `<div role="alert" class="alert">`+enAlert+`</div>`) {
		t.Errorf("English import refusal lacks %q:\n%s", enAlert, body)
	}
}

func TestFormatExample_Imports(t *testing.T) {
	type shape struct {
		options []int
		correct []int
	}
	shapes := map[lang]shape{}
	for _, l := range []lang{langEN, langID} {
		text := catalogueText(t, l, "exams.format_example")
		u, err := examfile.ReadUpload(zipOf(t, map[string][]byte{"ujian.md": []byte(text), "triangle.png": testPNG(t)}))
		if err != nil {
			t.Fatalf("%s: ReadUpload: %v", l, err)
		}
		ex, err := examfile.Parse(u, "/media")
		if err != nil {
			t.Fatalf("%s: Parse: %v", l, err)
		}
		if ex.Shuffle != examfile.ShuffleQuestionsOptions || ex.ShowResult != examfile.ShowReview || len(ex.Images) != 1 {
			t.Errorf("%s: settings or image differ: %v %v %d images", l, ex.Shuffle, ex.ShowResult, len(ex.Images))
		}
		var s shape
		for _, q := range ex.Questions {
			s.options = append(s.options, len(q.Options))
			for i, o := range q.Options {
				if o.Correct {
					s.correct = append(s.correct, i)
				}
			}
		}
		if len(ex.Questions) != 2 {
			t.Errorf("%s: %d questions, want 2", l, len(ex.Questions))
		}
		shapes[l] = s
	}
	if fmt.Sprint(shapes[langEN]) != fmt.Sprint(shapes[langID]) {
		t.Errorf("examples differ in shape: en %v, id %v", shapes[langEN], shapes[langID])
	}
	if catalogueText(t, langEN, "exams.format_example") == catalogueText(t, langID, "exams.format_example") {
		t.Errorf("the Indonesian example is the English one")
	}
}

func TestLocalizer_ShuffleShowResult(t *testing.T) {
	en, id := localizer{langEN}, localizer{langID}
	for _, c := range []struct {
		v      examfile.Shuffle
		en, id string
	}{
		{examfile.ShuffleNone, "No shuffling", "Tidak diacak"},
		{examfile.ShuffleQuestions, "Questions shuffled", "Soal diacak"},
		{examfile.ShuffleQuestionsOptions, "Questions and options shuffled", "Soal dan pilihan diacak"},
		{"sideways", "", ""},
	} {
		if got := en.shuffle(c.v); got != c.en {
			t.Errorf("en shuffle(%q) = %q, want %q", c.v, got, c.en)
		}
		if got := id.shuffle(c.v); got != c.id {
			t.Errorf("id shuffle(%q) = %q, want %q", c.v, got, c.id)
		}
	}
	for _, c := range []struct {
		v      examfile.ShowResult
		en, id string
	}{
		{examfile.ShowNone, "Nothing", "Tidak ada"},
		{examfile.ShowScore, "Score", "Nilai"},
		{examfile.ShowReview, "Score and review", "Nilai dan pembahasan"},
		{"everything", "", ""},
	} {
		if got := en.showResult(c.v); got != c.en {
			t.Errorf("en showResult(%q) = %q, want %q", c.v, got, c.en)
		}
		if got := id.showResult(c.v); got != c.id {
			t.Errorf("id showResult(%q) = %q, want %q", c.v, got, c.id)
		}
	}
}

func TestExamPage_Indonesian(t *testing.T) {
	env, cases := pageCases(t)
	page := func(name string) string {
		t.Helper()
		i := slices.IndexFunc(cases, func(c pageCase) bool { return c.name == name })
		if i < 0 {
			t.Fatalf("no fixture case %q", name)
		}
		return env.fetch(t, cases[i], langID)
	}
	i := slices.IndexFunc(cases, func(c pageCase) bool { return c.name == "projector-closed" })
	closedPath := strings.TrimSuffix(cases[i].path, "/projector")
	admin := cases[i].header

	open := page("exam-open")
	requireIndonesian(t, "open", open,
		tx(t, "examdetail.back").in(`<a href="/admin/exams">{}</a>`),
		tx(t, "examdetail.badge.open").in(`<span class="badge badge-ok">{}</span>`),
		tx(t, "examdetail.state.label").in(`<h2 class="state-label">{}</h2>`),
		tx(t, "examdetail.state.open").in(`>{}</p>`),
		tx(t, "examdetail.explain.open").in(`<p class="state-explain">{}</p>`),
		tx(t, "examdetail.close_exam").in(`>{}</button>`),
		tx(t, "examdetail.code").in("<h2>{}</h2>"),
		tx(t, "examdetail.student_link").in(`<p class="student-link">{} <a id="student-link"`),
		tx(t, "examdetail.copy.done").in(`data-done="{}"`),
		tx(t, "examdetail.copy.failed").in(`data-failed="{}"`),
		tx(t, "examdetail.copy.button").in(` hidden>{}</button>`),
		tx(t, "examdetail.projector").in(`/projector">{}</a>`),
		tx(t, "examdetail.nav_label").in(`<nav class="exam-links" aria-label="{}">`),
		tx(t, "examdetail.results").in(`/attempts">{}</a>`),
		tx(t, "examdetail.history").in(`/revisions">{}</a>`),
		tx(t, "examdetail.attempts.other", 3).in(`<strong class="count-total">{}</strong>`),
		tx(t, "examdetail.counts.in_progress", 1).in(`<span class="count count-live">{}</span>`),
		tx(t, "examdetail.counts.submitted", 1).in(`<span class="count count-done">{}</span>`),
		tx(t, "examdetail.counts.timed_out", 1).in(`<span class="count count-out">{}</span>`),
		tx(t, "setting.heading").in("<h2>{}</h2>"),
		tx(t, "setting.length").in("<dt>{}</dt>"),
		tx(t, "setting.minutes", 30).in("<dd>{}</dd>"),
		tx(t, "setting.shuffle").in("<dt>{}</dt>"),
		gox(t, "setting.shuffle.none").in("<dd>{}</dd>"),
		tx(t, "setting.result_shown").in("<dt>{}</dt>"),
		gox(t, "setting.show_result.review").in("<dd>{}</dd>"),
		tx(t, "setting.monitor").in("<dt>{}</dt>"),
		tx(t, "setting.monitor_on").in("<dd>{}</dd>"),
		tx(t, "setting.questions").in("<dt>{}</dt>"),
		tx(t, "examdetail.more").in("<h2>{}</h2>"),
		tx(t, "examdetail.download").in(`/download">{}</a>`),
		tx(t, "examdetail.copy_exam").in(`<button type="submit" class="quiet">{}</button>`),
		tx(t, "examdetail.archive").in(`/archive">{}</a>`),
		tx(t, "examdetail.replace.heading").in("<h3>{}</h3>"),
		gox(t, "examdetail.frozen").in(`<p class="frozen-notice">{}</p>`),
		tx(t, "preview.heading").in("<h2>{}</h2>"),
		tx(t, "preview.lede").in(`<p class="preview-lede">{}</p>`),
		tx(t, "preview.question", 1).in(`<p class="eyebrow">{}</p>`),
		tx(t, "preview.correct").in(`<span class="correct-mark">{}</span>`),
		tx(t, "preview.accepted").in(`<p class="accepted-label">{}</p>`))

	closed := do(env.h, "GET", closedPath, langHeader(admin, langID)).Body.String()
	requireIndonesian(t, "closed", closed,
		tx(t, "examdetail.badge.closed").in(`<span class="badge badge-muted">{}</span>`),
		tx(t, "examdetail.state.closed").in(`>{}</p>`),
		tx(t, "examdetail.explain.closed").in(`<p class="state-explain">{}</p>`),
		tx(t, "examdetail.open_exam").in(`<button type="submit">{}</button>`),
		tx(t, "examdetail.join_note").in(`<p class="join-note">{}</p>`),
		tx(t, "examdetail.no_attempts").in(`<p id="attempt-counts" class="attempt-counts">{}</p>`),
		tx(t, "examdetail.edit").in(`/edit">{}</a>`),
		tx(t, "examdetail.schedule.heading").in("<h2>{}</h2>"),
		tx(t, "examdetail.schedule.none").in(`<p id="schedule-times">{}</p>`),
		tx(t, "examdetail.schedule.set").in("<summary>{}</summary>"),
		tx(t, "examdetail.schedule.opens_label").in(`<label for="schedule-opens">{}`+"\n"),
		tx(t, "examdetail.schedule.closes_label").in(`<label for="schedule-closes">{}`+"\n"),
		tx(t, "examdetail.schedule.save").in(`<button type="submit" class="secondary">{}</button>`),
		tx(t, "examdetail.schedule.note", "UTC").in(`<p class="zone-note">{}</p>`),
		tx(t, "examdetail.replace.label").in(`<label for="replace-file">{}</label>`),
		tx(t, "examdetail.replace.submit").in(`<button type="submit" class="secondary">{}</button>`),
		gox(t, "setting.show_result.score").in("<dd>{}</dd>"))

	scheduled := page("exam-scheduled")
	requireIndonesian(t, "scheduled", scheduled,
		tx(t, "examdetail.explain.scheduled").in(`<p class="state-explain">{}</p>`),
		tx(t, "examdetail.schedule.change").in("<summary>{}</summary>"),
		tx(t, "examdetail.opens", "2026-11-16 08:00").in(`<p id="schedule-times">{}`))
	if !strings.Contains(scheduled, " · "+strings.TrimSuffix(catalogueText(t, langID, "examdetail.closes"), "%s")) {
		t.Errorf("scheduled: the close time is not in Indonesian")
	}

	archived := page("exam-archived")
	requireIndonesian(t, "archived", archived,
		tx(t, "examdetail.badge.archived").in(`<span class="badge badge-archived">{}</span>`),
		tx(t, "examdetail.state.archived").in(`>{}</p>`),
		tx(t, "examdetail.explain.archived").in(`<p class="state-explain">{}</p>`),
		tx(t, "examdetail.unarchive").in(`<button type="submit">{}</button>`),
		tx(t, "examdetail.archived_on", localizer{langID}.Date(testStart)).in(`<p id="archived-note" class="frozen-notice">{}</p>`))

	requireIndonesian(t, "schedule refused", page("exam-schedule-refused"), alertDiv(gox(t, "examdetail.schedule.order")))
	requireIndonesian(t, "replace frozen", page("exam-replace-frozen"), alertDiv(gox(t, "examdetail.frozen")))
}

// refusal is a request the teacher pages refuse with status and alert.
type refusal struct {
	name   string
	send   func(l lang) *httptest.ResponseRecorder
	status int
	alert  shown
}

// teacherRefusals are the refusals of the exam pages and Teachers made on a
// fresh state.
func teacherRefusals(t *testing.T) []refusal {
	t.Helper()
	ctx := context.Background()
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	examID, err := env.exams.Import(ctx, env.owner, []byte(fixtureSimple("Ujian Biologi")))
	if err != nil {
		t.Fatal(err)
	}
	archived, err := env.exams.Import(ctx, env.owner, []byte(fixtureSimple("Ujian Kimia")))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.exams.Archive(ctx, env.owner, archived); err != nil {
		t.Fatal(err)
	}
	frozen := env.openExam(t, fixtureSimple("Ujian Fisika"))
	env.startAttempt(t, frozen, "Tono")
	old := env.examWithoutRevision(t, "PQRXYZ", false)
	if err := env.admin.AddTeacher(ctx, fixtureWatiEmail); err != nil {
		t.Fatal(err)
	}
	adminID := env.owner.TeacherID

	type sender = func(lang) *httptest.ResponseRecorder
	form := func(path string, v url.Values) sender {
		return func(l lang) *httptest.ResponseRecorder {
			v.Set("csrf_token", csrf)
			return postForm(env.h, path, v, langHeader(cookie, l))
		}
	}
	get := func(path string) sender {
		return func(l lang) *httptest.ResponseRecorder { return do(env.h, "GET", path, langHeader(cookie, l)) }
	}
	upload := func(path, text string) sender {
		return func(l lang) *httptest.ResponseRecorder {
			return serve(env.h, uploadRequest(t, path, csrf, "ujian.md", []byte(text), langHeader(cookie, l)))
		}
	}
	schedule := func(opens, closes string) sender {
		return form(examPath(examID)+"/schedule", url.Values{"opens_at": {opens}, "closes_at": {closes}})
	}
	invalid := fileError(t, noCorrectSecond)
	fileAlert := func(key string) shown {
		return shown{
			argEscaper.Replace(localizer{langID}.T(key, localizer{langID}.FileError(invalid))),
			argEscaper.Replace(localizer{langEN}.T(key, invalid.Msg)),
		}
	}
	return []refusal{
		{"import no file", upload("/admin/exams", ""), http.StatusUnprocessableEntity, gox(t, "exams.missing_file")},
		{"import invalid", upload("/admin/exams", noCorrectSecond), http.StatusUnprocessableEntity, fileAlert("exams.import_failed")},
		{"replace invalid", upload(examPath(examID)+"/replace", noCorrectSecond), http.StatusUnprocessableEntity, fileAlert("examdetail.replace_failed")},
		{"replace frozen", upload(examPath(frozen)+"/replace", fixtureSimple("Baru")), http.StatusConflict, gox(t, "examdetail.frozen")},
		{"schedule time", schedule("bukan waktu", ""), http.StatusUnprocessableEntity, gox(t, "examdetail.schedule.invalid_time")},
		{"schedule empty", schedule("", ""), http.StatusUnprocessableEntity, gox(t, "examdetail.schedule.empty")},
		{"schedule order", schedule("2026-11-20T10:00", "2026-11-20T09:00"), http.StatusUnprocessableEntity, gox(t, "examdetail.schedule.order")},
		{"schedule past", schedule("", "2026-11-01T09:00"), http.StatusUnprocessableEntity, gox(t, "examdetail.schedule.past")},
		{"open archived", form(examPath(archived)+"/open", url.Values{}), http.StatusConflict, gox(t, "examdetail.archived_refusal")},
		{"schedule archived", form(examPath(archived)+"/schedule", url.Values{"closes_at": {"2026-11-20T09:00"}}), http.StatusConflict, gox(t, "examdetail.archived_refusal")},
		{"download no revision", get(examPath(old) + "/download"), http.StatusConflict, gox(t, "examdetail.no_revision")},
		{"copy no revision", form(examPath(old)+"/copy", url.Values{}), http.StatusConflict, gox(t, "examdetail.no_revision")},
		{"add teacher invalid", form("/admin/teachers", url.Values{"email": {"bukan surel"}}), http.StatusUnprocessableEntity, gox(t, "teachers.invalid_email")},
		{"remove admin confirm", get("/admin/teachers/" + strconv.FormatInt(adminID, 10) + "/remove"), http.StatusConflict, gox(t, "teachers.admin_email")},
		{"remove admin", form("/admin/teachers/"+strconv.FormatInt(adminID, 10)+"/remove", url.Values{}), http.StatusConflict, gox(t, "teachers.admin_email")},
	}
}

func TestTeacherPages_SameRefusals(t *testing.T) {
	for _, r := range teacherRefusals(t) {
		statuses := map[lang]int{}
		for _, l := range []lang{langEN, langID} {
			rec := r.send(l)
			statuses[l] = rec.Code
			body := rec.Body.String()
			want := alertDiv(r.alert)
			text := map[lang]string{langEN: want.en, langID: want.id}[l]
			if !strings.Contains(body, text) {
				t.Errorf("%s in %s: lacks %q:\n%s", r.name, l, text, body)
			}
		}
		if statuses[langEN] != r.status || statuses[langID] != r.status {
			t.Errorf("%s: status en %d, id %d, want %d", r.name, statuses[langEN], statuses[langID], r.status)
		}
	}
}

func TestProjector_Indonesian(t *testing.T) {
	pages := fixturePages(t, langID, "projector-open", "projector-closed")
	requireIndonesian(t, "projector open", pages["projector-open"],
		tx(t, "projector.back").in(`<a class="quiet" href="/admin/exams/`),
		thx(t, "projector.step_html", "app.test").in(`<p class="projector-step">{}</p>`))
	if strings.Contains(pages["projector-open"], `id="projector-closed"`) {
		t.Errorf("open projector shows the closed note")
	}
	requireIndonesian(t, "projector closed", pages["projector-closed"],
		tx(t, "projector.closed").in(`<p id="projector-closed" class="frozen-notice">{}</p>`))
	if !strings.Contains(pages["projector-open"], tx(t, "projector.back").id+"</a></header>") {
		t.Errorf("projector: back link not in Indonesian")
	}
}

var confirmFormRe = regexp.MustCompile(`(?s)<form id="[^"]*" method="post" action="[^"]*">.*?<button`)

func TestConfirm_Indonesian(t *testing.T) {
	id := fixturePages(t, langID, "confirm-archive", "confirm-remove-teacher")
	en := fixturePages(t, langEN, "confirm-archive", "confirm-remove-teacher")
	note := `<p class="confirm-note">{}</p>`
	requireIndonesian(t, "archive", id["confirm-archive"],
		gox(t, "confirm.archive.heading").in("<title>{} · Answerbox</title>"),
		gox(t, "confirm.archive.heading").in("<h1>{}</h1>"),
		gox(t, "confirm.archive.question", "Ujian Biologi").in(`<p class="confirm-question">{}</p>`),
		gox(t, "confirm.archive.note_close").in(note),
		gox(t, "confirm.archive.note_list").in(note),
		gox(t, "confirm.archive.note_keeps").in(note),
		gox(t, "confirm.archive.note_unarchive").in(note),
		gox(t, "confirm.archive.submit").in(`<button type="submit">{}</button>`),
		tx(t, "confirm.cancel").in(`">{}</a>`))
	if !strings.Contains(id["confirm-archive"], "‘Ujian Biologi’") {
		t.Errorf("archive: the title is not in ‘ ’")
	}
	requireIndonesian(t, "remove teacher", id["confirm-remove-teacher"],
		gox(t, "confirm.remove_teacher.heading").in("<h1>{}</h1>"),
		gox(t, "confirm.remove_teacher.question", fixtureWatiEmail).in(`<p class="confirm-question">{}</p>`),
		gox(t, "confirm.remove_teacher.note_signed_out").in(note),
		gox(t, "confirm.remove_teacher.note_exams", 0).in(note),
		gox(t, "confirm.remove_teacher.note_add_again").in(note),
		gox(t, "confirm.remove_teacher.submit").in(`<button type="submit" class="danger">{}</button>`),
		tx(t, "confirm.cancel").in(`">{}</a>`))
	for name := range id {
		got, want := confirmFormRe.FindString(id[name]), confirmFormRe.FindString(en[name])
		if want == "" || got != want {
			t.Errorf("%s: form %q, English %q", name, got, want)
		}
	}
}

func TestTeachers_Indonesian(t *testing.T) {
	pages := fixturePages(t, langID, "teachers", "teachers-add-refused", "teachers-remove-admin")
	th := `<th scope="col">{}</th>`
	requireIndonesian(t, "teachers", pages["teachers"],
		tx(t, "teachers.title").in("<title>{} · Answerbox</title>"),
		tx(t, "teachers.title").in("<h1>{}</h1>"),
		tx(t, "teachers.email_label").in(`<label for="add-teacher-email">{}</label>`),
		tx(t, "teachers.add").in(`<button type="submit">{}</button>`),
		tx(t, "teachers.col.email").in(th), tx(t, "teachers.col.name").in(th), tx(t, "teachers.col.last_sign_in").in(th),
		tx(t, "teachers.col.exams").in(th), tx(t, "teachers.col.actions").in(th),
		tx(t, "teachers.admin_badge").in(`<span class="badge badge-muted">{}</span>`),
		tx(t, "teachers.remove").in(`/remove">{}</a>`),
		gox(t, "teachers.never").in("<td>{}</td>"),
		tx(t, "teachers.zone_note", "UTC").in(`<p class="zone-note">{}</p>`),
		tx(t, "nav.teachers").in(` aria-current="page">{}</a>`))
	requireIndonesian(t, "add refused", pages["teachers-add-refused"], alertDiv(gox(t, "teachers.invalid_email")))
	requireIndonesian(t, "remove admin", pages["teachers-remove-admin"], alertDiv(gox(t, "teachers.admin_email")))
}
