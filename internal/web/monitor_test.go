package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (env *testEnv) event(cookie http.Header, kind string, question int64) *httptest.ResponseRecorder {
	form := url.Values{"kind": {kind}}
	if question != 0 {
		form.Set("question", strconv.FormatInt(question, 10))
	}
	return postForm(env.h, "/exam/event", form, cookie)
}

func TestEvent_Endpoint(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")
	other := env.start(t, "Bo")
	qs := pageQuestions(t, env.examPage(t, cookie))

	requireJSON(t, env.event(cookie, "paste", qs[1].ID), http.StatusOK, `{"recorded":true}`)
	requireJSON(t, env.event(cookie, "left_page", 0), http.StatusOK, `{"recorded":true}`)
	requireJSON(t, env.event(nil, "copy", 0), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireJSON(t, env.event(studentCookie(unknownToken), "copy", 0), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireJSON(t, env.event(cookie, "scroll", 0), http.StatusUnprocessableEntity, `{"error":"invalid"}`)
	requireJSON(t, postForm(env.h, "/exam/event", url.Values{"kind": {"copy"}, "question": {"x"}}, cookie),
		http.StatusUnprocessableEntity, `{"error":"invalid"}`)

	rows := env.list(t, id)
	if rows[0].Flags != 2 || rows[1].Flags != 0 {
		t.Fatalf("flags = %d, %d; want 2, 0", rows[0].Flags, rows[1].Flags)
	}

	// Saving answers still works after events and is unaffected by them.
	requireJSON(t, env.answer(cookie, qs[0].ID, qs[0].Options[0]), http.StatusOK, `{"saved":true,"remaining_ms":3600000}`)

	requireRedirect(t, postForm(env.h, "/exam/submit", nil, other), "/exam")
	requireJSON(t, env.event(other, "copy", 0), http.StatusConflict, `{"error":"finished"}`)
}

func TestEvent_OversizedBodyStoresNothing(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, simpleExam("score"))
	cookie := env.start(t, "Ada")
	form := url.Values{"kind": {"paste"}, "pad": {strings.Repeat("x", studentBodyLimit)}}
	requireJSON(t, postForm(env.h, "/exam/event", form, cookie), http.StatusUnprocessableEntity, `{"error":"invalid"}`)
	if f := env.list(t, id)[0].Flags; f != 0 {
		t.Fatalf("flags = %d after an oversized body", f)
	}
}

func TestEvent_MonitorOff(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, "# Quiet\nmonitor: off\n\n1. One\n- a *\n- b\n")
	cookie := env.start(t, "Ada")
	page := env.examPage(t, cookie)
	if !strings.Contains(page, `data-monitor="off"`) {
		t.Errorf("exam page lacks data-monitor=\"off\":\n%s", page)
	}
	requireJSON(t, env.event(cookie, "paste", 0), http.StatusOK, `{"recorded":false}`)
	if f := env.list(t, id)[0].Flags; f != 0 {
		t.Fatalf("flags = %d with monitor off", f)
	}
}

func TestExamPage_MonitorHooks(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, simpleExam("score"))
	page := env.examPage(t, env.start(t, "Ada"))
	for _, want := range []string{
		`data-monitor="on"`,
		`data-event-url="/exam/event"`,
		`<div id="monitor-banner" class="banner banner-warn" role="alert" hidden>Your teacher has been notified.</div>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("exam page lacks %s", want)
		}
	}
}

func TestTeacher_FlagsInListDetailCSVAndExam(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, _, _ := env.signIn(t)
	id, qs := env.resultsExam(t, 3)
	token, aid := env.startAttempt(t, id, "Ayu")
	env.startAttempt(t, id, "Budi")
	n := 0
	for i, q := range pageQuestions(t, env.examPage(t, studentCookie(token))) {
		if q.ID == qs[2].ID {
			n = i + 1
		}
	}
	if n == 0 {
		t.Fatal("file question 3 is not on the student's page")
	}
	onScreen := fmt.Sprintf("Question %d on screen (file question 3)", n)

	env.clock = testStart.Add(2 * time.Minute)
	if _, err := env.attempts.RecordEvent(t.Context(), token, "right_click", 0); err != nil {
		t.Fatal(err)
	}
	env.clock = testStart.Add(time.Minute)
	if _, err := env.attempts.RecordEvent(t.Context(), token, "left_page", qs[2].ID); err != nil {
		t.Fatal(err)
	}

	list := getOK(t, env, attemptsPath(id), cookie)
	if !regexp.MustCompile(`<th scope="col">Flags</th>`).MatchString(list) {
		t.Errorf("Flags column missing:\n%s", list)
	}
	rows := tableRows(list)
	if rows[0][1] != "Ayu" || rows[0][8] != "2" || rows[1][8] != "0" {
		t.Errorf("rows = %q, want flags 2 and 0 in the Flags column", rows)
	}

	detail := getOK(t, env, fmt.Sprintf("%s/%d", attemptsPath(id), aid), cookie)
	events := regexp.MustCompile(`(?s)<tr class="event">(.*?)</tr>`).FindAllStringSubmatch(detail, -1)
	var got []string
	for _, e := range events {
		got = append(got, strings.Join(strings.Fields(tagRe.ReplaceAllString(e[1], " ")), " "))
	}
	want := []string{
		"2026-11-14 16:31 Left the exam page " + onScreen,
		"2026-11-14 16:32 Right-clicked —",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %q, want %q", got, want)
	}
	if link := `<a href="#q3">` + onScreen + `</a>`; !strings.Contains(detail, link) {
		t.Errorf("detail lacks %s:\n%s", link, detail)
	}

	records, err := csv.NewReader(strings.NewReader(getOK(t, env, attemptsPath(id)+".csv", cookie))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if h := records[0]; h[len(h)-1] != "flags" || records[1][len(h)-1] != "2" || records[2][len(h)-1] != "0" {
		t.Errorf("CSV flags column wrong: %q", records)
	}

	examPage := getOK(t, env, examPath(id), cookie)
	if !regexp.MustCompile(`<dt>Monitor</dt>\s*<dd>On</dd>`).MatchString(examPage) {
		t.Errorf("exam detail does not show Monitor On:\n%s", examPage)
	}
}

func TestTeacher_NoEventsDetail(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	id, _ := env.resultsExam(t, 1)
	_, aid := env.startAttempt(t, id, "Ayu")
	detail := getOK(t, env, fmt.Sprintf("%s/%d", attemptsPath(id), aid), cookie)
	if !strings.Contains(detail, "No flags.") {
		t.Errorf("detail lacks the empty flags state:\n%s", detail)
	}
}

func TestAttempt_FlagQuestionUnshuffled(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	id := env.openExam(t, examText("none", "score", 3, 2))
	qs, err := env.exams.Questions(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	token, aid := env.startAttempt(t, id, "Ayu")
	if _, err := env.attempts.RecordEvent(t.Context(), token, "copy", qs[1].ID); err != nil {
		t.Fatal(err)
	}
	detail := getOK(t, env, fmt.Sprintf("%s/%d", attemptsPath(id), aid), cookie)
	if !strings.Contains(detail, `<a href="#q2">Question 2</a>`) {
		t.Errorf("detail lacks the one-number question link:\n%s", detail)
	}
	if strings.Contains(detail, "on screen") {
		t.Errorf("unshuffled exam shows a screen number:\n%s", detail)
	}
}
