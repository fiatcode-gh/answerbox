package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
)

// typedExam has two sections: Part A holds fill-in question 1 accepting
// "Paris" and multiple-choice question 2 keyed at option 2, Part B holds
// fill-in question 3 accepting "42".
const typedExam = "# Typed exam\nshuffle: none\nshow_result: score\n\n" +
	"## Part A\nIntro A.\n\n" +
	"1. Capital of France?\n= Paris\n\n" +
	"2. Two\n- c\n- d *\n\n" +
	"## Part B\nIntro B.\n\n" +
	"3. Six times seven\n= 42\n"

// typeAnswer saves text as the typed answer of q for the attempt of token.
func (env *testEnv) typeAnswer(t *testing.T, token string, q exam.Question, text string) {
	t.Helper()
	if _, err := env.attempts.SaveText(context.Background(), token, q.ID, text); err != nil {
		t.Fatalf("SaveText(%q): %v", text, err)
	}
}

// detailSection returns the attempt page's <section> of file question n.
func detailSection(t *testing.T, body string, n int) string {
	t.Helper()
	sec := regexp.MustCompile(`(?s)<section class="detail-question" id="q` + strconv.Itoa(n) + `">.*?</section>`).FindString(body)
	if sec == "" {
		t.Fatalf("question %d not on the attempt page:\n%s", n, body)
	}
	return sec
}

func overridePath(examID, attemptID int64, n string) string {
	return attemptPath(examID, attemptID) + "/questions/" + n + "/override"
}

// overrides counts the stored teacher marks.
func (env *testEnv) overrides(t *testing.T) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM typed_answer WHERE override_by IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireContains(t *testing.T, what, s string, want, absent []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, s)
		}
	}
	for _, a := range absent {
		if strings.Contains(s, a) {
			t.Errorf("%s has %q:\n%s", what, a, s)
		}
	}
}

func TestAttempt_FillInBlock(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id := env.openExam(t, fillInExam("score", "Lyon", "metre"))
	qs := env.fileQuestions(t, id)
	token, aid := env.startAttempt(t, id, "Ayu")
	env.typeAnswer(t, token, qs[0], "Paris")
	env.pick(t, token, qs[1], 2)
	env.typeAnswer(t, token, qs[2], "<b>x</b>")
	env.submitAttempt(t, token)

	body := getOK(t, env, attemptPath(id, aid), cookie)
	form := func(n int) string {
		return fmt.Sprintf(`<form class="override-form" method="post" action="/admin/exams/%d/attempts/%d/questions/%d/override">`, id, aid, n)
	}
	requireContains(t, "answered right question 1", detailSection(t, body, 1), []string{
		`<div class="detail-fill-in" data-answered="true" data-correct="true">`,
		`<p class="typed-answer">Typed answer: <span class="typed-text">Paris</span></p>`,
		`<p class="accepted-label">Accepted answers</p><ul class="accepted-answers"><li>Lyon</li><li>Paris</li></ul>`,
		`<p class="auto-mark" data-auto="true">Automatic mark: ✓ Right</p>`,
		form(1), `name="csrf_token" value="` + csrf + `"`,
		`<button type="submit" name="mark" value="right" class="secondary">Mark right</button>`,
		`<button type="submit" name="mark" value="wrong" class="secondary">Mark wrong</button>`,
	}, []string{"Remove override", "override-mark", "No answer", "detail-options"})
	requireContains(t, "answered wrong question 3", detailSection(t, body, 3), []string{
		`<div class="detail-fill-in" data-answered="true" data-correct="false">`,
		`<span class="typed-text">&lt;b&gt;x&lt;/b&gt;</span>`,
		`<ul class="accepted-answers"><li>metre</li></ul>`,
		`<p class="auto-mark" data-auto="false">Automatic mark: ✗ Wrong</p>`,
		form(3),
	}, []string{"<b>x", "Remove override"})
	for _, n := range []int{1, 3} {
		f := regexp.MustCompile(`(?s)<form class="override-form".*?</form>`).FindString(detailSection(t, body, n))
		label := strings.Index(f, `<span class="override-label">Mark this answer:</span>`)
		if label < 0 || label < strings.Index(f, `name="csrf_token"`) || label > strings.Index(f, "<button") {
			t.Errorf("question %d: override form lacks the Mark this answer: label before its first button:\n%s", n, f)
		}
	}
	requireContains(t, "unanswered question 4", detailSection(t, body, 4), []string{
		`<span class="no-answer">No answer</span>`,
		`<div class="detail-fill-in" data-answered="false" data-correct="false">`,
		`<ul class="accepted-answers"><li>42</li></ul>`,
	}, []string{"typed-answer", "auto-mark", "<form", "Mark right"})
	requireContains(t, "multiple-choice question 2", detailSection(t, body, 2), []string{
		`<ol class="preview-options detail-options">`, "Student's pick",
	}, []string{"detail-fill-in", "Accepted answers", "<form"})

	budi, bid := env.startAttempt(t, id, "Budi")
	env.typeAnswer(t, budi, qs[0], "Rome")
	body = getOK(t, env, attemptPath(id, bid), cookie)
	requireContains(t, "question 1 in progress", detailSection(t, body, 1), []string{
		`<span class="typed-text">Rome</span>`, `Automatic mark: ✗ Wrong`,
	}, nil)
	requireContains(t, "attempt page in progress", body, nil, []string{"override-form", "Mark right", "/override"})
}

func TestAttempt_Override(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id := env.openExam(t, typedExam)
	qs := env.fileQuestions(t, id)
	ayu, aid := env.startAttempt(t, id, "Ayu")
	env.typeAnswer(t, ayu, qs[0], "Paris")
	env.pick(t, ayu, qs[1], 2)
	env.typeAnswer(t, ayu, qs[2], "42")
	env.submitAttempt(t, ayu)
	env.clock = testStart.Add(time.Minute)
	budi, _ := env.startAttempt(t, id, "Budi")
	env.typeAnswer(t, budi, qs[0], "Rome")
	env.pick(t, budi, qs[1], 2)
	env.typeAnswer(t, budi, qs[2], "42")
	env.submitAttempt(t, budi)

	post := func(mark string) {
		t.Helper()
		rec := postForm(env.h, overridePath(id, aid, "1"), url.Values{"csrf_token": {csrf}, "mark": {mark}}, cookie)
		requireRedirect(t, rec, attemptPath(id, aid)+"#q1")
	}
	scores := func(query string) [][]string {
		t.Helper()
		var out [][]string
		for _, row := range tableRows(getOK(t, env, attemptsPath(id)+query, cookie)) {
			out = append(out, []string{row[1], row[5], row[6], row[7]}) // name, score, two sections
		}
		return out
	}
	csvScores := func() [][]string {
		t.Helper()
		records, err := csv.NewReader(strings.NewReader(getOK(t, env, attemptsPath(id)+".csv", cookie))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		var out [][]string
		for _, r := range records {
			out = append(out, r[:6])
		}
		return out
	}
	header := []string{"name", "status", "score", "total", "Part A", "Part B"}
	requireScores := func(when string, ayuRow, budiRow []string, byScore [][]string, csvAyu string, csvAyuA string) {
		t.Helper()
		if got, want := scores(""), [][]string{ayuRow, budiRow}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: list = %q, want %q", when, got, want)
		}
		if got := scores("?sort=score&dir=asc"); !reflect.DeepEqual(got, byScore) {
			t.Errorf("%s: list by score = %q, want %q", when, got, byScore)
		}
		want := [][]string{header, {"Ayu", "submitted", csvAyu, "3", csvAyuA, "1"}, {"Budi", "submitted", "2", "3", "1", "1"}}
		if got := csvScores(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: CSV = %q, want %q", when, got, want)
		}
	}
	budiRow := []string{"Budi", "2 / 3", "1 / 2", "1 / 1"}
	ayuBefore := []string{"Ayu", "3 / 3", "2 / 2", "1 / 1"}
	ayuAfter := []string{"Ayu", "2 / 3", "1 / 2", "1 / 1"}
	requireScores("before", ayuBefore, budiRow, [][]string{budiRow, ayuBefore}, "3", "2")

	env.clock = testStart.Add(20 * time.Minute)
	post("wrong")
	body := getOK(t, env, attemptPath(id, aid), cookie)
	requireContains(t, "overridden question 1", detailSection(t, body, 1), []string{
		`<div class="detail-fill-in" data-answered="true" data-correct="false">`,
		`<p class="auto-mark" data-auto="true">Automatic mark: ✓ Right</p>`,
		`<p class="override-mark" data-override="false">Marked wrong by admin@example.test on <time>2026-11-14 16:50</time></p>`,
		`<button type="submit" name="mark" value="none" class="secondary">Remove override</button>`,
	}, nil)
	requireContains(t, "attempt page after the mark", body, []string{`<dd class="numeric">2 / 3</dd>`}, nil)
	requireScores("after mark wrong", ayuAfter, budiRow, [][]string{ayuAfter, budiRow}, "2", "1")

	post("none")
	body = getOK(t, env, attemptPath(id, aid), cookie)
	requireContains(t, "question 1 after removing the mark", detailSection(t, body, 1), []string{
		`data-correct="true"`, `Automatic mark: ✓ Right`, "Mark right", "Mark wrong",
	}, []string{"override-mark", "Remove override"})
	requireContains(t, "attempt page after removing the mark", body, []string{`<dd class="numeric">3 / 3</dd>`}, nil)
	requireScores("after removing the mark", ayuBefore, budiRow, [][]string{budiRow, ayuBefore}, "3", "2")
	if n := env.overrides(t); n != 0 {
		t.Fatalf("stored marks = %d, want 0", n)
	}
}

func TestAttempt_OverrideRefusals(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id := env.openExam(t, fillInExam("score", "Lyon", "metre"))
	qs := env.fileQuestions(t, id)
	ayu, aid := env.startAttempt(t, id, "Ayu Lestari")
	env.typeAnswer(t, ayu, qs[0], "Paris")
	env.pick(t, ayu, qs[1], 2)
	env.submitAttempt(t, ayu)
	budi, bid := env.startAttempt(t, id, "Budi")
	env.typeAnswer(t, budi, qs[0], "Paris")

	const inProgress = "This attempt is still in progress. An answer can be marked once it has ended."
	const refused = "Only an answered fill-in question can be marked right or wrong."
	form := func(mark string) url.Values { return url.Values{"csrf_token": {csrf}, "mark": {mark}} }
	for _, c := range []struct {
		what      string
		attemptID int64
		n, mark   string
		status    int
		alert     string
	}{
		{"in progress", bid, "1", "right", http.StatusConflict, inProgress},
		{"multiple choice", aid, "2", "right", http.StatusUnprocessableEntity, refused},
		{"unanswered", aid, "3", "wrong", http.StatusUnprocessableEntity, refused},
		{"mark maybe", aid, "1", "maybe", http.StatusUnprocessableEntity, refused},
	} {
		rec := postForm(env.h, overridePath(id, c.attemptID, c.n), form(c.mark), cookie)
		requireAlert(t, rec, c.status, c.alert)
		if body := rec.Body.String(); !strings.Contains(body, `id="rename-name" name="name" value="`) || !strings.Contains(body, `class="detail-question"`) {
			t.Errorf("%s: refusal is not the attempt page:\n%s", c.what, body)
		}
	}
	if body := postForm(env.h, overridePath(id, aid, "2"), form("right"), cookie).Body.String(); !strings.Contains(body, `name="name" value="Ayu Lestari"`) {
		t.Errorf("refusal page lacks the stored name in the rename field:\n%s", body)
	}

	for _, c := range []struct {
		what      string
		attemptID int64
		n         string
	}{
		{"unknown attempt", bid + 1, "1"},
		{"question 99", aid, "99"},
		{"question x", aid, "x"},
	} {
		if rec := postForm(env.h, overridePath(id, c.attemptID, c.n), form("right"), cookie); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", c.what, rec.Code)
		}
	}
	if rec := postForm(env.h, overridePath(id, aid, "1"), url.Values{"mark": {"right"}}, cookie); rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF token = %d, want 403", rec.Code)
	}
	if n := env.overrides(t); n != 0 {
		t.Fatalf("stored marks = %d, want 0", n)
	}
}
