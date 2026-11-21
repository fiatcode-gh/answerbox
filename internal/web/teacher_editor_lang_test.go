package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

var (
	// hookTagRe finds the tags whose attributes the tests and scripts read.
	hookTagRe = regexp.MustCompile(`<(?:form|input|textarea|select|option|button|a|fieldset|div|section|main)\b[^>]*>`)
	// ariaLabelRe is the one attribute of those tags that is worded.
	ariaLabelRe = regexp.MustCompile(` aria-label="[^"]*"`)
	textareaRe  = regexp.MustCompile(`<textarea[^>]*>`)
	firstButton = regexp.MustCompile(`<button[^>]*>`)
	examIDRe    = regexp.MustCompile(`data-exam-id="([0-9]+)"`)
	dataRevRe   = regexp.MustCompile(`data-revision="([0-9]+)"`)
)

// hookTags are the form, field, link and container tags of body without
// their aria-label, in order, inside <main>.
func hookTags(body string) []string {
	if i, j := strings.Index(body, "<main"), strings.Index(body, "</main>"); i >= 0 && j > i {
		body = body[i:j]
	}
	tags := hookTagRe.FindAllString(body, -1)
	for i, tag := range tags {
		tags[i] = ariaLabelRe.ReplaceAllString(tag, "")
	}
	return tags
}

// requireSameTags wants the Indonesian page to carry the English page's
// forms, fields, classes, ids and values unchanged.
func requireSameTags(t *testing.T, label, id, en string) {
	t.Helper()
	got, want := hookTags(id), hookTags(en)
	if !slices.Equal(got, want) {
		t.Errorf("%s: tags differ between languages:\nid %q\nen %q", label, got, want)
	}
}

// requireEditorTraps checks the question-form traps: the first button of
// the form is the hidden save button and every textarea opens on a line
// break.
func requireEditorTraps(t *testing.T, label, body, formID string) {
	t.Helper()
	form := regexp.MustCompile(`(?s)<form id="` + formID + `".*?</form>`).FindString(body)
	if first := firstButton.FindString(form); first != `<button type="submit" name="do" value="save" class="visually-hidden" tabindex="-1" aria-hidden="true">` {
		t.Errorf("%s: first button of #%s is %s", label, formID, first)
	}
	for _, loc := range textareaRe.FindAllStringIndex(body, -1) {
		if !strings.HasPrefix(body[loc[1]:], "\n") {
			t.Errorf("%s: %s does not start with a line break", label, body[loc[0]:loc[1]])
		}
	}
}

func pageExamID(t *testing.T, body string) int64 {
	t.Helper()
	m := examIDRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page has no data-exam-id")
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

func revisionsPath(id int64) string { return examPath(id) + "/revisions" }

func TestEditorPage_Indonesian(t *testing.T) {
	id := fixturePages(t, langID, "editor")["editor"]
	en := fixturePages(t, langEN, "editor")["editor"]
	examID := pageExamID(t, id)
	rev, _ := strconv.Atoi(dataRevRe.FindStringSubmatch(id)[1])
	requireIndonesian(t, "editor", id,
		tx(t, "editor.title", "Ujian Fisika Baru").in("<title>{} · Answerbox</title>"),
		tx(t, "editor.heading").in("<h1>{}</h1>"),
		tx(t, "editor.settings").in(`/settings">{}</a>`),
		tx(t, "editor.add_question").in(`/questions/new">{}</a></p></div>`),
		tx(t, "editor.add_question").in(`<p class="list-end"><a class="button secondary" href="/admin/exams/`+strconv.FormatInt(examID, 10)+`/questions/new">{}</a></p>`),
		thx(t, "editor.revision_note_html", rev, revisionsPath(examID)).in(`<p class="zone-note">{}</p>`),
		tx(t, "editor.edit_section").in(`/sections/1">{}</a>`),
		tx(t, "editor.remove_section").in(`/sections/2/remove">{}</a>`),
		tx(t, "editor.question", 3).in(`<p class="eyebrow">{}</p>`),
		tx(t, "editor.edit").in(`/questions/1">{}</a>`),
		tx(t, "editor.move_up").in(`<button type="submit" class="quiet">{}</button>`),
		tx(t, "editor.move_down").in(`<button type="submit" class="quiet">{}</button>`),
		tx(t, "editor.start_section").in(`/section">{}</a>`),
		tx(t, "editor.remove").in(`/questions/1/remove">{}</a>`))
	for _, class := range []string{"edit-question", "move-up", "move-down", "start-section", "remove-question", "edit-section", "remove-section"} {
		n := strings.Count(en, `class="`+class)
		if n == 0 || strings.Count(id, `class="`+class) != n {
			t.Errorf("class %s: %d in Indonesian, %d in English", class, strings.Count(id, `class="`+class), n)
		}
	}
	requireSameTags(t, "editor", id, en)
}

func TestQuestionForm_Indonesian(t *testing.T) {
	names := []string{"question-choice", "question-fill-in", "question-new", "question-new-add"}
	id, en := fixturePages(t, langID, names...), fixturePages(t, langEN, names...)
	choice := id["question-choice"]
	examID := pageExamID(t, choice)
	requireIndonesian(t, "choice", choice,
		gox(t, "question.heading", 1).in("<title>{}: Ujian Fisika Baru · Answerbox</title>"),
		gox(t, "question.heading", 1).in("<h1>{}</h1>"),
		tx(t, "editor.title", "Ujian Fisika Baru").in(`/edit">← {}</a>`),
		tx(t, "editor.save").in(`aria-hidden="true">{}</button>`),
		tx(t, "question.save").in(`<button type="submit" id="save" name="do" value="save">{}</button>`),
		tx(t, "editor.cancel").in(`<a class="quiet" href="/admin/exams/`+strconv.FormatInt(examID, 10)+`/edit">{}</a>`),
		tx(t, "question.type").in(`<span class="kind-label">{}</span>`),
		tx(t, "question.kind.choice").in("<strong>{}</strong>"),
		tx(t, "question.switch_to_fill_in").in(`value="kind:fill_in" formnovalidate>{}</button>`),
		tx(t, "question.text_label").in(`<label for="question-text">{}</label>`),
		tx(t, "question.options").in("<legend>{}</legend>"),
		tx(t, "question.correct").in(` checked> {}</label>`),
		tx(t, "question.option_label", 2).in(`aria-label="{}"`),
		tx(t, "question.up").in(`value="up:1" formnovalidate>{}</button>`),
		tx(t, "question.down").in(`value="down:0" formnovalidate>{}</button>`),
		tx(t, "question.add_option").in(`value="add" formnovalidate>{}</button>`),
		tx(t, "question.answers").in("<legend>{}</legend>"),
		tx(t, "question.answer_label", 1).in(`aria-label="{}"`),
		tx(t, "question.answers_note").in("<small>{}</small>"),
		tx(t, "editor.image_label").in(`<label for="question-image">{}</label>`),
		tx(t, "question.image_note").in(`<small id="question-image-note">{}</small>`),
		tx(t, "editor.format_help").in("<summary>{}</summary>"),
		thx(t, "question.format.inline_html").in("<li>{}</li>"),
		thx(t, "question.format.code_html").in("<li>{}</li>"),
		thx(t, "question.format.images_html").in("<li>{}</li>"),
		thx(t, "question.format.options_html").in("<li>{}</li>"),
		thx(t, "question.format.fill_in_html").in("<li>{}</li>"),
		thx(t, "editor.saving_note_html", revisionsPath(examID)).in(`<p class="zone-note">{}</p>`))
	requireIndonesian(t, "fill-in", id["question-fill-in"],
		tx(t, "question.kind.fill_in").in("<strong>{}</strong>"),
		tx(t, "question.switch_to_choice").in(`value="kind:choice" formnovalidate>{}</button>`),
		tx(t, "question.add_answer").in(`value="answer-add" formnovalidate>{}</button>`))
	requireIndonesian(t, "new", id["question-new"],
		gox(t, "question.heading_new", 5).in("<h1>{}</h1>"),
		tx(t, "question.remove").in(`value="remove:0" formnovalidate>{}</button>`))
	for _, name := range names {
		requireSameTags(t, name, id[name], en[name])
		requireEditorTraps(t, name, id[name], "question-form")
	}

	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "kuis.md", []byte(oneQuestionExam))
	const typed = "Teks yang saya ketik"
	for _, c := range []struct {
		do   string
		want []shown
	}{
		{"add", []shown{tx(t, "question.option_label", 3).in(`aria-label="{}"`), tx(t, "question.kind.choice").in("<strong>{}</strong>")}},
		{"kind:fill_in", []shown{tx(t, "question.kind.fill_in").in("<strong>{}</strong>"), tx(t, "question.answer_label", 1).in(`aria-label="{}"`)}},
		{"answer-add", []shown{tx(t, "question.answer_label", 2).in(`aria-label="{}"`), tx(t, "question.add_answer").in(`formnovalidate>{}</button>`)}},
	} {
		form := url.Values{"revision": {"1"}, "do": {c.do}, "kind": {"choice"}, "text": {typed}, "option": {"ya", "tidak"}, "correct": {"0"}, "answer": {"jawab"}}
		rec := formPost(t, env, detail+"/questions/1", form, nil, langHeader(cookie, langID), csrf)
		if rec.Code != http.StatusOK {
			t.Fatalf("do=%s: %d", c.do, rec.Code)
		}
		body := rec.Body.String()
		requireIndonesian(t, "do="+c.do, body, append(c.want,
			gox(t, "question.heading", 1).in("<h1>{}</h1>"),
			tx(t, "question.save").in(`value="save">{}</button>`))...)
		if !strings.Contains(body, ">\n"+typed+"</textarea>") {
			t.Errorf("do=%s: the typed text is gone", c.do)
		}
		requireEditorTraps(t, "do="+c.do, body, "question-form")
	}
}

// editorRefusals are the refusals of the editor forms, made on a fresh
// state, with the English and Indonesian alerts.
func editorRefusals(t *testing.T) []refusal {
	t.Helper()
	ctx := context.Background()
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	exam := func(text string) string { return importExam(t, env, cookie, csrf, "ujian.md", []byte(text)) }
	notSaved := func(e *examfile.Error) shown {
		return shown{
			argEscaper.Replace(localizer{langID}.T("editor.not_saved", localizer{langID}.FileError(e))),
			argEscaper.Replace("Not saved: " + e.Msg),
		}
	}
	type sender = func(lang) *httptest.ResponseRecorder
	multipart := func(path string, form url.Values, image []byte) sender {
		return func(l lang) *httptest.ResponseRecorder {
			return formPost(t, env, path, form, image, langHeader(cookie, l), csrf)
		}
	}
	get := func(path string) sender {
		return func(l lang) *httptest.ResponseRecorder { return do(env.h, "GET", path, langHeader(cookie, l)) }
	}
	post := func(path string, form url.Values) sender {
		return func(l lang) *httptest.ResponseRecorder {
			return editorPost(env, path, "", form, langHeader(cookie, l), csrf)
		}
	}

	threeQ := exam(threeQuestionExam)
	stale := exam(threeQuestionExam)
	staleID := pathID(t, stale)
	if err := env.exams.MoveQuestion(ctx, env.owner, staleID, 1, 1, 2); err != nil {
		t.Fatal(err)
	}
	parts := exam(partsExam)
	one := exam(oneQuestionExam)
	noCorrect := url.Values{"revision": {"1"}, "do": {"save"}, "kind": {"choice"}, "text": {"Soal tanpa jawaban"}, "option": {"a", "b"}}
	typed := url.Values{"revision": {"1"}, "do": {"save"}, "kind": {"choice"}, "text": {"Soal"}, "option": {"a", "b"}, "correct": {"0"}}
	newExam := url.Values{"do": {"save"}, "title": {""}, "shuffle": {"none"}, "show_result": {"score"}, "duration": {"60"}, "kind": {"choice"}, "text": {"Soal"}, "option": {"a", "b"}, "correct": {"0"}}
	return []refusal{
		{"no correct option", multipart(threeQ+"/questions/1", noCorrect, nil), http.StatusUnprocessableEntity, notSaved(examfile.NewError(examfile.CodeQuestionNoCorrect, 0, 1))},
		{"stale question", multipart(stale+"/questions/1", typed, nil), http.StatusConflict, gox(t, "editor.stale")},
		{"stale move", post(stale+"/questions/2/move", url.Values{"revision": {"1"}, "to": {"1"}}), http.StatusConflict, gox(t, "editor.stale")},
		{"image type", multipart(threeQ+"/questions/1", typed, []byte("bukan gambar")), http.StatusUnprocessableEntity, notSaved(examfile.NewError(examfile.CodeEditorImageType, 0))},
		{"section left empty", get(parts + "/questions/3/remove"), http.StatusUnprocessableEntity, notSaved(examfile.NewError(examfile.CodeEditorSectionLeftEmpty, 0, 2))},
		{"only question", get(one + "/questions/1/remove"), http.StatusUnprocessableEntity, notSaved(examfile.NewError(examfile.CodeNoQuestions, 0))},
		{"new exam without title", multipart("/admin/exams/new", newExam, nil), http.StatusUnprocessableEntity, notSaved(examfile.NewError(examfile.CodeEditorTitleMissing, 0))},
	}
}

func TestEditor_RefusalsIndonesian(t *testing.T) {
	for _, r := range editorRefusals(t) {
		if r.alert.id == r.alert.en {
			t.Errorf("%s: the Indonesian alert is the English one: %q", r.name, r.alert.en)
		}
		if strings.Contains(r.alert.id, "baris") {
			t.Errorf("%s: the Indonesian alert names a line: %q", r.name, r.alert.id)
		}
		for _, l := range []lang{langEN, langID} {
			rec := r.send(l)
			if rec.Code != r.status {
				t.Errorf("%s in %s: status %d, want %d", r.name, l, rec.Code, r.status)
			}
			want := map[lang]string{langEN: r.alert.en, langID: r.alert.id}[l]
			if body := rec.Body.String(); !strings.Contains(body, `<div role="alert" class="alert">`+want+`</div>`) {
				t.Errorf("%s in %s: lacks the alert %q:\n%s", r.name, l, want, body)
			}
		}
	}
}

// requireNotSavedIndonesian wants body to show a file-rule refusal in
// Indonesian.
func requireNotSavedIndonesian(t *testing.T, label, body string) {
	t.Helper()
	prefix := strings.TrimSuffix(idText(t, "editor.not_saved"), "%s")
	if !strings.Contains(body, `<div role="alert" class="alert">`+argEscaper.Replace(prefix)) || strings.Contains(body, "Not saved") {
		t.Errorf("%s: the refusal is not in Indonesian:\n%s", label, body)
	}
}

func TestSectionForm_Indonesian(t *testing.T) {
	names := []string{"section-new", "section-edit", "section-refused"}
	id, en := fixturePages(t, langID, names...), fixturePages(t, langEN, names...)
	edit := id["section-edit"]
	examID := pageExamID(t, edit)
	requireIndonesian(t, "section edit", edit,
		gox(t, "section.heading", 1).in("<title>{}: Ujian Fisika Baru · Answerbox</title>"),
		gox(t, "section.heading", 1).in("<h1>{}</h1>"),
		tx(t, "editor.title", "Ujian Fisika Baru").in(`/edit">← {}</a>`),
		tx(t, "section.title_label").in(`<label for="section-title">{}`+"\n"),
		tx(t, "section.text_label").in(`<label for="section-text">{}</label>`),
		tx(t, "editor.image_label").in(`<label for="section-image">{}</label>`),
		tx(t, "section.image_note").in(`<small id="section-image-note">{}</small>`),
		tx(t, "editor.format_help").in("<summary>{}</summary>"),
		thx(t, "section.format.same_html").in("<li>{}</li>"),
		thx(t, "section.format.list_html").in("<li>{}</li>"),
		tx(t, "section.save").in(`<button type="submit" id="save">{}</button>`),
		tx(t, "editor.cancel").in(`/edit">{}</a></div>`),
		thx(t, "editor.saving_note_html", revisionsPath(examID)).in(`<p class="zone-note">{}</p>`))
	requireIndonesian(t, "section new", id["section-new"], gox(t, "section.heading_new", 2).in("<h1>{}</h1>"))
	requireNotSavedIndonesian(t, "section refused", id["section-refused"])
	for _, name := range names {
		requireSameTags(t, name, id[name], en[name])
	}

	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "bagian.md", []byte(partsExam))
	rec := formPost(t, env, detail+"/sections/1", url.Values{"revision": {"1"}, "title": {"Bagian A"}, "text": {"Petunjuk"}}, []byte("bukan gambar"), langHeader(cookie, langID), csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("section with a bad image = %d", rec.Code)
	}
	requireIndonesian(t, "section image", rec.Body.String(),
		tx(t, "editor.image_dropped").in(`<p class="image-dropped">{}</p>`))
}

func TestSettingsForm_Indonesian(t *testing.T) {
	names := []string{"settings", "settings-refused"}
	id, en := fixturePages(t, langID, names...), fixturePages(t, langEN, names...)
	settings := id["settings"]
	examID := pageExamID(t, settings)
	requireIndonesian(t, "settings", settings,
		tx(t, "editor.settings_title", "Ujian Fisika Baru").in("<title>{} · Answerbox</title>"),
		tx(t, "editor.title", "Ujian Fisika Baru").in(`/edit">← {}</a>`),
		tx(t, "editor.settings").in("<h1>{}</h1>"),
		tx(t, "editor.field.title").in(`<label for="exam-title" class="settings-title">{}`+"\n"),
		tx(t, "editor.field.shuffle").in(`<label for="exam-shuffle">{}`+"\n"),
		tx(t, "editor.field.show_result").in(`<label for="exam-show-result">{}`+"\n"),
		tx(t, "editor.field.duration").in(`<label for="exam-duration">{}`+"\n"),
		tx(t, "editor.field.monitor").in("checked>\n    {}\n  </label>"),
		gox(t, "setting.shuffle.none").in(`<option value="none" selected>{}</option>`),
		gox(t, "setting.shuffle.questions").in(`<option value="questions">{}</option>`),
		gox(t, "setting.shuffle.questions_options").in(`<option value="questions_options">{}</option>`),
		gox(t, "setting.show_result.none").in(`<option value="none">{}</option>`),
		gox(t, "setting.show_result.score").in(`<option value="score" selected>{}</option>`),
		gox(t, "setting.show_result.review").in(`<option value="review">{}</option>`),
		tx(t, "editor.save_settings").in(`<button type="submit">{}</button>`),
		thx(t, "editor.saving_note_html", revisionsPath(examID)).in(`<p class="zone-note">{}</p>`))
	requireNotSavedIndonesian(t, "settings refused", id["settings-refused"])
	for _, name := range names {
		requireSameTags(t, name, id[name], en[name])
	}
}

func TestNewExamForm_Indonesian(t *testing.T) {
	names := []string{"exam-new", "exam-new-refused"}
	id, en := fixturePages(t, langID, names...), fixturePages(t, langEN, names...)
	requireIndonesian(t, "new exam", id["exam-new"],
		tx(t, "editor.new_exam").in("<title>{} · Answerbox</title>"),
		tx(t, "editor.new_exam").in("<h1>{}</h1>"),
		tx(t, "editor.all_exams").in(`<a href="/admin/exams">← {}</a>`),
		tx(t, "editor.save").in(`aria-hidden="true">{}</button>`),
		tx(t, "editor.question", 1).in("<h2>{}</h2>"),
		tx(t, "editor.field.title").in(`<label for="exam-title" class="settings-title">{}`+"\n"),
		tx(t, "question.text_label").in(`<label for="question-text">{}</label>`),
		tx(t, "editor.create_exam").in(`<button type="submit" id="save" name="do" value="save">{}</button>`),
		tx(t, "editor.cancel").in(`<a class="quiet" href="/admin/exams">{}</a>`),
		tx(t, "editor.new_exam_note").in(`<p class="zone-note">{}</p>`))
	requireNotSavedIndonesian(t, "new exam refused", id["exam-new-refused"])
	for _, name := range names {
		requireSameTags(t, name, id[name], en[name])
		requireEditorTraps(t, name, id[name], "new-exam-form")
	}
}

func TestEditorConfirm_Indonesian(t *testing.T) {
	names := []string{"confirm-remove-question", "confirm-remove-section"}
	id, en := fixturePages(t, langID, names...), fixturePages(t, langEN, names...)
	note := `<p class="confirm-note">{}</p>`
	requireIndonesian(t, "remove question", id["confirm-remove-question"],
		gox(t, "editor.confirm_remove_question.heading", 1).in("<title>{} · Answerbox</title>"),
		gox(t, "editor.confirm_remove_question.heading", 1).in("<h1>{}</h1>"),
		gox(t, "editor.confirm_remove_question.question", 1, "Ujian Fisika Baru").in(`<p class="confirm-question">{}</p>`),
		gox(t, "editor.confirm_remove_question.note").in(note),
		gox(t, "editor.confirm_remove_question.submit").in(`<button type="submit" class="danger">{}</button>`))
	requireIndonesian(t, "remove section", id["confirm-remove-section"],
		gox(t, "editor.confirm_remove_section.heading", 2).in("<h1>{}</h1>"),
		gox(t, "editor.confirm_remove_section.question", 2, "Bagian Dua", "Ujian Fisika Baru").in(`<p class="confirm-question">{}</p>`),
		gox(t, "editor.confirm_remove_section.note_join_before").in(note),
		gox(t, "editor.confirm_remove_section.note_revision").in(note),
		gox(t, "editor.confirm_remove_section.submit").in(`<button type="submit" class="danger">{}</button>`))
	if !strings.Contains(id["confirm-remove-section"], "‘Bagian Dua’") {
		t.Errorf("remove section: the title is not in ‘ ’")
	}
	for _, name := range names {
		got, want := confirmFormRe.FindString(id[name]), confirmFormRe.FindString(en[name])
		if want == "" || got != want {
			t.Errorf("%s: form %q, English %q", name, got, want)
		}
	}

	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	two := importExam(t, env, cookie, csrf, "dua.md", []byte(twoByTwoExam))
	solo := importExam(t, env, cookie, csrf, "solo.md", []byte("# Solo\n\n## Satu-satunya\n\n1. Soal\n- ya *\n- tidak\n"))
	for _, c := range []struct {
		path string
		note shown
	}{
		{two + "/sections/1/remove", gox(t, "editor.confirm_remove_section.note_join_next")},
		{solo + "/sections/1/remove", gox(t, "editor.confirm_remove_section.note_no_sections")},
	} {
		idBody := do(env.h, "GET", c.path, langHeader(cookie, langID)).Body.String()
		enBody := do(env.h, "GET", c.path, langHeader(cookie, langEN)).Body.String()
		requireIndonesian(t, c.path, idBody, c.note.in(note), gox(t, "editor.confirm_remove_section.note_revision").in(note))
		if !strings.Contains(enBody, c.note.in(note).en) {
			t.Errorf("%s: English lacks %q", c.path, c.note.en)
		}
		if got, want := confirmFormRe.FindString(idBody), confirmFormRe.FindString(enBody); want == "" || got != want {
			t.Errorf("%s: form %q, English %q", c.path, got, want)
		}
	}
}

func TestEditor_FrozenAndNoRevisionIndonesian(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	header := langHeader(cookie, langID)
	frozen, _ := env.ownedExam(t, env.owner, "Beku", true)
	old := env.examWithoutRevision(t, "EDTNRZ", false)
	all := func(id int64) []editorRequest {
		d := examPath(id)
		return slices.Concat(editorRequests(d), questionRequests(d), sectionRequests(d))
	}
	requireEditorRefused(t, env, frozen, header, csrf, gox(t, "examdetail.frozen").id, all(frozen))
	requireEditorRefused(t, env, old, header, csrf, gox(t, "examdetail.no_revision").id, all(old))
}
