package web

import (
	"bytes"
	"context"
	"html"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const threeQuestionExam = `# Three
shuffle: none

1. Alpha question
- a1 *
- a2

2. Beta question
- b1
- b2 *

3. Gamma question
- g1 *
- g2
`

const oneQuestionExam = "# One\n\n1. Only question\n- yes *\n- no\n"

var (
	editorSectionRe = regexp.MustCompile(`(?s)<section class="preview-question" id="q(\d+)">(.*?)</section>`)
	editorFormRe    = regexp.MustCompile(`(?s)<form class="(move-up|move-down|remove-question)"[^>]*>.*?</form>`)
)

// revisionCount is the number of stored revisions of exam id.
func (env *testEnv) revisionCount(t *testing.T, id int64) int {
	t.Helper()
	revs, err := env.exams.Revisions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return len(revs)
}

// questionCount is the number of stored questions of exam id.
func (env *testEnv) questionCount(t *testing.T, id int64) int {
	t.Helper()
	qs, err := env.exams.Questions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return len(qs)
}

// editorPost posts form plus the CSRF token to an editor path of detail.
func editorPost(env *testEnv, detail, rest string, form url.Values, cookie http.Header, csrf string) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", csrf)
	return postForm(env.h, detail+rest, form, cookie)
}

func TestExamPage_EditLink(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	link := `<a id="edit-link" role="button" class="secondary" href="` + detail + `/edit">Edit questions</a>`
	if body := getOK(t, env, detail, cookie); !strings.Contains(body, link) {
		t.Errorf("exam page lacks %s:\n%s", link, body)
	}

	frozen, _ := env.ownedExam(t, env.owner, "Frozen", true)
	if body := getOK(t, env, examPath(frozen), cookie); strings.Contains(body, `id="edit-link"`) {
		t.Errorf("exam with an attempt shows the edit link:\n%s", body)
	}
	old := env.examWithoutRevision(t, "EDTZZZ", false)
	if body := getOK(t, env, examPath(old), cookie); strings.Contains(body, `id="edit-link"`) {
		t.Errorf("exam without a revision shows the edit link:\n%s", body)
	}
}

func TestEditor_PageListsQuestions(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))

	body := getOK(t, env, detail+"/edit", cookie)
	sections := editorSectionRe.FindAllStringSubmatch(body, -1)
	want := []struct{ text, correct string }{{"Alpha question", "a1"}, {"Beta question", "b2"}, {"Gamma question", "g1"}}
	if len(sections) != len(want) {
		t.Fatalf("editor has %d questions, want %d:\n%s", len(sections), len(want), body)
	}
	for i, w := range want {
		s := sections[i]
		if s[1] != strconv.Itoa(i+1) || !strings.Contains(s[2], w.text) {
			t.Errorf("section %d is q%s without %q:\n%s", i+1, s[1], w.text, s[2])
		}
		mark := regexp.MustCompile(`<li data-correct="true"><div class="option-body"><p>` + w.correct + `</p>\s*</div><span class="correct-mark">`)
		if strings.Count(s[2], "✓ Correct") != 1 || !mark.MatchString(s[2]) {
			t.Errorf("question %d: want one correct mark on %s:\n%s", i+1, w.correct, s[2])
		}
		up, down := strings.Contains(s[2], `class="move-up"`), strings.Contains(s[2], `class="move-down"`)
		if up != (i > 0) || down != (i < len(want)-1) {
			t.Errorf("question %d: move-up %v, move-down %v", i+1, up, down)
		}
		if !strings.Contains(s[2], `class="remove-question"`) {
			t.Errorf("question %d has no remove form", i+1)
		}
	}
	middle := sections[1][2]
	for _, want := range []string{
		`action="` + detail + `/questions/2/move"`,
		`name="to" value="1"`,
		`name="to" value="3"`,
		`action="` + detail + `/questions/2/remove"`,
	} {
		if !strings.Contains(middle, want) {
			t.Errorf("question 2 lacks %s:\n%s", want, middle)
		}
	}
	forms := editorFormRe.FindAllString(body, -1)
	if len(forms) != 7 {
		t.Errorf("editor has %d question forms, want 7", len(forms))
	}
	for _, f := range forms {
		if !strings.Contains(f, `name="csrf_token" value="`+csrf+`"`) || !strings.Contains(f, `name="revision" value="1"`) {
			t.Errorf("form lacks the CSRF token or revision 1:\n%s", f)
		}
	}
	for _, want := range []string{
		`<h1>Edit questions</h1>`,
		`<a id="settings-link" role="button" class="secondary" href="` + detail + `/settings">Settings</a>`,
		`href="` + detail + `/revisions"`,
		`Revision 1. Every save makes a new revision.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("editor lacks %s:\n%s", want, body)
		}
	}

	one := importExam(t, env, cookie, csrf, "one.md", []byte(oneQuestionExam))
	if body := getOK(t, env, one+"/edit", cookie); strings.Contains(body, `class="remove-question"`) {
		t.Errorf("one-question exam has a remove form:\n%s", body)
	}
}

func TestEditor_MoveAndRemove(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))

	rec := editorPost(env, detail, "/questions/2/move", url.Values{"revision": {"1"}, "to": {"1"}}, cookie, csrf)
	requireRedirect(t, rec, detail+"/edit")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 2 || rows[0][4] != "Question 2 moved to 1" {
		t.Errorf("history after move = %q", rows)
	}
	sections := editorSectionRe.FindAllStringSubmatch(getOK(t, env, detail+"/edit", cookie), -1)
	if len(sections) != 2 || !strings.Contains(sections[0][2], "Second question") || !strings.Contains(sections[1][2], "First <strong>question</strong>") {
		t.Errorf("order after move is not swapped: %q", sections)
	}

	rec = editorPost(env, detail, "/questions/1/remove", url.Values{"revision": {"2"}}, cookie, csrf)
	requireRedirect(t, rec, detail+"/edit")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 3 || rows[0][4] != "Question 1 removed" {
		t.Errorf("history after remove = %q", rows)
	}
	sections = editorSectionRe.FindAllStringSubmatch(getOK(t, env, detail+"/edit", cookie), -1)
	if len(sections) != 1 || !strings.Contains(sections[0][2], "First <strong>question</strong>") {
		t.Errorf("after remove: %q", sections)
	}
}

func TestEditor_RemoveLastQuestion422(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "one.md", []byte(oneQuestionExam))
	id := pathID(t, detail)

	rec := editorPost(env, detail, "/questions/1/remove", url.Values{"revision": {"1"}}, cookie, csrf)
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Not saved: exam has no questions")
	if !strings.Contains(rec.Body.String(), "<h1>Edit questions</h1>") {
		t.Errorf("refusal is not the editor page:\n%s", rec.Body.String())
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestEditor_StaleMoveRefused(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	if err := env.exams.MoveQuestion(context.Background(), env.owner, id, 1, 2, 1); err != nil {
		t.Fatal(err)
	}

	rec := editorPost(env, detail, "/questions/2/move", url.Values{"revision": {"1"}, "to": {"1"}}, cookie, csrf)
	requireAlert(t, rec, http.StatusConflict, staleNotice)
	if !strings.Contains(rec.Body.String(), `name="revision" value="2"`) {
		t.Errorf("stale refusal does not show revision 2:\n%s", rec.Body.String())
	}
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}

	rec = editorPost(env, detail, "/questions/2/move", url.Values{"revision": {"x"}, "to": {"1"}}, cookie, csrf)
	requireAlert(t, rec, http.StatusConflict, staleNotice)
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

func TestEditor_UnknownQuestion404(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)

	for _, n := range []string{"0", "99", "x"} {
		for _, action := range []string{"move", "remove"} {
			rec := editorPost(env, detail, "/questions/"+n+"/"+action, url.Values{"revision": {"1"}, "to": {"1"}}, cookie, csrf)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s question %s = %d, want 404", action, n, rec.Code)
			}
		}
	}
	for _, to := range []string{"0", "99"} {
		rec := editorPost(env, detail, "/questions/1/move", url.Values{"revision": {"1"}, "to": {to}}, cookie, csrf)
		if rec.Code != http.StatusNotFound {
			t.Errorf("move 1 to %s = %d, want 404", to, rec.Code)
		}
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

// selected reports whether the select with id has option value selected.
func selected(body, id, value string) bool {
	sel := regexp.MustCompile(`(?s)<select[^>]*id="` + id + `"[^>]*>.*?</select>`).FindString(body)
	return regexp.MustCompile(`<option value="` + value + `" selected>`).MatchString(sel)
}

// checked reports whether the checkbox with id is checked.
func checked(body, id string) bool {
	return regexp.MustCompile(`<input[^>]*id="` + id + `"[^>]* checked`).MatchString(body)
}

func TestSettings_FormAndSave(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))

	body := getOK(t, env, detail+"/settings", cookie)
	if got := inputValue(t, body, "exam-title"); got != "Geometry & Co" {
		t.Errorf("title input = %q", got)
	}
	if got := inputValue(t, body, "exam-duration"); got != "60" {
		t.Errorf("duration input = %q", got)
	}
	if !selected(body, "exam-shuffle", "none") || selected(body, "exam-shuffle", "questions") {
		t.Errorf("shuffle select does not select only none:\n%s", body)
	}
	if !selected(body, "exam-show-result", "review") || selected(body, "exam-show-result", "score") {
		t.Errorf("show_result select does not select only review:\n%s", body)
	}
	if !checked(body, "exam-monitor") {
		t.Errorf("monitor is not checked:\n%s", body)
	}
	for _, want := range []string{
		`<form id="settings-form" method="post" action="` + detail + `/settings">`,
		`name="revision" value="1"`,
		`<button type="submit">Save settings</button>`,
		`href="` + detail + `/edit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page lacks %s:\n%s", want, body)
		}
	}

	form := url.Values{"revision": {"1"}, "title": {"Geometry II"}, "shuffle": {"none"}, "show_result": {"review"}, "monitor": {"on"}, "duration": {"45"}}
	requireRedirect(t, editorPost(env, detail, "/settings", form, cookie, csrf), detail+"/edit")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 2 || rows[0][4] != "Settings changed: title, length" {
		t.Errorf("history after settings = %q", rows)
	}
	page := getOK(t, env, detail, cookie)
	if !strings.Contains(page, "<h1>Geometry II</h1>") || !strings.Contains(page, "<dd>45 minutes</dd>") {
		t.Errorf("exam page lacks the new title or length:\n%s", page)
	}

	form = url.Values{"revision": {"2"}, "title": {"Geometry II"}, "shuffle": {"none"}, "show_result": {"review"}, "duration": {"45"}}
	requireRedirect(t, editorPost(env, detail, "/settings", form, cookie, csrf), detail+"/edit")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 3 || rows[0][4] != "Settings changed: monitor" {
		t.Errorf("history after unchecking monitor = %q", rows)
	}
}

func TestSettings_RefusedKeepsTypedValues(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	long := strings.Repeat("x", 201)

	for _, c := range []struct {
		title, duration, alert, input, value string
	}{
		{long, "60", "Not saved: title is longer than 200 characters", "exam-title", long},
		{"Geometry", "abc", "Not saved: duration must be a whole number of minutes from 5 to 240", "exam-duration", "abc"},
	} {
		form := url.Values{"revision": {"1"}, "title": {c.title}, "shuffle": {"none"}, "show_result": {"review"}, "monitor": {"on"}, "duration": {c.duration}}
		rec := editorPost(env, detail, "/settings", form, cookie, csrf)
		requireAlert(t, rec, http.StatusUnprocessableEntity, c.alert)
		if got := inputValue(t, rec.Body.String(), c.input); got != c.value {
			t.Errorf("%s = %q, want the typed %q", c.input, got, c.value)
		}
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestSettings_StaleKeepsTypedValues(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	if err := env.exams.MoveQuestion(context.Background(), env.owner, id, 1, 2, 1); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"revision": {"1"}, "title": {"Typed title"}, "shuffle": {"none"}, "show_result": {"review"}, "monitor": {"on"}, "duration": {"60"}}
	rec := editorPost(env, detail, "/settings", form, cookie, csrf)
	requireAlert(t, rec, http.StatusConflict, staleNotice)
	body := rec.Body.String()
	if got := inputValue(t, body, "exam-title"); got != "Typed title" {
		t.Errorf("title input = %q, want the typed title", got)
	}
	if !strings.Contains(body, `name="revision" value="1"`) {
		t.Errorf("stale settings page does not keep revision 1:\n%s", body)
	}
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

// editorRequest is a request to an editor route with its form for a POST.
type editorRequest struct {
	method, path string
	form         url.Values
}

// editorRequests are the five editor-page and settings routes of exam
// detail with a form that would pass validation.
func editorRequests(detail string) []editorRequest {
	form := url.Values{"revision": {"1"}, "to": {"2"}, "title": {"Edited"}, "shuffle": {"none"}, "show_result": {"score"}, "monitor": {"on"}, "duration": {"30"}}
	return []editorRequest{
		{"GET", detail + "/edit", nil},
		{"GET", detail + "/settings", nil},
		{"POST", detail + "/settings", form},
		{"POST", detail + "/questions/1/move", form},
		{"POST", detail + "/questions/1/remove", form},
	}
}

// questionRequests are the four question-form routes of exam detail with a
// form that would save.
func questionRequests(detail string) []editorRequest {
	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"Edited question"}, "option": {"a", "b"}, "correct": {"0"}}
	return []editorRequest{
		{"GET", detail + "/questions/1", nil},
		{"POST", detail + "/questions/1", form},
		{"GET", detail + "/questions/new", nil},
		{"POST", detail + "/questions/new", form},
	}
}

// sectionRequests are the five section routes of exam detail with a form
// that would save.
func sectionRequests(detail string) []editorRequest {
	form := url.Values{"revision": {"1"}, "title": {"Edited section"}, "text": {"Edited instructions"}}
	return []editorRequest{
		{"GET", detail + "/questions/1/section", nil},
		{"POST", detail + "/questions/1/section", form},
		{"GET", detail + "/sections/1", nil},
		{"POST", detail + "/sections/1", form},
		{"POST", detail + "/sections/1/remove", form},
	}
}

// requireEditorRefused sends reqs for exam id and wants a 409 exam page with
// notice for each, and the exam's revisions and questions unchanged.
func requireEditorRefused(t *testing.T, env *testEnv, id int64, cookie http.Header, csrf, notice string, reqs []editorRequest) {
	t.Helper()
	revs, qs := env.revisionCount(t, id), env.questionCount(t, id)
	for _, req := range reqs {
		var rec *httptest.ResponseRecorder
		if req.method == "GET" {
			rec = do(env.h, "GET", req.path, cookie)
		} else {
			form := url.Values{"csrf_token": {csrf}}
			for k, v := range req.form {
				form[k] = v
			}
			rec = postForm(env.h, req.path, form, cookie)
		}
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			requireAlert(t, rec, http.StatusConflict, notice)
			if !strings.Contains(rec.Body.String(), `id="exam-code"`) {
				t.Errorf("refusal is not the exam page:\n%s", rec.Body.String())
			}
		})
	}
	if n := env.revisionCount(t, id); n != revs {
		t.Errorf("revisions = %d, want %d", n, revs)
	}
	if n := env.questionCount(t, id); n != qs {
		t.Errorf("questions = %d, want %d", n, qs)
	}
}

func TestEditor_FrozenEveryRoute(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id, _ := env.ownedExam(t, env.owner, "Frozen", true)
	requireEditorRefused(t, env, id, cookie, csrf, frozenNotice, editorRequests(examPath(id)))
}

func TestEditor_NoRevisionEveryRoute(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id := env.examWithoutRevision(t, "EDTNRZ", false)
	requireEditorRefused(t, env, id, cookie, csrf, noRevisionNotice, editorRequests(examPath(id)))
}

func TestEditor_RequiresCSRF(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	for _, req := range editorRequests(detail) {
		if req.method != "POST" {
			continue
		}
		if rec := postForm(env.h, req.path, req.form, cookie); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without csrf_token = %d, want 403", req.path, rec.Code)
		}
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

var (
	questionTextRe = regexp.MustCompile(`(?s)<textarea id="question-text" name="text" rows="6">\n(.*?)</textarea>`)
	optionTextRe   = regexp.MustCompile(`(?s)<textarea name="option" rows="2" aria-label="Option (\d+)">\n(.*?)</textarea>`)
	correctRadioRe = regexp.MustCompile(`<input type="radio" name="correct" value="(\d+)"( checked)?>`)
	newExamPathRe  = regexp.MustCompile(`^/admin/exams/([0-9]+)/edit$`)
	editorImageRe  = regexp.MustCompile(`<img src="(/media/[0-9a-f]+/([0-9a-f]+\.png))"`)
)

const imageDroppedNote = "The image was not kept. Choose it again."

// shownForm is the question part of a question form page as shown.
type shownForm struct {
	text    string
	options []string
	correct int // -1 when no radio is checked
}

// shownQuestion reads the question text, the option texts in order and the
// checked radio from a question form page.
func shownQuestion(t *testing.T, body string) shownForm {
	t.Helper()
	m := questionTextRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no question text area opening with a line break:\n%s", body)
	}
	f := shownForm{text: html.UnescapeString(m[1]), correct: -1}
	for i, o := range optionTextRe.FindAllStringSubmatch(body, -1) {
		if o[1] != strconv.Itoa(i+1) {
			t.Fatalf("option text area %d is labelled Option %s", i+1, o[1])
		}
		f.options = append(f.options, html.UnescapeString(o[2]))
	}
	radios := correctRadioRe.FindAllStringSubmatch(body, -1)
	if len(radios) != len(f.options) {
		t.Fatalf("%d correct radios for %d options:\n%s", len(radios), len(f.options), body)
	}
	for i, r := range radios {
		if r[1] != strconv.Itoa(i) {
			t.Fatalf("radio %d has value %s", i, r[1])
		}
		if r[2] != "" {
			if f.correct != -1 {
				t.Fatalf("more than one radio checked:\n%s", body)
			}
			f.correct = i
		}
	}
	return f
}

func requireShown(t *testing.T, body string, want shownForm) {
	t.Helper()
	got := shownQuestion(t, body)
	if got.text != want.text || strings.Join(got.options, "|") != strings.Join(want.options, "|") || len(got.options) != len(want.options) || got.correct != want.correct {
		t.Errorf("form shows %+v, want %+v", got, want)
	}
}

// formRequestMultipart builds a multipart POST of fields, repeated values in
// order, and an "image" part when image is not nil.
func formRequestMultipart(t *testing.T, target string, fields url.Values, image []byte, cookie http.Header) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, values := range fields {
		for _, v := range values {
			if err := mw.WriteField(name, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if image != nil {
		fw, err := mw.CreateFormFile("image", "picture.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(image); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, vs := range cookie {
		req.Header[k] = vs
	}
	return req
}

// formPost posts form plus the CSRF token as a multipart body to path, with
// an image part when image is not nil.
func formPost(t *testing.T, env *testEnv, path string, form url.Values, image []byte, cookie http.Header, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	all := url.Values{"csrf_token": {csrf}}
	for k, v := range form {
		all[k] = v
	}
	return serve(env.h, formRequestMultipart(t, path, all, image, cookie))
}

// mediaFiles lists every file under the media root.
func mediaFiles(t *testing.T, env *testEnv) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(env.exams.MediaFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const escapesExam = `# Escapes
shuffle: none

1. Plain
- p1 *
- p2

2. Is 1 < 2 & "quoted"?
Second line
- x < y
- y > x *
- z
`

func TestQuestion_EditFormShowsSource(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "escapes.md", []byte(escapesExam))

	body := getOK(t, env, detail+"/questions/2", cookie)
	requireShown(t, body, shownForm{
		text:    "Is 1 < 2 & \"quoted\"?\nSecond line",
		options: []string{"x < y", "y > x", "z"},
		correct: 1,
	})
	if !strings.Contains(body, "Is 1 &lt; 2 &amp; ") {
		t.Errorf("question text is not escaped:\n%s", body)
	}
	form := regexp.MustCompile(`(?s)<form id="question-form" method="post" enctype="multipart/form-data" action="` + detail + `/questions/2">(.*?)</form>`).FindStringSubmatch(body)
	if form == nil {
		t.Fatalf("no question form posting to question 2:\n%s", body)
	}
	first := regexp.MustCompile(`<button[^>]*>`).FindString(form[1])
	if !strings.Contains(first, `name="do" value="save"`) {
		t.Errorf("first button of the form is %s, want the save button", first)
	}
	for _, want := range []string{
		`name="csrf_token" value="` + csrf + `"`,
		`name="revision" value="1"`,
		`<h1>Question 2</h1>`,
		`<button type="submit" id="save" name="do" value="save">Save question</button>`,
		`href="` + detail + `/edit"`,
	} {
		if !strings.Contains(form[0]+body, want) {
			t.Errorf("question page lacks %s:\n%s", want, body)
		}
	}
	if all, broken := strings.Count(body, "<textarea"), len(regexp.MustCompile(`<textarea[^>]*>\n`).FindAllString(body, -1)); all == 0 || all != broken {
		t.Errorf("%d of %d text areas open with a line break", broken, all)
	}
}

func TestQuestion_SaveFlow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))

	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"Beta edited"}, "option": {"b1", "b2"}, "correct": {"0"}}
	requireRedirect(t, formPost(t, env, detail+"/questions/2", form, nil, cookie, csrf), detail+"/edit#q2")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 2 || rows[0][4] != "Question 2 edited; correct option changed" {
		t.Errorf("history after save = %q", rows)
	}
	sections := editorSectionRe.FindAllStringSubmatch(getOK(t, env, detail+"/edit", cookie), -1)
	if len(sections) != 3 || !strings.Contains(sections[1][2], "Beta edited") {
		t.Errorf("editor does not show the new text: %q", sections)
	}
}

func TestQuestion_CodeBlockOnFirstLine(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))
	id := pathID(t, detail)

	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"```\r\nprint(1)\r\n```\r\nWhat?"},
		"option": {"```go\r\nx := 1\r\n```", "b2"}, "correct": {"0"}}
	requireRedirect(t, formPost(t, env, detail+"/questions/2", form, nil, cookie, csrf), detail+"/edit#q2")
	sections := editorSectionRe.FindAllStringSubmatch(getOK(t, env, detail+"/edit", cookie), -1)
	if len(sections) != 3 || !strings.Contains(sections[1][2], `<pre><code class="language-go">x := 1`) {
		t.Errorf("editor does not show the code block option: %q", sections)
	}

	body := getOK(t, env, detail+"/questions/2", cookie)
	requireShown(t, body, shownForm{"\n```\nprint(1)\n```\nWhat?", []string{"\n```go\nx := 1\n```", "b2"}, 0})
	if !strings.Contains(body, `name="revision" value="2"`) {
		t.Errorf("question form is not on revision 2:\n%s", body)
	}
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	again := url.Values{"revision": {"2"}, "do": {"save"}, "text": {crlf("\n```\nprint(1)\n```\nWhat?")},
		"option": {crlf("\n```go\nx := 1\n```"), "b2"}, "correct": {"0"}}
	requireRedirect(t, formPost(t, env, detail+"/questions/2", again, nil, cookie, csrf), detail+"/edit#q2")
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

func TestQuestion_AddFlow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))

	body := getOK(t, env, detail+"/questions/new", cookie)
	requireShown(t, body, shownForm{options: []string{"", "", "", ""}, correct: -1})
	for _, want := range []string{`<h1>New question 3</h1>`, `action="` + detail + `/questions/new"`, `name="revision" value="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("new question page lacks %s:\n%s", want, body)
		}
	}

	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"Third question"}, "option": {"t1", "t2"}, "correct": {"1"}}
	requireRedirect(t, formPost(t, env, detail+"/questions/new", form, nil, cookie, csrf), detail+"/edit#q3")
	if rows := revisionRows(getOK(t, env, detail+"/revisions", cookie)); len(rows) != 2 || rows[0][4] != "Question 3 added" {
		t.Errorf("history after add = %q", rows)
	}
}

func TestQuestion_OptionActions(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	const typed = "Typed text\nsecond line"
	opts := []string{"gamma", "delta", "epsilon"}

	for _, c := range []struct {
		do, correct string
		want        shownForm
	}{
		{"add", "1", shownForm{typed, []string{"gamma", "delta", "epsilon", ""}, 1}},
		{"remove:1", "2", shownForm{typed, []string{"gamma", "epsilon"}, 1}},
		{"remove:1", "1", shownForm{typed, []string{"gamma", "epsilon"}, -1}},
		{"remove:1", "0", shownForm{typed, []string{"gamma", "epsilon"}, 0}},
		{"up:1", "1", shownForm{typed, []string{"delta", "gamma", "epsilon"}, 0}},
		{"up:1", "0", shownForm{typed, []string{"delta", "gamma", "epsilon"}, 1}},
		{"down:0", "1", shownForm{typed, []string{"delta", "gamma", "epsilon"}, 0}},
		{"down:1", "1", shownForm{typed, []string{"gamma", "epsilon", "delta"}, 2}},
		{"remove:9", "1", shownForm{typed, opts, 1}},
		{"remove:x", "1", shownForm{typed, opts, 1}},
		{"up:0", "1", shownForm{typed, opts, 1}},
		{"down:2", "1", shownForm{typed, opts, 1}},
		{"bogus", "1", shownForm{typed, opts, 1}},
		{"add", "x", shownForm{typed, []string{"gamma", "delta", "epsilon", ""}, -1}},
	} {
		form := url.Values{"revision": {"1"}, "do": {c.do}, "text": {typed}, "option": opts, "correct": {c.correct}}
		rec := formPost(t, env, detail+"/questions/2", form, nil, cookie, csrf)
		if rec.Code != http.StatusOK {
			t.Fatalf("do=%s = %d, want 200:\n%s", c.do, rec.Code, rec.Body.String())
		}
		t.Run(c.do+" correct "+c.correct, func(t *testing.T) {
			requireShown(t, rec.Body.String(), c.want)
			if !strings.Contains(rec.Body.String(), `name="revision" value="1"`) {
				t.Errorf("revision 1 not echoed")
			}
		})
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestQuestion_OptionButtons(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))

	body := getOK(t, env, detail+"/questions/1", cookie)
	for _, want := range []string{
		`<button type="submit" class="secondary" name="do" value="down:0" formnovalidate>Down</button>`,
		`<button type="submit" class="secondary" name="do" value="up:1" formnovalidate>Up</button>`,
		`<button type="submit" class="secondary" name="do" value="add" formnovalidate>Add option</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("two-option form lacks %s", want)
		}
	}
	for _, absent := range []string{`value="up:0"`, `value="down:1"`, `value="remove:`} {
		if strings.Contains(body, absent) {
			t.Errorf("two-option form has %s", absent)
		}
	}
	if !strings.Contains(getOK(t, env, detail+"/questions/2", cookie), `<button type="submit" class="secondary" name="do" value="remove:2" formnovalidate>Remove</button>`) {
		t.Errorf("three-option form lacks Remove")
	}

	ten := make([]string, 10)
	rec := formPost(t, env, detail+"/questions/1", url.Values{"revision": {"1"}, "do": {"none"}, "option": ten}, nil, cookie, csrf)
	if strings.Contains(rec.Body.String(), `value="add"`) {
		t.Errorf("form with %d options offers Add option", len(ten))
	}
}

func TestQuestion_RefusedKeepsTypedText(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))
	id := pathID(t, detail)
	shape := `question 2: the text cannot be saved as written: a line that starts with "-" would become an option, and an option that ends with "*" would become the correct one; write \- or \* instead`

	for _, c := range []struct {
		text    string
		options []string
		correct string
		alert   string
	}{
		{"Typed beta", []string{"b1 typed", "b2 typed"}, "", "Not saved: question 2 has no correct option"},
		{"Typed beta", []string{"only"}, "0", "Not saved: question 2 needs at least two options"},
		{"Which?\r\n- not this", []string{"b1 typed", "b2 typed"}, "0", "Not saved: " + html.EscapeString(shape)},
	} {
		form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {c.text}, "option": c.options, "correct": {c.correct}}
		rec := formPost(t, env, detail+"/questions/2", form, nil, cookie, csrf)
		requireAlert(t, rec, http.StatusUnprocessableEntity, c.alert)
		correct := -1
		if c.correct != "" {
			correct = 0
		}
		requireShown(t, rec.Body.String(), shownForm{c.text, c.options, correct})
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestQuestion_StaleKeepsTypedText(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	if err := env.exams.MoveQuestion(context.Background(), env.owner, id, 1, 2, 1); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"Typed stale"}, "option": {"s1", "s2"}, "correct": {"1"}}
	rec := formPost(t, env, detail+"/questions/1", form, nil, cookie, csrf)
	requireAlert(t, rec, http.StatusConflict, staleNotice)
	requireShown(t, rec.Body.String(), shownForm{"Typed stale", []string{"s1", "s2"}, 1})
	if !strings.Contains(rec.Body.String(), `name="revision" value="1"`) {
		t.Errorf("stale form does not keep revision 1:\n%s", rec.Body.String())
	}
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

func TestQuestion_UnknownNumber404(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)

	for _, n := range []string{"0", "99", "x"} {
		requireNotFound(t, env, "GET", detail+"/questions/"+n, cookie)
		form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"Q"}, "option": {"a", "b"}, "correct": {"0"}}
		if rec := formPost(t, env, detail+"/questions/"+n, form, nil, cookie, csrf); rec.Code != http.StatusNotFound {
			t.Errorf("POST question %s = %d, want 404", n, rec.Code)
		}
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestQuestion_ImageUpload(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)

	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"With image"}, "option": {"a", "b"}, "correct": {"0"}}
	requireRedirect(t, formPost(t, env, detail+"/questions/1", form, testPNG(t), cookie, csrf), detail+"/edit#q1")

	m := editorImageRe.FindStringSubmatch(getOK(t, env, detail+"/edit", cookie))
	if m == nil {
		t.Fatal("editor page shows no uploaded image")
	}
	if rec := do(env.h, "GET", m[1], cookie); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", m[1], rec.Code)
	}
	src, err := env.exams.Source(context.Background(), env.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if text := src.Questions[0].Text; !strings.HasSuffix(text, "![]("+m[2]+")") {
		t.Errorf("stored text = %q, want it to end with the image reference", text)
	}
	if rec := do(env.h, "GET", detail+"/download", cookie); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("download = %d %q, want a zip", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestQuestion_RefusedImageStoresNoFile(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)

	form := url.Values{"revision": {"1"}, "do": {"save"}, "text": {"Typed with image"}, "option": {"a", "b"}, "correct": {"0"}}
	rec := formPost(t, env, detail+"/questions/1", form, []byte("not an image at all"), cookie, csrf)
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Not saved: image is not a PNG, JPEG, GIF or WebP file")
	requireShown(t, rec.Body.String(), shownForm{"Typed with image", []string{"a", "b"}, 0})
	if !strings.Contains(rec.Body.String(), imageDroppedNote) {
		t.Errorf("refusal does not say the image was dropped:\n%s", rec.Body.String())
	}

	form.Set("do", "add")
	rec = formPost(t, env, detail+"/questions/1", form, testPNG(t), cookie, csrf)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), imageDroppedNote) {
		t.Errorf("option action with an image = %d without the dropped note:\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(getOK(t, env, detail+"/questions/1", cookie), imageDroppedNote) {
		t.Errorf("a fresh form says an image was dropped")
	}
	if files := mediaFiles(t, env); len(files) != 0 {
		t.Errorf("media files = %q, want none", files)
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
}

func TestNewExam_Flow(t *testing.T) {
	env := newTestEnv(t)
	a := env.addTeacher(t, "a@example.test")
	b := env.addTeacher(t, "b@example.test")

	link := `<a id="new-exam" role="button" class="secondary" href="/admin/exams/new">New exam</a>`
	if body := getOK(t, env, "/admin/exams", a.cookie); !strings.Contains(body, link) || !strings.Contains(body, "No exams yet.") {
		t.Errorf("exams list lacks %s or the empty-state sentence:\n%s", link, body)
	}

	body := getOK(t, env, "/admin/exams/new", a.cookie)
	if !selected(body, "exam-shuffle", "questions") || !selected(body, "exam-show-result", "score") || !checked(body, "exam-monitor") {
		t.Errorf("new exam form lacks the default settings:\n%s", body)
	}
	if got := inputValue(t, body, "exam-duration"); got != "60" {
		t.Errorf("duration = %q, want 60", got)
	}
	if got := inputValue(t, body, "exam-title"); got != "" {
		t.Errorf("title = %q, want empty", got)
	}
	requireShown(t, body, shownForm{options: []string{"", "", "", ""}, correct: -1})
	for _, want := range []string{
		`<form id="new-exam-form" method="post" enctype="multipart/form-data" action="/admin/exams/new">`,
		`<h1>New exam</h1>`,
		`<button type="submit" id="save" name="do" value="save">Create exam</button>`,
		`name="csrf_token" value="` + a.csrf + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("new exam page lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="revision"`) {
		t.Errorf("new exam form posts a revision")
	}
	form := regexp.MustCompile(`(?s)<form id="new-exam-form".*?</form>`).FindString(body)
	if first := regexp.MustCompile(`<button[^>]*>`).FindString(form); !strings.Contains(first, `name="do" value="save"`) {
		t.Errorf("first button of the new exam form is %s", first)
	}

	typed := url.Values{"title": {"Typed title"}, "shuffle": {"none"}, "show_result": {"review"}, "duration": {"30"}, "do": {"add"}, "text": {"First?"}, "option": {"x", "y"}, "correct": {"1"}}
	rec := formPost(t, env, "/admin/exams/new", typed, nil, a.cookie, a.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("do=add on the new exam form = %d:\n%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if inputValue(t, body, "exam-title") != "Typed title" || !selected(body, "exam-shuffle", "none") || !selected(body, "exam-show-result", "review") || checked(body, "exam-monitor") || inputValue(t, body, "exam-duration") != "30" {
		t.Errorf("option action lost the typed settings:\n%s", body)
	}
	requireShown(t, body, shownForm{"First?", []string{"x", "y", ""}, 1})

	typed.Set("do", "save")
	rec = formPost(t, env, "/admin/exams/new", typed, nil, a.cookie, a.csrf)
	m := newExamPathRe.FindStringSubmatch(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || m == nil {
		t.Fatalf("create = %d Location %q, want 303 /admin/exams/{id}/edit:\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	e, err := env.exams.Get(context.Background(), a.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Open || e.OwnerID != a.actor.TeacherID || e.Title != "Typed title" || e.DurationMinutes != 30 {
		t.Errorf("created exam = %+v, want closed, owned by a, as typed", e)
	}
	if rows := revisionRows(getOK(t, env, examPath(id)+"/revisions", a.cookie)); len(rows) != 1 || rows[0][4] != "Created in the editor" {
		t.Errorf("history of the new exam = %q", rows)
	}
	requireNotFound(t, env, "GET", examPath(id), b.cookie)
}

func TestNewExam_RefusedKeepsTypedValues(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	exams := env.examCount(t)

	for _, c := range []struct {
		title   string
		options []string
		alert   string
	}{
		{"", []string{"x typed", "y typed"}, "Not saved: the exam needs a title"},
		{"Typed title", []string{"only"}, "Not saved: question 1 needs at least two options"},
	} {
		form := url.Values{"title": {c.title}, "shuffle": {"none"}, "show_result": {"review"}, "duration": {"30"}, "do": {"save"}, "text": {"Typed question"}, "option": c.options, "correct": {"0"}}
		rec := formPost(t, env, "/admin/exams/new", form, testPNG(t), cookie, csrf)
		requireAlert(t, rec, http.StatusUnprocessableEntity, c.alert)
		body := rec.Body.String()
		if inputValue(t, body, "exam-title") != c.title || !selected(body, "exam-shuffle", "none") || !selected(body, "exam-show-result", "review") || checked(body, "exam-monitor") || inputValue(t, body, "exam-duration") != "30" {
			t.Errorf("refusal lost the typed settings:\n%s", body)
		}
		requireShown(t, body, shownForm{"Typed question", c.options, 0})
		if !strings.Contains(body, imageDroppedNote) {
			t.Errorf("refusal does not say the image was dropped")
		}
	}
	if n := env.examCount(t); n != exams {
		t.Errorf("exams = %d, want %d", n, exams)
	}
	if files := mediaFiles(t, env); len(files) != 0 {
		t.Errorf("media files = %q, want none", files)
	}
}

func TestQuestion_FrozenAndNoRevision(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	frozen, _ := env.ownedExam(t, env.owner, "Frozen", true)
	requireEditorRefused(t, env, frozen, cookie, csrf, frozenNotice, questionRequests(examPath(frozen)))
	old := env.examWithoutRevision(t, "EDTQNR", false)
	requireEditorRefused(t, env, old, cookie, csrf, noRevisionNotice, questionRequests(examPath(old)))
}

func TestQuestionAndNewExam_RequireCSRF(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := pathID(t, detail)
	exams := env.examCount(t)

	newExam := url.Values{"title": {"New"}, "shuffle": {"none"}, "show_result": {"score"}, "monitor": {"on"}, "duration": {"30"}, "do": {"save"}, "text": {"Q"}, "option": {"a", "b"}, "correct": {"0"}}
	reqs := append(questionRequests(detail), editorRequest{"POST", "/admin/exams/new", newExam})
	for _, req := range reqs {
		if req.method != "POST" {
			continue
		}
		if rec := serve(env.h, formRequestMultipart(t, req.path, req.form, nil, cookie)); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without csrf_token = %d, want 403", req.path, rec.Code)
		}
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
	if n := env.examCount(t); n != exams {
		t.Errorf("exams = %d, want %d", n, exams)
	}
}
