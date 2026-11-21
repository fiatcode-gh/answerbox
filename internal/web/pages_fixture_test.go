package web

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite/sqlitetest"
)

// pageCase is one request of the page fixture. Requesting it changes
// nothing, so a case can be fetched any number of times in any language.
type pageCase struct {
	name   string      // unique, file-name safe
	method string      // GET or POST
	path   string      // path and query
	form   url.Values  // POST body; nil for GET
	upload []byte      // when set: a multipart body with this file part "file"
	header http.Header // cookies of the student or teacher; nil for none
	status int         // the status the case must answer
}

// The fixture's people and exam content hold no English dictionary word, so
// an Indonesian page can be searched for English left over.
const (
	fixtureAdminEmail   = "kepala@sekolah.id"
	fixtureAdminName    = "Bu Kepala"
	fixtureWatiEmail    = "wati@sekolah.id"
	fixtureWatiName     = "Bu Wati"
	fixtureJokoEmail    = "joko@sekolah.id"
	fixtureResumeTyped  = "ABCDEFGH"
	fixtureUploadedName = "ujian.md"
)

// fixtureMath is open, sectioned, with a fill-in question, the monitor on
// and a review shown after submitting.
const fixtureMath = `# Ujian Matematika
shuffle: none
show_result: review
duration: 30

## Bagian Satu
Pilih **satu** jawaban.

1. Tiga ditambah empat?
- 7 *
- 8

2. Lima dikali dua?
- 12
- 10 *

## Bagian Dua

3. Ibu kota Indonesia?
= Jakarta

4. Sembilan dikurangi lima?
- 4 *
- 3
`

// fixtureLanguage shows nothing after the end; its one attempt times out.
const fixtureLanguage = `# Bahasa Daerah
shuffle: none
show_result: none
duration: 5

1. Sapaan pagi?
- Sugeng enjing *
- Sugeng dalu
`

// fixturePhysics is edited into five revisions and copied; it gets no attempt.
const fixturePhysics = `# Ujian Fisika
shuffle: none
show_result: score

## Bagian Satu

1. Dua ditambah dua?
- 4 *
- 5

2. Tiga ditambah tiga?
- 6 *
- 7

3. Empat ditambah empat?
- 8 *
- 9

4. Ibu kota Jawa Barat?
= Bandung
`

const fixtureBroken = "# Ujian Rusak\nshuffle: none\n\nsalah\n"

func fixtureSimple(title string) string {
	return "# " + title + "\nshuffle: none\nshow_result: score\n\n1. Satu ditambah satu?\n- 2 *\n- 3\n"
}

// fixtureSignIn opens a session for email through the service and returns
// its cookie header and CSRF token.
func fixtureSignIn(t *testing.T, env *testEnv, email, name string) (http.Header, string) {
	t.Helper()
	ctx := context.Background()
	token, err := env.admin.SignIn(ctx, adminauth.Identity{Email: email, EmailVerified: true, Name: name})
	if err != nil {
		t.Fatalf("SignIn(%s): %v", email, err)
	}
	sess, ok, err := env.admin.Session(ctx, token)
	if err != nil || !ok {
		t.Fatalf("Session(%s): %v %v", email, ok, err)
	}
	return adminCookie(token), sess.CSRFToken
}

// pageCases builds one deterministic state and returns the cases that show
// every template in the states the localization must cover. The sign-in
// success page (admin_signed_in.html) needs a fresh provider flow per
// request, so its own tests cover it.
func pageCases(t *testing.T) (*testEnv, []pageCase) {
	t.Helper()
	ctx := context.Background()
	env := newTestEnvWith(t, sqlitetest.Open(t), envConfig{adminEmails: []string{fixtureAdminEmail}})
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("fixture: %s: %v", what, err)
		}
	}
	t0 := env.clock

	// Exams.
	math := env.openExam(t, fixtureMath)
	language := env.openExam(t, fixtureLanguage)
	history := env.scheduledExam(t, fixtureSimple("Ujian Sejarah"), exam.Schedule{
		OpensAt:  time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC),
		ClosesAt: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
	})
	biology, err := env.exams.Import(ctx, env.owner, []byte(fixtureSimple("Ujian Biologi")))
	must("import biology", err)
	chemistry, err := env.exams.Import(ctx, env.owner, []byte(fixtureSimple("Ujian Kimia")))
	must("import chemistry", err)
	must("archive chemistry", env.exams.Archive(ctx, env.owner, chemistry))

	// Five revisions: import, settings, the correct option of question 1,
	// a second section at question 3, question 1 moved to 2. Then a copy.
	physics, err := env.exams.Import(ctx, env.owner, []byte(fixturePhysics))
	must("import physics", err)
	rev := func() int {
		t.Helper()
		e, err := env.exams.Get(ctx, env.owner, physics)
		must("get physics", err)
		return e.Revision
	}
	must("settings", env.exams.SaveSettings(ctx, env.owner, physics, rev(), exam.Settings{
		Title: "Ujian Fisika Baru", Shuffle: examfile.ShuffleNone, ShowResult: examfile.ShowScore, Monitor: true, DurationMinutes: 40,
	}))
	must("question", env.exams.SaveQuestion(ctx, env.owner, physics, rev(), 1, exam.QuestionSource{
		Text: "Dua ditambah dua?", Options: []exam.OptionSource{{Text: "4"}, {Text: "5", Correct: true}},
	}))
	must("section", env.exams.StartSection(ctx, env.owner, physics, rev(), 3, exam.SectionSource{Title: "Bagian Dua", Text: "Jawab dengan singkat."}))
	must("move", env.exams.MoveQuestion(ctx, env.owner, physics, rev(), 1, 2))
	physicsCopy, err := env.exams.Copy(ctx, env.owner, physics)
	must("copy physics", err)
	physicsRev := strconv.Itoa(rev())

	// Attempts: Tono and Rudi time out, Sari submits, Budi is still working.
	mathQs, err := env.exams.Questions(ctx, math)
	must("questions", err)
	tonoToken, tono := env.startAttempt(t, math, "Tono")
	rudiToken, _ := env.startAttempt(t, language, "Rudi")
	env.clock = t0.Add(31 * time.Minute)
	env.submitAttempt(t, tonoToken)
	env.submitAttempt(t, rudiToken)

	sariToken, sari := env.startAttempt(t, math, "Sari")
	env.pick(t, sariToken, mathQs[0], 1)
	env.pick(t, sariToken, mathQs[1], 1)
	_, err = env.attempts.SaveText(ctx, sariToken, mathQs[2].ID, "Batavia")
	must("type", err)
	_, err = env.attempts.RecordEvent(ctx, sariToken, attempt.LeftPage, mathQs[0].ID)
	must("event", err)
	_, err = env.attempts.RecordEvent(ctx, sariToken, attempt.Copy, mathQs[2].ID)
	must("event", err)

	env.clock = t0.Add(35 * time.Minute)
	budiToken, budi := env.startAttempt(t, math, "Budi")
	env.pick(t, budiToken, mathQs[0], 2)
	_, err = env.attempts.SaveText(ctx, budiToken, mathQs[2].ID, "Bandung")
	must("type", err)
	_, err = env.attempts.RecordEvent(ctx, budiToken, attempt.Paste, mathQs[2].ID)
	must("event", err)

	env.clock = t0.Add(40 * time.Minute)
	env.submitAttempt(t, sariToken)
	must("override", env.attempts.Override(ctx, math, sari, 3, env.owner.TeacherID, attempt.MarkRight))

	// Teachers: Wati signed in once, Joko never did.
	env.clock = t0.Add(45 * time.Minute)
	must("add wati", env.admin.AddTeacher(ctx, fixtureWatiEmail))
	must("add joko", env.admin.AddTeacher(ctx, fixtureJokoEmail))
	fixtureSignIn(t, env, fixtureWatiEmail, fixtureWatiName)
	admin, csrf := fixtureSignIn(t, env, fixtureAdminEmail, fixtureAdminName)
	var wati int64
	teachers, err := env.admin.Teachers(ctx)
	must("teachers", err)
	for _, tc := range teachers {
		if tc.Email == fixtureWatiEmail {
			wati = tc.ID
		}
	}

	codes := map[string]bool{}
	for _, id := range []int64{math, language, history, biology, chemistry, physics, physicsCopy} {
		codes[env.examCode(t, id)] = true
	}
	unknown := "ABCDEF"
	for _, c := range []string{"GHJKMN", "PQRSTV", "WXYZ23"} {
		if !codes[unknown] {
			break
		}
		unknown = c
	}

	student := func(token string) http.Header { return studentCookie(token) }
	withCSRF := func(v url.Values) url.Values {
		v.Set("csrf_token", csrf)
		return v
	}
	get := func(name, path string, header http.Header, status int) pageCase {
		return pageCase{name: name, method: "GET", path: path, header: header, status: status}
	}
	post := func(name, path string, form url.Values, header http.Header, status int) pageCase {
		return pageCase{name: name, method: "POST", path: path, form: form, header: header, status: status}
	}
	ep := examPath
	n := func(id int64) string { return strconv.FormatInt(id, 10) }
	ok, unprocessable, conflict := http.StatusOK, http.StatusUnprocessableEntity, http.StatusConflict

	cases := []pageCase{
		// landing.html
		get("landing", "/", nil, ok),
		get("landing-code-open", "/?code="+env.examCode(t, math), nil, ok),
		get("landing-code-unknown", "/?code="+unknown, nil, ok),
		get("landing-code-scheduled", "/?code="+env.examCode(t, history), nil, ok),
		get("landing-last-result", "/", student(sariToken), ok),
		post("landing-start-refused-name", "/start", url.Values{"code": {env.examCode(t, math)}, "name": {""}}, nil, unprocessable),
		post("landing-start-refused-code", "/start", url.Values{"code": {unknown}, "name": {"Rina"}}, nil, unprocessable),
		post("landing-resume-refused", "/resume", url.Values{"code": {fixtureResumeTyped}}, nil, unprocessable),

		// exam.html, result.html
		get("exam-in-progress", "/exam", student(budiToken), ok),
		get("result-review", "/exam", student(sariToken), ok),
		get("result-timed-out-none", "/exam", student(rudiToken), ok),

		// error.html
		get("error-404", "/tidak-ada", nil, http.StatusNotFound),
		get("error-404-teacher", "/admin/tidak-ada", admin, http.StatusNotFound),
		post("error-403-csrf", ep(biology)+"/open", url.Values{"csrf_token": {"salah"}}, admin, http.StatusForbidden),
		{name: "error-403-cross-site", method: "POST", path: "/start", form: url.Values{"code": {env.examCode(t, math)}, "name": {"Rina"}},
			header: http.Header{"Sec-Fetch-Site": {"cross-site"}}, status: http.StatusForbidden},

		// admin_login.html
		get("login", "/admin/login", nil, ok),
		get("login-not-allowed", "/admin/login?error=not_allowed", nil, ok),
		get("login-failed", "/admin/login?error=failed", nil, ok),

		// admin_exams.html
		get("exams", "/admin/exams", admin, ok),
		get("exams-archived", "/admin/exams?archived=1", admin, ok),
		{name: "exams-import-refused", method: "POST", path: "/admin/exams", form: withCSRF(url.Values{}), upload: []byte(fixtureBroken), header: admin, status: unprocessable},
		post("exams-import-no-file", "/admin/exams", withCSRF(url.Values{}), admin, unprocessable),

		// admin_exam.html
		get("exam-open", ep(math), admin, ok),
		get("exam-scheduled", ep(history), admin, ok),
		get("exam-archived", ep(chemistry), admin, ok),
		post("exam-schedule-refused", ep(biology)+"/schedule", withCSRF(url.Values{"opens_at": {"2026-11-20T10:00"}, "closes_at": {"2026-11-20T09:00"}}), admin, unprocessable),
		{name: "exam-replace-frozen", method: "POST", path: ep(math) + "/replace", form: withCSRF(url.Values{}), upload: []byte(fixtureMath), header: admin, status: conflict},

		// admin_projector.html
		get("projector-open", ep(math)+"/projector", admin, ok),
		get("projector-closed", ep(biology)+"/projector", admin, ok),

		// admin_confirm.html
		get("confirm-archive", ep(biology)+"/archive", admin, ok),
		get("confirm-remove-question", ep(physics)+"/questions/1/remove", admin, ok),
		get("confirm-remove-section", ep(physics)+"/sections/2/remove", admin, ok),
		get("confirm-remove-teacher", "/admin/teachers/"+n(wati)+"/remove", admin, ok),
		get("confirm-delete-attempt", attemptPath(math, tono)+"/delete", admin, ok),

		// admin_attempts.html
		get("attempts", attemptsPath(math), admin, ok),
		get("attempts-filter-one", attemptsPath(math)+"?name=sari", admin, ok),
		get("attempts-filter-none", attemptsPath(math)+"?name=zaki", admin, ok),
		get("attempts-empty", attemptsPath(biology), admin, ok),

		// admin_attempt.html
		get("attempt-submitted", attemptPath(math, sari), admin, ok),
		get("attempt-in-progress", attemptPath(math, budi), admin, ok),
		post("attempt-rename-refused", attemptPath(math, sari)+"/rename", withCSRF(url.Values{"name": {""}}), admin, unprocessable),
		post("attempt-override-refused", attemptPath(math, sari)+"/questions/1/override", withCSRF(url.Values{"mark": {"right"}}), admin, unprocessable),

		// admin_revisions.html, admin_revision.html
		get("revisions", ep(physics)+"/revisions", admin, ok),
		get("revisions-copy", ep(physicsCopy)+"/revisions", admin, ok),
		get("revision-copy-1", ep(physicsCopy)+"/revisions/1", admin, ok),

		// admin_exam_edit.html
		get("editor", editPath(physics), admin, ok),
		post("editor-move-stale", ep(physics)+"/questions/2/move", withCSRF(url.Values{"revision": {"1"}, "to": {"1"}}), admin, conflict),

		// admin_question.html
		get("question-choice", questionPath(physics, 1), admin, ok),
		get("question-fill-in", questionPath(physics, 4), admin, ok),
		get("question-new", ep(physics)+"/questions/new", admin, ok),
		post("question-new-add", ep(physics)+"/questions/new", withCSRF(url.Values{"revision": {physicsRev}, "do": {"add"}, "kind": {"choice"}, "text": {""}, "option": {"", "", "", ""}}), admin, ok),
		post("question-refused", questionPath(physics, 1), withCSRF(url.Values{"revision": {physicsRev}, "do": {"save"}, "kind": {"choice"}, "text": {"Dua ditambah dua?"}, "option": {"4", "5"}}), admin, unprocessable),
		post("question-stale", questionPath(physics, 1), withCSRF(url.Values{"revision": {"1"}, "do": {"save"}, "kind": {"choice"}, "text": {"Dua ditambah dua?"}, "option": {"4", "5"}, "correct": {"0"}}), admin, conflict),

		// admin_section.html
		get("section-new", questionPath(physics, 2)+"/section", admin, ok),
		get("section-edit", sectionPath(physics, 1), admin, ok),
		post("section-refused", sectionPath(physics, 1), withCSRF(url.Values{"revision": {physicsRev}, "title": {""}, "text": {""}}), admin, unprocessable),

		// admin_exam_settings.html
		get("settings", ep(physics)+"/settings", admin, ok),
		post("settings-refused", ep(physics)+"/settings", withCSRF(url.Values{"revision": {physicsRev}, "title": {"Ujian Fisika Baru"}, "shuffle": {"none"}, "show_result": {"score"}, "monitor": {"on"}, "duration": {"0"}}), admin, unprocessable),

		// admin_exam_new.html
		get("exam-new", "/admin/exams/new", admin, ok),
		post("exam-new-refused", "/admin/exams/new", withCSRF(url.Values{"do": {"save"}, "title": {""}, "shuffle": {"none"}, "show_result": {"score"}, "duration": {"60"}, "kind": {"choice"}, "text": {""}, "option": {"", "", "", ""}}), admin, unprocessable),

		// admin_teachers.html
		get("teachers", "/admin/teachers", admin, ok),
		post("teachers-add-refused", "/admin/teachers", withCSRF(url.Values{"email": {"bukan surel"}}), admin, unprocessable),
		get("teachers-remove-admin", "/admin/teachers/"+n(env.owner.TeacherID)+"/remove", admin, conflict),

		// CSV
		get("attempts-csv", attemptsPath(math)+".csv", admin, ok),
	}
	revisions, err := env.exams.Revisions(ctx, physics)
	must("revisions", err)
	for _, r := range revisions {
		cases = append(cases, get("revision-"+strconv.Itoa(r.Number), ep(physics)+"/revisions/"+strconv.Itoa(r.Number), admin, ok))
	}
	return env, cases
}

// fetch requests c with the language cookie of l, checks its status and
// returns the body.
func (env *testEnv) fetch(t *testing.T, c pageCase, l lang) string {
	t.Helper()
	rec := env.response(t, c, l)
	if rec.Code != c.status {
		t.Fatalf("%s: %s %s = %d, want %d:\n%s", c.name, c.method, c.path, rec.Code, c.status, rec.Body.String())
	}
	return rec.Body.String()
}

// response requests c with the language cookie of l.
func (env *testEnv) response(t *testing.T, c pageCase, l lang) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	switch {
	case c.upload != nil:
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, vs := range c.form {
			for _, v := range vs {
				if err := mw.WriteField(k, v); err != nil {
					t.Fatal(err)
				}
			}
		}
		fw, err := mw.CreateFormFile("file", fixtureUploadedName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(c.upload); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(c.method, c.path, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
	case c.method == "POST":
		req = httptest.NewRequest(c.method, c.path, strings.NewReader(c.form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	default:
		req = httptest.NewRequest(c.method, c.path, nil)
	}
	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Add("Cookie", langCookieName+"="+string(l))
	return serve(env.h, req)
}
