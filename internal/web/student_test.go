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
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
)

// unknownToken is syntactically valid (43 base64url characters) but names no
// attempt.
const unknownToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (env *testEnv) openExam(t *testing.T, text string) int64 {
	t.Helper()
	id, err := env.exams.Import(context.Background(), []byte(text))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := env.exams.Open(context.Background(), id); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return id
}

func (env *testEnv) list(t *testing.T, examID int64) []attempt.Summary {
	t.Helper()
	rows, err := env.attempts.List(context.Background(), examID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return rows
}

func studentCookie(token string) http.Header {
	return http.Header{"Cookie": {"goexam_attempt=" + token}}
}

// attemptSetCookie returns the goexam_attempt cookie the response sets, if any.
func attemptSetCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, line := range rec.Header().Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("bad Set-Cookie %q: %v", line, err)
		}
		if c.Name == "goexam_attempt" {
			return c
		}
	}
	return nil
}

func requireAttemptCookieCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := attemptSetCookie(t, rec)
	if c == nil || c.MaxAge >= 0 || c.Path != "/" || c.Value != "" {
		t.Fatalf("want a cleared goexam_attempt cookie with Path=/, got %+v", c)
	}
}

// start posts the start form and returns the new attempt's cookie header.
func (env *testEnv) start(t *testing.T, name string) http.Header {
	t.Helper()
	rec := postForm(env.h, "/start", url.Values{"name": {name}}, nil)
	requireRedirect(t, rec, "/exam")
	c := attemptSetCookie(t, rec)
	if c == nil {
		t.Fatal("POST /start set no goexam_attempt cookie")
	}
	return studentCookie(c.Value)
}

// examText builds an exam of n questions with m options each; option 1 of
// every question is the correct one.
func examText(shuffle, show string, n, m int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Exam %s %s\nshuffle: %s\nshow_result: %s\n", shuffle, show, shuffle, show)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "\n%d. Question %d\n", i, i)
		for j := 1; j <= m; j++ {
			mark := ""
			if j == 1 {
				mark = " *"
			}
			fmt.Fprintf(&b, "- Option %d.%d%s\n", i, j, mark)
		}
	}
	return b.String()
}

// simpleExam has two unshuffled questions; the keys are option 1 of
// question 1 and option 2 of question 2.
func simpleExam(show string) string {
	return "# Algebra & Co\nshuffle: none\nshow_result: " + show + "\n\n1. One\n- a *\n- b\n\n2. Two\n- c\n- d *\n"
}

func TestLanding_States(t *testing.T) {
	env := newTestEnv(t)

	rec := do(env.h, "GET", "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	noExam := regexp.MustCompile(`(?s)id="no-exam"[^>]*>.*?No exam is open right now`).MatchString(body)
	if !noExam {
		t.Errorf("no open exam: #no-exam text missing:\n%s", body)
	}
	if !strings.Contains(body, `id="resume-form"`) || !strings.Contains(body, `name="code"`) {
		t.Errorf("resume form missing:\n%s", body)
	}
	if strings.Contains(body, `id="start-form"`) {
		t.Error("start form shown without an open exam")
	}

	env.openExam(t, simpleExam("score"))
	body = do(env.h, "GET", "/", nil).Body.String()
	for _, want := range []string{"Algebra &amp; Co", `<form id="start-form" method="post" action="/start">`, `name="name"`,
		"You have 60 minutes. The timer starts when you press Start and does not pause.", `id="resume-form"`} {
		if !strings.Contains(body, want) {
			t.Errorf("open exam landing lacks %q", want)
		}
	}
	if strings.Contains(body, `id="no-exam"`) {
		t.Error("#no-exam shown with an open exam")
	}
	if strings.Contains(body, "See your last result") {
		t.Error("last-result link shown without a cookie")
	}

	cookie := env.start(t, "Ada")
	requireRedirect(t, do(env.h, "GET", "/", cookie), "/exam")

	requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
	rec = do(env.h, "GET", "/", cookie)
	if rec.Code != http.StatusOK || !regexp.MustCompile(`<a href="/exam"[^>]*>See your last result</a>`).MatchString(rec.Body.String()) {
		t.Errorf("finished cookie: want landing with the last-result link, got %d:\n%s", rec.Code, rec.Body.String())
	}

	rec = do(env.h, "GET", "/", studentCookie(unknownToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown cookie: GET / = %d", rec.Code)
	}
	requireAttemptCookieCleared(t, rec)
	if strings.Contains(rec.Body.String(), "See your last result") {
		t.Error("unknown cookie shows the last-result link")
	}
}

func TestStart_Validation(t *testing.T) {
	env := newTestEnv(t)

	rec := postForm(env.h, "/start", url.Values{"name": {"Ada"}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no open exam: POST /start = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="no-exam"`) || attemptSetCookie(t, rec) != nil {
		t.Errorf("no open exam: want the no-exam landing without a cookie:\n%s", rec.Body.String())
	}

	examID := env.openExam(t, simpleExam("score"))
	for _, name := range []string{"", "   ", strings.Repeat("x", 101)} {
		rec := postForm(env.h, "/start", url.Values{"name": {name}}, nil)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("name %q: status = %d, want 422", name, rec.Code)
		}
		body := rec.Body.String()
		if !regexp.MustCompile(`role="alert"[^>]*>Enter your name \(1 to 100 characters\)\.<`).MatchString(body) {
			t.Errorf("name %q: alert missing:\n%s", name, body)
		}
		if !strings.Contains(body, `id="start-form"`) || !strings.Contains(body, `value="`+name+`"`) {
			t.Errorf("name %q: typed value not kept in the start form", name)
		}
		if attemptSetCookie(t, rec) != nil {
			t.Errorf("name %q: cookie set", name)
		}
	}
	if n := len(env.list(t, examID)); n != 0 {
		t.Fatalf("invalid names created %d attempts", n)
	}

	cookie := env.start(t, "Ada")
	rec = postForm(env.h, "/start", url.Values{"name": {"Bob"}}, cookie)
	requireRedirect(t, rec, "/exam")
	if attemptSetCookie(t, rec) != nil {
		t.Error("second start replaced the cookie")
	}
	if rows := env.list(t, examID); len(rows) != 1 || rows[0].StudentName != "Ada" {
		t.Fatalf("attempts after second start = %+v, want only Ada", rows)
	}
}

func TestStart_CookieAttributes(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	rec := postForm(env.h, "/start", url.Values{"name": {"Ada"}}, nil)
	requireRedirect(t, rec, "/exam")

	var line string
	for _, l := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(l, "goexam_attempt=") {
			line = l
		}
	}
	if !regexp.MustCompile(`^goexam_attempt=[A-Za-z0-9_-]{43};`).MatchString(line) {
		t.Fatalf("Set-Cookie = %q, want a 43-character base64url token", line)
	}
	for _, attr := range []string{"Path=/", "Max-Age=604800", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains("; "+line+";", "; "+attr+";") {
			t.Errorf("Set-Cookie %q lacks %s", line, attr)
		}
	}
}

// pageQuestion is a question as the exam page renders it, in page order.
type pageQuestion struct {
	ID      int64
	Options []int64
	Checked int64 // 0 when no radio is checked
}

var (
	questionRe = regexp.MustCompile(`<section class="question" id="q(\d+)" data-question-id="(\d+)">`)
	radioRe    = regexp.MustCompile(`<input type="radio" name="q(\d+)" value="(\d+)" id="o(\d+)"( checked)?>`)
)

func pageQuestions(t *testing.T, body string) []pageQuestion {
	t.Helper()
	var out []pageQuestion
	index := map[int64]int{}
	for i, m := range questionRe.FindAllStringSubmatch(body, -1) {
		if m[1] != strconv.Itoa(i+1) {
			t.Fatalf("question section %d has id q%s", i+1, m[1])
		}
		id, _ := strconv.ParseInt(m[2], 10, 64)
		index[id] = len(out)
		out = append(out, pageQuestion{ID: id})
	}
	for _, m := range radioRe.FindAllStringSubmatch(body, -1) {
		q, _ := strconv.ParseInt(m[1], 10, 64)
		o, _ := strconv.ParseInt(m[2], 10, 64)
		if m[2] != m[3] {
			t.Fatalf("radio value %s has id o%s", m[2], m[3])
		}
		i, ok := index[q]
		if !ok {
			t.Fatalf("radio for unknown question %d", q)
		}
		out[i].Options = append(out[i].Options, o)
		if m[4] != "" {
			out[i].Checked = o
		}
	}
	if len(out) == 0 {
		t.Fatalf("no questions on the page:\n%s", body)
	}
	return out
}

func questionIDs(qs []pageQuestion) []int64 {
	ids := make([]int64, len(qs))
	for i, q := range qs {
		ids[i] = q.ID
	}
	return ids
}

func (env *testEnv) examPage(t *testing.T, cookie http.Header) string {
	t.Helper()
	rec := do(env.h, "GET", "/exam", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /exam = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func answerForm(q, o int64) url.Values {
	return url.Values{"question": {strconv.FormatInt(q, 10)}, "option": {strconv.FormatInt(o, 10)}}
}

func (env *testEnv) answer(cookie http.Header, q, o int64) *httptest.ResponseRecorder {
	return postForm(env.h, "/exam/answer", answerForm(q, o), cookie)
}

func requireJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), status, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestStudent_FlowStartAnswerReloadSubmit(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, examText("questions, options", "score", 3, 3))
	cookie := env.start(t, "Ada <Lovelace>")

	rec := do(env.h, "GET", "/exam", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /exam = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("exam page Cache-Control = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<main id="exam" data-remaining-ms="3600000" data-answer-url="/exam/answer" data-status-url="/exam/status"`,
		`<span id="timer" role="timer" data-level="normal">60:00</span>`,
		`id="timer-announce"`, `aria-live="polite"`,
		`<span id="save-state" role="status" data-state="saved">`,
		`<button type="button" id="nav-toggle" aria-expanded="false" aria-controls="question-nav" hidden`,
		`<nav id="question-nav" data-open="false"`,
		`<a class="bubble" href="#q1" data-answered="false">1</a>`,
		`<a class="bubble" href="#q3" data-answered="false">3</a>`,
		`id="answered-count">Answered 0 of 3<`,
		`<span class="q-state" data-state="idle">`,
		`<label class="option"`, `<span class="bubble">A</span>`,
		`<form id="submit-form" method="post" action="/exam/submit">`,
		`<button type="button" id="submit-open"`,
		`<dialog id="submit-dialog"`, `id="submit-summary"`, `id="submit-cancel"`, `id="submit-confirm"`,
		`id="time-up"`, `id="moved"`,
		`<noscript>`, "This exam needs JavaScript to save your answers.",
		"Exam questions, options score", "Ada &lt;Lovelace&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exam page lacks %q", want)
		}
	}
	for _, banner := range []string{"time-up", "moved"} {
		if !regexp.MustCompile(`id="` + banner + `"[^>]*\bhidden\b`).MatchString(body) {
			t.Errorf("#%s is not initially hidden", banner)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("exam page has a script tag")
	}
	first := pageQuestions(t, body)
	if len(first) != 3 {
		t.Fatalf("questions = %d, want 3", len(first))
	}

	q, o := first[1].ID, first[1].Options[2]
	requireJSON(t, env.answer(cookie, q, o), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)

	body = env.examPage(t, cookie)
	again := pageQuestions(t, body)
	if !slices.Equal(questionIDs(again), questionIDs(first)) {
		t.Fatalf("question order changed on reload: %v → %v", questionIDs(first), questionIDs(again))
	}
	if again[1].Checked != o || again[0].Checked != 0 || again[2].Checked != 0 {
		t.Errorf("checked radios after reload = %+v, want only %d", again, o)
	}
	for _, want := range []string{
		`<a class="bubble" href="#q2" data-answered="true">2</a>`,
		`<a class="bubble" href="#q1" data-answered="false">1</a>`,
		`id="answered-count">Answered 1 of 3<`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("reloaded page lacks %q", want)
		}
	}
	states := regexp.MustCompile(`<span class="q-state" data-state="(\w+)">`).FindAllStringSubmatch(body, -1)
	if len(states) != 3 || states[0][1] != "idle" || states[1][1] != "saved" || states[2][1] != "idle" {
		t.Errorf("q-state values = %v, want idle saved idle", states)
	}

	env.clock = env.clock.Add(10 * time.Minute)
	body = env.examPage(t, cookie)
	for _, want := range []string{`data-remaining-ms="3000000"`, `data-level="normal">50:00<`} {
		if !strings.Contains(body, want) {
			t.Errorf("after 10 minutes the page lacks %q", want)
		}
	}

	requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
	rec = do(env.h, "GET", "/exam", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<main id="result"`) {
		t.Fatalf("after submit GET /exam = %d, want the result page:\n%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("result page Cache-Control = %q", got)
	}
}

func TestExamPage_TimerLevels(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")
	for _, c := range []struct {
		elapsed time.Duration
		want    string
	}{
		{55*time.Minute - time.Second, `data-level="normal">05:01<`},
		{55 * time.Minute, `data-level="warn">05:00<`},
		{59 * time.Minute, `data-level="danger">01:00<`},
		{60*time.Minute - 1500*time.Millisecond, `data-level="danger">00:02<`},
		{60*time.Minute + 2*time.Second, `data-level="danger">00:00<`},
	} {
		env.clock = testStart.Add(c.elapsed)
		if body := env.examPage(t, cookie); !strings.Contains(body, c.want) {
			t.Errorf("after %v the timer lacks %q", c.elapsed, c.want)
		}
	}
}

func TestAnswer_AfterDeadline409(t *testing.T) {
	env := newTestEnv(t)
	examID := env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, cookie))
	deadline := testStart.Add(attempt.Duration)

	env.clock = deadline.Add(2 * time.Second)
	requireJSON(t, env.answer(cookie, qs[0].ID, qs[0].Options[0]), http.StatusOK, `{"saved":true,"remaining_ms":0}`)

	env.clock = deadline.Add(3*time.Second + time.Millisecond)
	requireJSON(t, env.answer(cookie, qs[1].ID, qs[1].Options[1]), http.StatusConflict, `{"error":"finished"}`)

	body := env.examPage(t, cookie)
	if !strings.Contains(body, `data-status="timed_out"`) || !strings.Contains(body, `id="score">1 / 2<`) {
		t.Errorf("want the timed-out result scoring only the in-grace answer:\n%s", body)
	}
	if rows := env.list(t, examID); len(rows) != 1 || rows[0].Score != 1 {
		t.Errorf("stored score = %+v, want 1", rows)
	}
}

func TestAnswer_Errors(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, cookie))
	noAttempt := `{"error":"no_attempt"}`

	requireJSON(t, env.answer(nil, qs[0].ID, qs[0].Options[0]), http.StatusUnauthorized, noAttempt)
	requireJSON(t, env.answer(studentCookie("not-a-token"), qs[0].ID, qs[0].Options[0]), http.StatusUnauthorized, noAttempt)
	requireJSON(t, env.answer(studentCookie(unknownToken), qs[0].ID, qs[0].Options[0]), http.StatusUnauthorized, noAttempt)
	for _, form := range []url.Values{
		{"question": {"x"}, "option": {strconv.FormatInt(qs[0].Options[0], 10)}},
		{"question": {strconv.FormatInt(qs[0].ID, 10)}, "option": {"1.5"}},
		{"question": {strconv.FormatInt(qs[0].ID, 10)}},
	} {
		requireJSON(t, postForm(env.h, "/exam/answer", form, cookie), http.StatusUnprocessableEntity, `{"error":"invalid"}`)
	}
	requireJSON(t, env.answer(cookie, qs[0].ID, qs[1].Options[0]), http.StatusUnprocessableEntity, `{"error":"invalid"}`)

	requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
	requireJSON(t, env.answer(cookie, qs[0].ID, qs[0].Options[0]), http.StatusConflict, `{"error":"finished"}`)
}

func TestStatus_JSON(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")

	env.clock = testStart.Add(90 * time.Second)
	requireJSON(t, do(env.h, "GET", "/exam/status", cookie), http.StatusOK, `{"status":"in_progress","remaining_ms":3510000}`)
	requireJSON(t, do(env.h, "GET", "/exam/status", nil), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireJSON(t, do(env.h, "GET", "/exam/status", studentCookie(unknownToken)), http.StatusUnauthorized, `{"error":"no_attempt"}`)

	requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
	requireJSON(t, do(env.h, "GET", "/exam/status", cookie), http.StatusOK, `{"status":"finished","remaining_ms":0}`)
}

func TestResume_OldCookieLosesAccess(t *testing.T) {
	env := newTestEnv(t)
	examID := env.openExam(t, examText("questions, options", "score", 3, 3))
	old := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, old))
	requireJSON(t, env.answer(old, qs[0].ID, qs[0].Options[1]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)

	rows := env.list(t, examID)
	if len(rows) != 1 || rows[0].ResumeCode == "" {
		t.Fatalf("no resume code: %+v", rows)
	}
	rec := postForm(env.h, "/resume", url.Values{"code": {rows[0].ResumeCode}}, nil)
	requireRedirect(t, rec, "/exam")
	c := attemptSetCookie(t, rec)
	if c == nil || c.MaxAge != 604800 {
		t.Fatalf("resume set no attempt cookie: %+v", c)
	}
	moved := studentCookie(c.Value)

	again := pageQuestions(t, env.examPage(t, moved))
	if !slices.Equal(questionIDs(again), questionIDs(qs)) || again[0].Checked != qs[0].Options[1] {
		t.Fatalf("resumed page = %+v, want order %v with %d checked", again, questionIDs(qs), qs[0].Options[1])
	}

	rec = do(env.h, "GET", "/exam", old)
	requireRedirect(t, rec, "/")
	requireAttemptCookieCleared(t, rec)
	requireJSON(t, env.answer(old, qs[1].ID, qs[1].Options[0]), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireRedirect(t, postForm(env.h, "/exam/submit", nil, old), "/")
	if rows := env.list(t, examID); rows[0].Status != attempt.InProgress {
		t.Fatalf("old cookie's submit finished the attempt: %+v", rows[0])
	}

	rec = postForm(env.h, "/resume", url.Values{"code": {"ZZZZ-ZZZZ"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad code = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !regexp.MustCompile(`role="alert"[^>]*>That code is not valid\.<`).MatchString(body) ||
		!strings.Contains(body, `<details class="resume" open>`) || attemptSetCookie(t, rec) != nil {
		t.Errorf("bad code: want the alert, an open resume form and no cookie:\n%s", body)
	}
}

func TestStudent_CannotTouchOtherAttempt(t *testing.T) {
	env := newTestEnv(t)
	examID := env.openExam(t, examText("none", "score", 2, 2))
	a := env.start(t, "Ada")
	b := env.start(t, "Bob")
	qs := pageQuestions(t, env.examPage(t, a))

	requireJSON(t, env.answer(a, qs[0].ID, qs[0].Options[0]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)
	for _, q := range pageQuestions(t, env.examPage(t, b)) {
		if q.Checked != 0 {
			t.Fatalf("A's answer shows on B's page: %+v", q)
		}
	}

	requireJSON(t, env.answer(b, qs[1].ID, qs[1].Options[0]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)
	pageA := pageQuestions(t, env.examPage(t, a))
	pageB := pageQuestions(t, env.examPage(t, b))
	if pageA[0].Checked != qs[0].Options[0] || pageA[1].Checked != 0 {
		t.Errorf("A's page after B saved = %+v", pageA)
	}
	if pageB[0].Checked != 0 || pageB[1].Checked != qs[1].Options[0] {
		t.Errorf("B's page = %+v", pageB)
	}

	requireRedirect(t, postForm(env.h, "/exam/submit", nil, b), "/exam")
	rows := env.list(t, examID)
	if len(rows) != 2 || rows[0].StudentName != "Ada" || rows[0].Status != attempt.InProgress || rows[0].Score != 1 ||
		rows[1].Status != attempt.Submitted || rows[1].Score != 1 {
		t.Fatalf("attempts = %+v, want Ada in progress (1) and Bob submitted (1)", rows)
	}

	stranger := studentCookie(unknownToken)
	requireJSON(t, env.answer(stranger, qs[0].ID, qs[0].Options[1]), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireJSON(t, do(env.h, "GET", "/exam/status", stranger), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireRedirect(t, do(env.h, "GET", "/exam", stranger), "/")
	requireRedirect(t, postForm(env.h, "/exam/submit", nil, stranger), "/")
	if rows := env.list(t, examID); rows[0].Status != attempt.InProgress || rows[0].Score != 1 {
		t.Fatalf("stranger changed A: %+v", rows[0])
	}
}

func TestExamPage_NoCookieRedirects(t *testing.T) {
	env := newTestEnv(t)
	rec := do(env.h, "GET", "/exam", nil)
	requireRedirect(t, rec, "/")
	if attemptSetCookie(t, rec) != nil {
		t.Error("no cookie: a cookie was set")
	}
	rec = do(env.h, "GET", "/exam", studentCookie("short"))
	requireRedirect(t, rec, "/")
	requireAttemptCookieCleared(t, rec)
}

func TestStudentPosts_CrossOriginRejected(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, cookie))
	crossSite := http.Header{"Sec-Fetch-Site": {"cross-site"}, "Cookie": cookie["Cookie"]}

	if rec := postForm(env.h, "/start", url.Values{"name": {"Mallory"}}, http.Header{"Sec-Fetch-Site": {"cross-site"}}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST /start = %d, want 403", rec.Code)
	}
	if rec := postForm(env.h, "/exam/answer", answerForm(qs[0].ID, qs[0].Options[0]), crossSite); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST /exam/answer = %d, want 403", rec.Code)
	}
	if q := pageQuestions(t, env.examPage(t, cookie)); q[0].Checked != 0 {
		t.Error("cross-site answer was stored")
	}
}

// keyedExam is examText with the key of question i at option key(i) (both
// 1-based), so two exams can be byte-identical except for the key.
func keyedExam(shuffle, show string, n, m int, key func(i int) int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Keyed exam\nshuffle: %s\nshow_result: %s\n", shuffle, show)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "\n%d. Question %d\n", i, i)
		for j := 1; j <= m; j++ {
			mark := ""
			if j == key(i) {
				mark = " *"
			}
			fmt.Fprintf(&b, "- Option %d.%d%s\n", i, j, mark)
		}
	}
	return b.String()
}

// twin is two apps with the same seeds and clock whose open exams differ only
// in the answer key (K1, K2). Every step runs on both and must answer alike.
type twin struct {
	k1, k2 *testEnv
}

func newTwin(t *testing.T, shuffle, show string, n, m int, key1, key2 func(int) int) twin {
	t.Helper()
	tw := twin{newTestEnv(t), newTestEnv(t)}
	text1, text2 := keyedExam(shuffle, show, n, m, key1), keyedExam(shuffle, show, n, m, key2)
	if text1 == text2 {
		t.Fatal("K1 and K2 exams are identical")
	}
	tw.k1.openExam(t, text1)
	tw.k2.openExam(t, text2)
	return tw
}

func (tw twin) setClock(c time.Time) {
	tw.k1.clock, tw.k2.clock = c, c
}

// step runs req on both apps, requires identical responses and returns K1's.
func (tw twin) step(t *testing.T, name string, req func(env *testEnv) *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	a, b := req(tw.k1), req(tw.k2)
	if a.Code != b.Code {
		t.Fatalf("%s: status K1 %d, K2 %d", name, a.Code, b.Code)
	}
	for _, h := range []string{"Location", "Content-Type"} {
		if a.Header().Get(h) != b.Header().Get(h) {
			t.Fatalf("%s: %s K1 %q, K2 %q", name, h, a.Header().Get(h), b.Header().Get(h))
		}
	}
	if !slices.Equal(a.Header().Values("Set-Cookie"), b.Header().Values("Set-Cookie")) {
		t.Fatalf("%s: Set-Cookie K1 %q, K2 %q", name, a.Header().Values("Set-Cookie"), b.Header().Values("Set-Cookie"))
	}
	if a.Body.String() != b.Body.String() {
		t.Fatalf("%s: bodies differ\nK1:\n%s\nK2:\n%s", name, a.Body.String(), b.Body.String())
	}
	return a
}

// startTwin starts one attempt on both apps and returns its cookie (the same
// on both, since the seeds are equal) and the exam page's questions.
func (tw twin) start(t *testing.T) (http.Header, []pageQuestion) {
	t.Helper()
	tw.step(t, "GET /", func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/", nil) })
	rec := tw.step(t, "POST /start", func(env *testEnv) *httptest.ResponseRecorder {
		return postForm(env.h, "/start", url.Values{"name": {"Ada"}}, nil)
	})
	c := attemptSetCookie(t, rec)
	if c == nil {
		t.Fatal("POST /start set no cookie")
	}
	cookie := studentCookie(c.Value)
	page := tw.step(t, "GET /exam", func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/exam", cookie) })
	return cookie, pageQuestions(t, page.Body.String())
}

func (tw twin) answer(t *testing.T, cookie http.Header, q, o int64) {
	t.Helper()
	rec := tw.step(t, fmt.Sprintf("answer %d=%d", q, o), func(env *testEnv) *httptest.ResponseRecorder { return env.answer(cookie, q, o) })
	if rec.Code != http.StatusOK {
		t.Fatalf("answer %d=%d: %d %s", q, o, rec.Code, rec.Body.String())
	}
}

func TestNoKeyLeak_InProgress(t *testing.T) {
	tw := newTwin(t, "questions, options", "review", 4, 3, func(int) int { return 1 }, func(int) int { return 2 })
	cookie, qs := tw.start(t)
	for i, q := range qs {
		tw.answer(t, cookie, q.ID, q.Options[i%len(q.Options)])
	}
	tw.step(t, "GET /exam/status", func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/exam/status", cookie) })
	rec := tw.step(t, "GET /exam again", func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/exam", cookie) })
	if !strings.Contains(rec.Body.String(), `<main id="exam"`) {
		t.Fatalf("not the exam page:\n%s", rec.Body.String())
	}
}

func TestNoKeyLeak_FinishedNoneAndScore(t *testing.T) {
	k1 := func(i int) int { return 1 + (i-1)%2 } // keys 1, 2
	k2 := func(i int) int { return 2 - (i-1)%2 } // keys 2, 1
	getExam := func(cookie http.Header) func(env *testEnv) *httptest.ResponseRecorder {
		return func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/exam", cookie) }
	}

	t.Run("none/submitted", func(t *testing.T) {
		tw := newTwin(t, "questions, options", "none", 2, 2, k1, k2)
		cookie, qs := tw.start(t)
		tw.answer(t, cookie, qs[0].ID, qs[0].Options[0])
		tw.step(t, "POST /exam/submit", func(env *testEnv) *httptest.ResponseRecorder {
			return postForm(env.h, "/exam/submit", nil, cookie)
		})
		body := tw.step(t, "GET /exam result", getExam(cookie)).Body.String()
		if !strings.Contains(body, `data-show="none"`) || strings.Contains(body, `id="score"`) {
			t.Errorf("want a none result without #score:\n%s", body)
		}
	})
	t.Run("none/timed_out", func(t *testing.T) {
		tw := newTwin(t, "questions, options", "none", 2, 2, k1, k2)
		cookie, qs := tw.start(t)
		tw.answer(t, cookie, qs[1].ID, qs[1].Options[1])
		tw.setClock(testStart.Add(attempt.Duration + time.Minute))
		body := tw.step(t, "GET /exam result", getExam(cookie)).Body.String()
		if !strings.Contains(body, `data-status="timed_out"`) || strings.Contains(body, `id="score"`) {
			t.Errorf("want a timed-out none result without #score:\n%s", body)
		}
	})
	t.Run("score", func(t *testing.T) {
		tw := newTwin(t, "none", "score", 2, 2, k1, k2)
		cookie, qs := tw.start(t)
		tw.answer(t, cookie, qs[0].ID, qs[0].Options[0])
		tw.answer(t, cookie, qs[1].ID, qs[1].Options[0])
		tw.step(t, "POST /exam/submit", func(env *testEnv) *httptest.ResponseRecorder {
			return postForm(env.h, "/exam/submit", nil, cookie)
		})
		body := tw.step(t, "GET /exam result", getExam(cookie)).Body.String()
		if !strings.Contains(body, `<p id="score">1 / 2</p>`) || strings.Contains(body, "data-correct") {
			t.Errorf("want score 1 / 2 and no data-correct:\n%s", body)
		}
	})
}

var (
	reviewSectionRe = regexp.MustCompile(`(?s)<section class="review-question">.*?</section>`)
	reviewOptionRe  = regexp.MustCompile(`<li class="review-option" data-picked="(true|false)" data-correct="(true|false)">`)
)

func TestReview_ShowsKey(t *testing.T) {
	finished := func(env *testEnv, key func(int) int) string {
		env.openExam(t, keyedExam("questions, options", "review", 3, 3, key))
		cookie := env.start(t, "Ada")
		qs := pageQuestions(t, env.examPage(t, cookie))
		requireJSON(t, env.answer(cookie, qs[0].ID, qs[0].Options[2]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)
		requireJSON(t, env.answer(cookie, qs[1].ID, qs[1].Options[0]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)
		requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
		return env.examPage(t, cookie)
	}
	body1 := finished(newTestEnv(t), func(int) int { return 1 })
	body2 := finished(newTestEnv(t), func(int) int { return 2 })
	if body1 == body2 {
		t.Fatal("review pages under K1 and K2 are identical; the key is not shown")
	}
	for _, body := range []string{body1, body2} {
		sections := reviewSectionRe.FindAllString(body, -1)
		if len(sections) != 3 {
			t.Fatalf("review sections = %d, want 3:\n%s", len(sections), body)
		}
		for i, sec := range sections {
			correct, picked := 0, 0
			for _, m := range reviewOptionRe.FindAllStringSubmatch(sec, -1) {
				if m[2] == "true" {
					correct++
				}
				if m[1] == "true" {
					picked++
				}
			}
			if correct != 1 {
				t.Errorf("review question %d has %d correct options, want 1", i+1, correct)
			}
			wantPicked := 1
			if i == 2 {
				wantPicked = 0
				if !strings.Contains(sec, "No answer") {
					t.Errorf("unanswered review question lacks \"No answer\"")
				}
			}
			if picked != wantPicked {
				t.Errorf("review question %d has %d picked options, want %d", i+1, picked, wantPicked)
			}
		}
	}
}

func TestResult_ByShowResult(t *testing.T) {
	// finish answers two questions correctly and one wrongly, then submits.
	finish := func(t *testing.T, show string) (env *testEnv, cookie http.Header, order []string) {
		env = newTestEnv(t)
		examID := env.openExam(t, examText("questions, options", show, 4, 3))
		key := map[int64]int64{}
		wrong := map[int64]int64{}
		questions, err := env.exams.Questions(context.Background(), examID)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range questions {
			for _, o := range q.Options {
				if o.Correct {
					key[q.ID] = o.ID
				} else {
					wrong[q.ID] = o.ID
				}
			}
		}
		cookie = env.start(t, "Ada")
		body := env.examPage(t, cookie)
		for _, m := range regexp.MustCompile(`(?s)<div class="question-body">(.*?)</div>`).FindAllStringSubmatch(body, -1) {
			order = append(order, strings.TrimSpace(m[1]))
		}
		if len(order) != 4 {
			t.Fatalf("question bodies on the exam page = %d, want 4", len(order))
		}
		qs := pageQuestions(t, body)
		for i, o := range []int64{key[qs[0].ID], key[qs[1].ID], wrong[qs[2].ID]} {
			requireJSON(t, env.answer(cookie, qs[i].ID, o), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)
		}
		requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
		return env, cookie, order
	}

	t.Run("none", func(t *testing.T) {
		env, cookie, _ := finish(t, "none")
		body := env.examPage(t, cookie)
		if !strings.Contains(body, "Your answers are recorded.") || strings.Contains(body, `id="score"`) ||
			strings.Contains(body, "review-question") {
			t.Errorf("none result:\n%s", body)
		}
	})
	t.Run("score", func(t *testing.T) {
		env, cookie, _ := finish(t, "score")
		body := env.examPage(t, cookie)
		if !strings.Contains(body, `<p id="score">2 / 4</p>`) || !strings.Contains(body, "correct answers") ||
			strings.Contains(body, "review-question") || strings.Contains(body, "Your answers are recorded.") {
			t.Errorf("score result:\n%s", body)
		}
	})
	t.Run("review", func(t *testing.T) {
		env, cookie, order := finish(t, "review")
		body := env.examPage(t, cookie)
		if !strings.Contains(body, `<p id="score">2 / 4</p>`) {
			t.Errorf("review result lacks the score:\n%s", body)
		}
		sections := reviewSectionRe.FindAllString(body, -1)
		if len(sections) != len(order) {
			t.Fatalf("review sections = %d, want %d", len(sections), len(order))
		}
		for i, sec := range sections {
			if !strings.Contains(sec, order[i]) || !strings.Contains(sec, fmt.Sprintf("Question %d of 4", i+1)) {
				t.Errorf("review section %d is not question %q in attempt order:\n%s", i+1, order[i], sec)
			}
		}
		for _, want := range []string{"✓ Correct answer", "✗ Your answer", "✓ Your answer — correct", "No answer"} {
			if !strings.Contains(body, want) {
				t.Errorf("review lacks %q", want)
			}
		}
	})
	t.Run("headings", func(t *testing.T) {
		env, cookie, _ := finish(t, "score")
		body := env.examPage(t, cookie)
		if !strings.Contains(body, `data-status="submitted"`) || !strings.Contains(body, "<h1>Exam submitted</h1>") {
			t.Errorf("submitted heading missing:\n%s", body)
		}
		other := env.start(t, "Bob")
		env.clock = env.clock.Add(attempt.Duration + attempt.Grace + time.Millisecond)
		body = env.examPage(t, other)
		if !strings.Contains(body, `data-status="timed_out"`) || !strings.Contains(body, "<h1>Time is up</h1>") {
			t.Errorf("timed-out heading missing:\n%s", body)
		}
	})
}
