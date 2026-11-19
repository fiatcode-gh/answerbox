package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	answerInputRe   = regexp.MustCompile(`<input type="text" name="answer" value="([^"]*)" maxlength="200" autocomplete="off" aria-label="Accepted answer (\d+)">`)
	answerRemoveRe  = regexp.MustCompile(`<button type="submit" class="quiet" name="do" value="answer-remove:(\d+)" formnovalidate>Remove</button>`)
	answerAddButton = `<button type="submit" class="secondary" name="do" value="answer-add" formnovalidate>Add accepted answer</button>`
	toFillInButton  = `<button type="submit" class="secondary" name="do" value="kind:fill_in" formnovalidate>Switch to fill-in</button>`
	toChoiceButton  = `<button type="submit" class="secondary" name="do" value="kind:choice" formnovalidate>Switch to multiple choice</button>`
)

// shownAnswers reads the accepted-answer boxes of a question form page in
// order, checking that they are numbered from 1.
func shownAnswers(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for i, m := range answerInputRe.FindAllStringSubmatch(body, -1) {
		if m[2] != strconv.Itoa(i+1) {
			t.Fatalf("answer box %d is labelled Accepted answer %s", i+1, m[2])
		}
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// requireKind checks the form page is a fill-in form when fillIn is true and
// a multiple-choice form otherwise: the labelled kind line, the posted kind,
// the switch button and which list is hidden.
func requireKind(t *testing.T, body string, fillIn bool) {
	t.Helper()
	line := `<span class="kind-label">Question type:</span> <strong>Multiple choice</strong>`
	kind, button := `<input type="hidden" name="kind" value="choice">`, toFillInButton
	options, answers := `<fieldset class="option-list">`, `<fieldset class="answer-list" hidden>`
	if fillIn {
		line = `<span class="kind-label">Question type:</span> <strong>Fill-in</strong>`
		kind, button = `<input type="hidden" name="kind" value="fill_in">`, toChoiceButton
		options, answers = `<fieldset class="option-list" hidden>`, `<fieldset class="answer-list">`
	}
	for _, want := range []string{line, kind, button, options, answers} {
		if !strings.Contains(body, want) {
			t.Errorf("form (fill-in %v) lacks %s:\n%s", fillIn, want, body)
		}
	}
}

func requireAnswers(t *testing.T, body string, want []string) {
	t.Helper()
	if got := shownAnswers(t, body); !slices.Equal(got, want) {
		t.Errorf("answer boxes = %q, want %q", got, want)
	}
}

func TestQuestion_KindSwitch(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)

	body := getOK(t, env, detail+"/questions/2", cookie)
	requireKind(t, body, false)
	requireAnswers(t, body, []string{""})

	opts := []string{"gamma typed", "two\nlines", "epsilon"}
	form := url.Values{"revision": {"1"}, "kind": {"choice"}, "do": {"kind:fill_in"}, "text": {"Typed"}, "option": opts, "correct": {"1"}, "answer": {""}}
	rec := formPost(t, env, detail+"/questions/2", form, nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("do=kind:fill_in = %d, want 200:\n%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	requireKind(t, body, true)
	requireAnswers(t, body, []string{""})
	requireShown(t, body, shownForm{"Typed", []string{"gamma typed", "two\nlines", "epsilon"}, 1})

	form.Set("kind", "fill_in")
	form.Set("do", "kind:choice")
	form["answer"] = []string{"typed answer"}
	form["option"] = []string{"gamma typed", "two\nlines", "epsilon"}
	rec = formPost(t, env, detail+"/questions/2", form, nil, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("do=kind:choice = %d, want 200:\n%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	requireKind(t, body, false)
	requireShown(t, body, shownForm{"Typed", []string{"gamma typed", "two\nlines", "epsilon"}, 1})
	requireAnswers(t, body, []string{"typed answer"})
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestQuestion_AnswerActions(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	post := func(t *testing.T, do string, answers []string) string {
		t.Helper()
		form := url.Values{"revision": {"1"}, "kind": {"fill_in"}, "do": {do}, "text": {"Typed"}, "option": {"", "", "", ""}, "answer": answers}
		rec := formPost(t, env, detail+"/questions/1", form, nil, cookie, csrf)
		if rec.Code != http.StatusOK {
			t.Fatalf("do=%s = %d, want 200:\n%s", do, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	answers := []string{"a1"}
	for len(answers) < 10 {
		body := post(t, "answer-add", answers)
		answers = append(answers, "")
		requireAnswers(t, body, answers)
	}
	body := post(t, "none", answers)
	if strings.Contains(body, `value="answer-add"`) {
		t.Errorf("form with 10 answers offers Add accepted answer")
	}
	if len(answerRemoveRe.FindAllString(body, -1)) != 10 {
		t.Errorf("form with 10 answers lacks a Remove button per row")
	}
	requireAnswers(t, post(t, "answer-add", answers), answers)

	three := []string{"x", "y", "z"}
	for _, c := range []struct {
		do   string
		want []string
	}{
		{"answer-remove:1", []string{"x", "z"}},
		{"answer-remove:0", []string{"y", "z"}},
		{"answer-remove:2", []string{"x", "y"}},
		{"answer-remove:3", three},
		{"answer-remove:-1", three},
		{"answer-remove:x", three},
		{"answer-add", []string{"x", "y", "z", ""}},
	} {
		t.Run(c.do, func(t *testing.T) {
			body := post(t, c.do, three)
			requireAnswers(t, body, c.want)
			if !strings.Contains(body, answerAddButton) {
				t.Errorf("form with %d answers lacks Add accepted answer", len(c.want))
			}
			requireKind(t, body, true)
		})
	}

	body = post(t, "none", []string{"only"})
	if answerRemoveRe.MatchString(body) {
		t.Errorf("form with one answer offers Remove")
	}
	requireAnswers(t, post(t, "answer-remove:0", []string{"only"}), []string{"only"})
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

// fillInSaveForm posts question text as a fill-in question accepting
// answers, with the hidden option list as the browser posts it.
func fillInSaveForm(revision, text string, answers ...string) url.Values {
	return url.Values{"revision": {revision}, "kind": {"fill_in"}, "do": {"save"}, "text": {text}, "option": {"o1", "o2"}, "correct": {"0"}, "answer": answers}
}

// previewSection is the <section> of question n on an editor, exam or
// revision page.
func previewSection(t *testing.T, body string, n int) string {
	t.Helper()
	sec := regexp.MustCompile(`(?s)<section class="preview-question" id="q` + strconv.Itoa(n) + `">.*?</section>`).FindString(body)
	if sec == "" {
		t.Fatalf("no preview of question %d:\n%s", n, body)
	}
	return sec
}

// requireAcceptedPreview checks the preview section shows answers as its
// accepted answers, escaped, and no option list.
func requireAcceptedPreview(t *testing.T, where, sec string, answers []string) {
	t.Helper()
	var items strings.Builder
	for _, a := range answers {
		items.WriteString("<li>" + html.EscapeString(a) + "</li>")
	}
	want := `<div class="preview-answers"><p class="accepted-label">Accepted answers</p><ul class="accepted-answers">` + items.String() + `</ul></div>`
	if !strings.Contains(sec, want) {
		t.Errorf("%s: preview lacks %s:\n%s", where, want, sec)
	}
	if strings.Contains(sec, "preview-options") {
		t.Errorf("%s: fill-in preview shows an option list:\n%s", where, sec)
	}
}

func TestQuestion_FillInSaveFlow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))

	requireRedirect(t, formPost(t, env, detail+"/questions/2", fillInSaveForm("1", "Capital of France?", "Paris", "paris city"), nil, cookie, csrf), detail+"/edit#q2")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 2 || rows[0][4] != "Question 2 edited; accepted answers changed" {
		t.Errorf("history after save = %q", rows)
	}
	want := []string{"Paris", "paris city"}
	requireAcceptedPreview(t, "editor", previewSection(t, getOK(t, env, detail+"/edit", cookie), 2), want)
	requireAcceptedPreview(t, "exam page", previewSection(t, getOK(t, env, detail, cookie), 2), want)
	requireAcceptedPreview(t, "revision 2", previewSection(t, getOK(t, env, detail+"/revisions/2", cookie), 2), want)
	if sec := previewSection(t, getOK(t, env, detail+"/revisions/1", cookie), 2); !strings.Contains(sec, "preview-options") {
		t.Errorf("revision 1 lost the options of question 2:\n%s", sec)
	}
}

func TestQuestion_FillInEditFormShowsAnswers(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "typed.md", []byte("# Typed\n\n1. Capital?\n= Paris\n= <i>Lutetia</i>\n= \"P\" & co\n\n2. Two\n- c\n- d *\n"))

	body := getOK(t, env, detail+"/questions/1", cookie)
	requireKind(t, body, true)
	requireAnswers(t, body, []string{"Paris", "<i>Lutetia</i>", `"P" & co`})
	requireShown(t, body, shownForm{"Capital?", []string{"", "", "", ""}, -1})
	if strings.Contains(body, "<i>Lutetia") {
		t.Errorf("accepted answer is not escaped:\n%s", body)
	}

	body = getOK(t, env, detail+"/questions/2", cookie)
	requireKind(t, body, false)
	requireAnswers(t, body, []string{""})
}

func TestQuestion_FillInRefusedKeepsTypedAnswers(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))
	id := pathID(t, detail)

	rec := formPost(t, env, detail+"/questions/2", fillInSaveForm("1", "Typed fill-in", "first", ""), nil, cookie, csrf)
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Not saved: question 2, accepted answer 2 has no text")
	requireKind(t, rec.Body.String(), true)
	requireAnswers(t, rec.Body.String(), []string{"first", ""})
	requireShown(t, rec.Body.String(), shownForm{"Typed fill-in", []string{"o1", "o2"}, 0})

	if err := env.exams.MoveQuestion(context.Background(), env.owner, id, 1, 2, 1); err != nil {
		t.Fatal(err)
	}
	rec = formPost(t, env, detail+"/questions/2", fillInSaveForm("1", "Typed stale", "a", "b"), nil, cookie, csrf)
	requireAlert(t, rec, http.StatusConflict, staleNotice)
	requireKind(t, rec.Body.String(), true)
	requireAnswers(t, rec.Body.String(), []string{"a", "b"})
	requireShown(t, rec.Body.String(), shownForm{"Typed stale", []string{"o1", "o2"}, 0})
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

func TestQuestion_AddFillIn(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)

	body := getOK(t, env, detail+"/questions/new", cookie)
	requireKind(t, body, false)
	requireAnswers(t, body, []string{""})

	requireRedirect(t, formPost(t, env, detail+"/questions/new", fillInSaveForm("1", "Six times seven", "42"), nil, cookie, csrf), detail+"/edit#q3")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 2 || rows[0][4] != "Question 3 added" {
		t.Errorf("history after add = %q", rows)
	}
	src, err := env.exams.Source(context.Background(), env.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if q := src.Questions[2]; !slices.Equal(q.Answers, []string{"42"}) || len(q.Options) != 0 {
		t.Errorf("added question = %+v, want fill-in accepting 42", q)
	}
}

func TestNewExam_FillInFirstQuestion(t *testing.T) {
	env := newTestEnv(t)
	a := env.addTeacher(t, "a@example.test")
	exams := env.examCount(t)

	body := getOK(t, env, "/admin/exams/new", a.cookie)
	requireKind(t, body, false)
	requireAnswers(t, body, []string{""})

	typed := url.Values{"title": {"Typed title"}, "shuffle": {"none"}, "show_result": {"review"}, "duration": {"30"},
		"kind": {"choice"}, "do": {"kind:fill_in"}, "text": {"First?"}, "option": {"x", "y"}, "correct": {"1"}, "answer": {""}}
	rec := formPost(t, env, "/admin/exams/new", typed, nil, a.cookie, a.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("do=kind:fill_in on the new exam form = %d:\n%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if inputValue(t, body, "exam-title") != "Typed title" || !selected(body, "exam-shuffle", "none") || !selected(body, "exam-show-result", "review") || checked(body, "exam-monitor") || inputValue(t, body, "exam-duration") != "30" {
		t.Errorf("kind switch lost the typed settings:\n%s", body)
	}
	requireKind(t, body, true)
	requireShown(t, body, shownForm{"First?", []string{"x", "y"}, 1})
	if n := env.examCount(t); n != exams {
		t.Errorf("exams = %d, want %d", n, exams)
	}

	typed.Set("kind", "fill_in")
	typed.Set("do", "save")
	typed["answer"] = []string{"one", "1"}
	rec = formPost(t, env, "/admin/exams/new", typed, nil, a.cookie, a.csrf)
	m := newExamPathRe.FindStringSubmatch(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || m == nil {
		t.Fatalf("create = %d Location %q, want 303 /admin/exams/{id}/edit:\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	if rows := revisionRows(getOK(t, env, examPath(id)+"/revisions", a.cookie)); len(rows) != 1 || rows[0][4] != "Created in the editor" {
		t.Errorf("history of the new exam = %q", rows)
	}
	src, err := env.exams.Source(context.Background(), a.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Questions) != 1 || !slices.Equal(src.Questions[0].Answers, []string{"one", "1"}) || len(src.Questions[0].Options) != 0 {
		t.Errorf("created questions = %+v, want one fill-in question", src.Questions)
	}
}

func TestPreview_FillInAccepted(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "mixed.md", []byte("# Mixed\nshuffle: none\n\n1. Tag?\n= <i>\n= a & b\n\n2. Two\n- c\n- d *\n"))

	for _, path := range []string{detail, detail + "/revisions/1", detail + "/edit"} {
		body := getOK(t, env, path, cookie)
		requireAcceptedPreview(t, path, previewSection(t, body, 1), []string{"<i>", "a & b"})
		if sec := previewSection(t, body, 2); !strings.Contains(sec, `<ol class="preview-options">`) || strings.Contains(sec, "accepted-answers") {
			t.Errorf("%s: multiple-choice preview changed:\n%s", path, sec)
		}
	}
}

func TestQuestion_FormatNoteExplainsAnswers(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))

	help := regexp.MustCompile(`(?s)<details class="format-help">.*?</details>`).FindString(getOK(t, env, detail+"/questions/1", cookie))
	for _, want := range []string{"<code>=</code>", "accepted answers", "200 characters"} {
		if !strings.Contains(help, want) {
			t.Errorf("format help lacks %s:\n%s", want, help)
		}
	}
}
