package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fillInExam has four unshuffled questions: 1 fill-in accepting a1 and
// "Paris", 2 multiple choice keyed at option 2, 3 fill-in accepting a3,
// 4 fill-in accepting "42". Two calls differ only in their "=" lines.
func fillInExam(show, a1, a3 string) string {
	return "# Fill-in exam\nshuffle: none\nshow_result: " + show + "\n\n" +
		"1. Capital of France?\n= " + a1 + "\n= Paris\n\n" +
		"2. Two\n- c\n- d *\n\n" +
		"3. Name a unit\n= " + a3 + "\n\n" +
		"4. Six times seven\n= 42\n"
}

func textForm(q int64, text string) url.Values {
	return url.Values{"question": {strconv.FormatInt(q, 10)}, "text": {text}}
}

func (env *testEnv) answerText(cookie http.Header, q int64, text string) *httptest.ResponseRecorder {
	return postForm(env.h, "/exam/answer", textForm(q, text), cookie)
}

// questionSection returns the exam page's <section> of question number n.
func questionSection(t *testing.T, body string, n int) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<section class="question" id="q` + strconv.Itoa(n) + `".*?</section>`)
	sec := re.FindString(body)
	if sec == "" {
		t.Fatalf("question %d not on the page:\n%s", n, body)
	}
	return sec
}

// answerBox is the rendered box of a fill-in question.
func answerBox(id int64, number int, value string) string {
	return fmt.Sprintf(`<div class="answer-box"><label class="answer-label" for="t%d">Type your answer<span class="visually-hidden"> for question %d</span></label>`+
		`<input type="text" class="answer-text" id="t%d" name="t%d" value="%s" maxlength="200" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false" enterkeyhint="done"></div>`,
		id, number, id, id, value)
}

const savedJSON = `{"saved":true,"remaining_ms":3600000}`

func requireUnansweredFillIn(t *testing.T, body string, id int64, n, answered int) {
	t.Helper()
	sec := questionSection(t, body, n)
	for _, want := range []string{answerBox(id, n, ""), `<span class="q-state" data-state="idle">`} {
		if !strings.Contains(sec, want) {
			t.Errorf("question %d lacks %q:\n%s", n, want, sec)
		}
	}
	for _, want := range []string{
		fmt.Sprintf(`<a class="bubble" href="#q%d" data-answered="false">%d</a>`, n, n),
		fmt.Sprintf(`id="answered-count">Answered %d of 4<`, answered),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestExamPage_FillInBox(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, fillInExam("score", "Lyon", "metre"))
	cookie := env.start(t, "Ada")
	body := env.examPage(t, cookie)
	qs := pageQuestions(t, body)
	if len(qs) != 4 || len(qs[0].Options) != 0 || len(qs[1].Options) != 2 {
		t.Fatalf("page questions = %+v", qs)
	}
	for _, n := range []int{1, 3, 4} {
		sec := questionSection(t, body, n)
		if c := strings.Count(sec, "<input"); c != 1 {
			t.Errorf("fill-in question %d has %d inputs, want 1", n, c)
		}
		for _, absent := range []string{`type="radio"`, "<fieldset", "<form"} {
			if strings.Contains(sec, absent) {
				t.Errorf("fill-in question %d has %q", n, absent)
			}
		}
		requireUnansweredFillIn(t, body, qs[n-1].ID, n, 0)
	}
	mc := questionSection(t, body, 2)
	if !strings.Contains(mc, `<fieldset class="options">`) || strings.Contains(mc, "answer-text") || strings.Contains(mc, "answer-box") {
		t.Errorf("multiple-choice question has no fieldset or has a box:\n%s", mc)
	}

	requireJSON(t, env.answerText(cookie, qs[0].ID, "  Paris "), http.StatusOK, savedJSON)
	body = env.examPage(t, cookie)
	sec := questionSection(t, body, 1)
	for _, want := range []string{answerBox(qs[0].ID, 1, "Paris"), `<span class="q-state" data-state="saved">Saved</span>`} {
		if !strings.Contains(sec, want) {
			t.Errorf("saved question lacks %q:\n%s", want, sec)
		}
	}
	for _, want := range []string{`<a class="bubble" href="#q1" data-answered="true">1</a>`, `id="answered-count">Answered 1 of 4<`} {
		if !strings.Contains(body, want) {
			t.Errorf("page after a save lacks %q", want)
		}
	}

	requireJSON(t, env.answerText(cookie, qs[2].ID, `"><script>`), http.StatusOK, savedJSON)
	body = env.examPage(t, cookie)
	if want := answerBox(qs[2].ID, 3, `&#34;&gt;&lt;script&gt;`); !strings.Contains(body, want) {
		t.Errorf("typed markup not escaped; want %q in:\n%s", want, questionSection(t, body, 3))
	}
	if strings.Count(body, "<script") != 1 {
		t.Error("typed <script> reached the page unescaped")
	}
}

func TestAnswer_Text(t *testing.T) {
	env := newTestEnv(t)
	env.openExam(t, fillInExam("score", "Lyon", "metre"))
	cookie := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, cookie))
	fill, mc := qs[0], qs[1]
	invalid := `{"error":"invalid"}`

	requireJSON(t, env.answerText(cookie, fill.ID, "Lyon"), http.StatusOK, savedJSON)
	requireJSON(t, env.answerText(cookie, fill.ID, "Paris"), http.StatusOK, savedJSON)
	if sec := questionSection(t, env.examPage(t, cookie), 1); !strings.Contains(sec, answerBox(fill.ID, 1, "Paris")) {
		t.Errorf("second text did not replace the first:\n%s", sec)
	}
	long := strings.Repeat("é", 200)
	requireJSON(t, env.answerText(cookie, qs[3].ID, long), http.StatusOK, savedJSON)
	if sec := questionSection(t, env.examPage(t, cookie), 4); !strings.Contains(sec, answerBox(qs[3].ID, 4, long)) {
		t.Errorf("a 200-character answer was not stored:\n%s", sec)
	}
	requireJSON(t, env.answerText(cookie, qs[3].ID, ""), http.StatusOK, savedJSON)

	for _, clear := range []string{"", "  "} {
		requireJSON(t, env.answerText(cookie, fill.ID, "Paris"), http.StatusOK, savedJSON)
		requireJSON(t, env.answerText(cookie, fill.ID, clear), http.StatusOK, savedJSON)
		requireUnansweredFillIn(t, env.examPage(t, cookie), fill.ID, 1, 0)
	}

	id := func(v int64) []string { return []string{strconv.FormatInt(v, 10)} }
	for name, form := range map[string]url.Values{
		"option and text":     {"question": id(mc.ID), "option": id(mc.Options[0]), "text": {"x"}},
		"neither":             {"question": id(fill.ID)},
		"text on choice":      textForm(mc.ID, "c"),
		"option on fill-in":   {"question": id(fill.ID), "option": id(mc.Options[0])},
		"201 characters":      textForm(fill.ID, strings.Repeat("x", 201)),
		"tab inside":          textForm(fill.ID, "Pa\tris"),
		"bad question":        textForm(0, "Paris"),
		"unparsable question": {"question": {"x"}, "text": {"Paris"}},
	} {
		rec := postForm(env.h, "/exam/answer", form, cookie)
		if rec.Code != http.StatusUnprocessableEntity || strings.TrimSpace(rec.Body.String()) != invalid {
			t.Errorf("%s: got %d %s, want 422 %s", name, rec.Code, rec.Body.String(), invalid)
		}
	}
	requireUnansweredFillIn(t, env.examPage(t, cookie), fill.ID, 1, 0)

	requireJSON(t, env.answerText(nil, fill.ID, "Paris"), http.StatusUnauthorized, `{"error":"no_attempt"}`)
	requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
	requireJSON(t, env.answerText(cookie, fill.ID, "Paris"), http.StatusConflict, `{"error":"finished"}`)
}

func TestNoKeyLeak_FillInInProgress(t *testing.T) {
	tw := twin{newTestEnv(t), newTestEnv(t)}
	tw.k1.openExam(t, fillInExam("review", "Lyon", "metre"))
	tw.k2.openExam(t, fillInExam("review", "Marseille", "gram"))
	cookie, qs := tw.start(t)
	text := func(name string, q int64, v string) {
		t.Helper()
		rec := tw.step(t, name, func(env *testEnv) *httptest.ResponseRecorder { return env.answerText(cookie, q, v) })
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	text("save Lyon", qs[0].ID, "Lyon")
	text("save metre", qs[2].ID, "metre")
	text("clear 1", qs[0].ID, "")
	tw.step(t, "GET /exam/status", func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/exam/status", cookie) })
	rec := tw.step(t, "GET /exam again", func(env *testEnv) *httptest.ResponseRecorder { return do(env.h, "GET", "/exam", cookie) })
	if body := rec.Body.String(); !strings.Contains(body, `<main id="exam"`) || !strings.Contains(body, `value="metre"`) {
		t.Fatalf("not the exam page with the saved answer:\n%s", body)
	}
}

var reviewQuestionRe = regexp.MustCompile(`(?s)<section class="review-question">.*?</section>`)

// finishFillIn answers question 1 with "  PARIS " (accepted), question 2
// with its key, question 3 with "<i>x</i>" (not accepted), leaves question
// 4 empty, submits and returns the result page.
func finishFillIn(t *testing.T, env *testEnv, show string) (http.Header, []pageQuestion) {
	t.Helper()
	env.openExam(t, fillInExam(show, "Lyon & Co", "metre"))
	cookie := env.start(t, "Ada")
	qs := pageQuestions(t, env.examPage(t, cookie))
	requireJSON(t, env.answerText(cookie, qs[0].ID, "  PARIS "), http.StatusOK, savedJSON)
	requireJSON(t, env.answer(cookie, qs[1].ID, qs[1].Options[1]), http.StatusOK, savedJSON)
	requireJSON(t, env.answerText(cookie, qs[2].ID, "<i>x</i>"), http.StatusOK, savedJSON)
	requireRedirect(t, postForm(env.h, "/exam/submit", nil, cookie), "/exam")
	return cookie, qs
}

func TestResultPage_ReviewFillIn(t *testing.T) {
	env := newTestEnv(t)
	cookie, _ := finishFillIn(t, env, "review")
	body := env.examPage(t, cookie)
	if !strings.Contains(body, `<p id="score">2 / 4</p>`) {
		t.Errorf("score is not 2 / 4:\n%s", body)
	}
	secs := reviewQuestionRe.FindAllString(body, -1)
	if len(secs) != 4 {
		t.Fatalf("review sections = %d, want 4", len(secs))
	}
	for i, c := range []struct {
		want, absent []string
	}{
		{want: []string{
			`<div class="review-fill-in" data-answered="true" data-correct="true">`,
			`<p class="review-typed">Your answer: <span class="typed-text">PARIS</span> <span class="review-mark">✓ Correct</span></p>`,
			`<p class="accepted-label">Accepted answers</p><ul class="accepted-answers"><li>Lyon &amp; Co</li><li>Paris</li></ul></div>`,
		}, absent: []string{"No answer", "review-option"}},
		{want: []string{`<ul class="review-options">`}, absent: []string{"review-fill-in", "Accepted answers"}},
		{want: []string{
			`<div class="review-fill-in" data-answered="true" data-correct="false">`,
			`<p class="review-typed">Your answer: <span class="typed-text">&lt;i&gt;x&lt;/i&gt;</span> <span class="review-mark">✗ Not correct</span></p>`,
			`<ul class="accepted-answers"><li>metre</li></ul>`,
		}, absent: []string{"No answer", "<i>x"}},
		{want: []string{
			`<p class="no-answer">No answer</p>`,
			`<div class="review-fill-in" data-answered="false" data-correct="false"><p class="accepted-label">Accepted answers</p><ul class="accepted-answers"><li>42</li></ul></div>`,
		}, absent: []string{"Your answer:", "review-mark"}},
	} {
		for _, want := range c.want {
			if !strings.Contains(secs[i], want) {
				t.Errorf("review question %d lacks %q:\n%s", i+1, want, secs[i])
			}
		}
		for _, absent := range c.absent {
			if strings.Contains(secs[i], absent) {
				t.Errorf("review question %d has %q:\n%s", i+1, absent, secs[i])
			}
		}
	}

	for _, show := range []string{"score", "none"} {
		env := newTestEnv(t)
		cookie, _ := finishFillIn(t, env, show)
		body := env.examPage(t, cookie)
		for _, absent := range []string{"Accepted answers", "accepted-answers", "review-fill-in", "Your answer:", "PARIS", "Lyon", "metre", "&lt;i&gt;"} {
			if strings.Contains(body, absent) {
				t.Errorf("%s result has %q:\n%s", show, absent, body)
			}
		}
	}
}

func TestResultPage_ScoreIncludesOverride(t *testing.T) {
	env := newTestEnv(t)
	cookie, qs := finishFillIn(t, env, "review")
	res, err := env.db.Exec(`UPDATE typed_answer SET override_correct = 1, override_by = ?, override_at = ?
		WHERE question_id = ?`, env.owner.TeacherID, env.clock.UnixMilli(), qs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("override touched %d rows", n)
	}
	body := env.examPage(t, cookie)
	if !strings.Contains(body, `<p id="score">3 / 4</p>`) {
		t.Errorf("score with the override is not 3 / 4:\n%s", body)
	}
	secs := reviewQuestionRe.FindAllString(body, -1)
	if len(secs) != 4 || !strings.Contains(secs[2], `data-correct="true"`) || !strings.Contains(secs[2], "✓ Correct") {
		t.Errorf("overridden answer is not marked correct:\n%s", body)
	}
	lower := strings.ToLower(body)
	for _, absent := range []string{"overrid", "marked"} {
		if strings.Contains(lower, absent) {
			t.Errorf("result page mentions %q", absent)
		}
	}
}
