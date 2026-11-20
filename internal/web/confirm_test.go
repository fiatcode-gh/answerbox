package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
)

// confirmWant is what a confirmation page shows and posts.
type confirmWant struct {
	heading, question string
	notes             []string
	formID, action    string
	csrf, revision    string // revision "" when the form posts none
	submit            string
	danger            bool
	cancel            string
}

// requireConfirm checks a confirmation page against w.
func requireConfirm(t *testing.T, body string, w confirmWant) {
	t.Helper()
	for _, s := range []string{
		"<h1>" + w.heading + "</h1>",
		`<p class="confirm-question">` + w.question + "</p>",
		`<a class="button secondary" href="` + w.cancel + `">Cancel</a>`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("confirmation lacks %s:\n%s", s, body)
		}
	}
	if n := strings.Count(body, `class="confirm-note"`); n != len(w.notes) {
		t.Errorf("confirmation has %d notes, want %d:\n%s", n, len(w.notes), body)
	}
	for _, note := range w.notes {
		if !strings.Contains(body, `<p class="confirm-note">`+note+"</p>") {
			t.Errorf("confirmation lacks the note %q:\n%s", note, body)
		}
	}
	open := fmt.Sprintf(`<form id="%s" method="post" action="%s">`, w.formID, w.action)
	form := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(open) + `.*?</form>`).FindString(body)
	if form == "" {
		t.Fatalf("confirmation lacks %s:\n%s", open, body)
	}
	if !strings.Contains(form, `name="csrf_token" value="`+w.csrf+`"`) {
		t.Errorf("form lacks the CSRF token:\n%s", form)
	}
	if got := strings.Contains(form, `name="revision"`); got != (w.revision != "") {
		t.Errorf("form posts a revision = %v, want %v:\n%s", got, w.revision != "", form)
	}
	if w.revision != "" && !strings.Contains(form, `name="revision" value="`+w.revision+`"`) {
		t.Errorf("form lacks revision %s:\n%s", w.revision, form)
	}
	button := `<button type="submit">` + w.submit + "</button>"
	if w.danger {
		button = `<button type="submit" class="danger">` + w.submit + "</button>"
	}
	if !strings.Contains(form, button) {
		t.Errorf("form lacks %s:\n%s", button, form)
	}
	if strings.Contains(body, "<script") {
		t.Errorf("confirmation loads a script:\n%s", body)
	}
}

var confirmRevisionRe = regexp.MustCompile(`name="revision" value="(\d+)"`)

// postedRevision is the revision the confirmation form of body posts.
func postedRevision(t *testing.T, body string) string {
	t.Helper()
	m := confirmRevisionRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page posts no revision:\n%s", body)
	}
	return m[1]
}

const removeQuestionNote = "This makes a new revision. The question stays readable in earlier revisions, but it cannot be restored from there."

func TestConfirm_RemoveQuestion(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))
	id := pathID(t, detail)
	path := detail + "/questions/2/remove"

	body := getOK(t, env, path, cookie)
	requireConfirm(t, body, confirmWant{
		heading:  "Remove question 2",
		question: "Remove question 2 from ‘Three’?",
		notes:    []string{removeQuestionNote},
		formID:   "remove-question-form", action: path,
		csrf: csrf, revision: "1",
		submit: "Remove question", danger: true,
		cancel: detail + "/edit#q2",
	})
	preview := regexp.MustCompile(`(?s)<section class="preview-question">.*?</section>`).FindString(body)
	for _, want := range []string{"Beta question", "<p>b1</p>", "<p>b2</p>", "✓ Correct"} {
		if !strings.Contains(preview, want) {
			t.Errorf("preview lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Alpha question") || strings.Contains(body, "Gamma question") {
		t.Errorf("confirmation shows another question:\n%s", body)
	}
	if r, q := env.revisionCount(t, id), env.questionCount(t, id); r != 1 || q != 3 {
		t.Errorf("after the GET: revisions %d, questions %d, want 1 and 3", r, q)
	}
	for _, n := range []string{"0", "4", "99", "x"} {
		requireNotFound(t, env, "GET", detail+"/questions/"+n+"/remove", cookie)
	}

	// The page went out of date: another save lands before its form is posted.
	stale := postedRevision(t, body)
	requireRedirect(t, editorPost(env, detail, "/questions/1/move", url.Values{"revision": {"1"}, "to": {"2"}}, cookie, csrf), detail+"/edit")
	requireAlert(t, editorPost(env, detail, "/questions/2/remove", url.Values{"revision": {stale}}, cookie, csrf), http.StatusConflict, staleNotice)
	if q := env.questionCount(t, id); q != 3 {
		t.Errorf("stale removal left %d questions, want 3", q)
	}

	body = getOK(t, env, path, cookie)
	requireRedirect(t, editorPost(env, detail, "/questions/2/remove", url.Values{"revision": {postedRevision(t, body)}}, cookie, csrf), detail+"/edit")
	if q := env.questionCount(t, id); q != 2 {
		t.Errorf("after the removal: %d questions, want 2", q)
	}
}

// The editor shows no Remove for an exam's only question; its typed
// confirmation URL answers what posting the removal answers.
func TestConfirm_RemoveLastQuestionRefused(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "one.md", []byte(oneQuestionExam))
	id := pathID(t, detail)

	get := do(env.h, "GET", detail+"/questions/1/remove", cookie)
	post := editorPost(env, detail, "/questions/1/remove", url.Values{"revision": {"1"}}, cookie, csrf)
	requireAlert(t, post, http.StatusUnprocessableEntity, "Not saved: exam has no questions")
	requireAlert(t, get, http.StatusUnprocessableEntity, "Not saved: exam has no questions")
	if withoutLangSwitch(t, get.Body.String()) != withoutLangSwitch(t, post.Body.String()) {
		t.Errorf("GET answers another page than the POST:\nGET:\n%s\nPOST:\n%s", get.Body.String(), post.Body.String())
	}
	if r, q := env.revisionCount(t, id), env.questionCount(t, id); r != 1 || q != 1 {
		t.Errorf("revisions %d, questions %d, want 1 and 1", r, q)
	}
}

// The editor shows no Remove for a section's only question either; its
// typed confirmation URL answers what posting the removal answers.
func TestConfirm_RemoveOnlyQuestionOfSectionRefused(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	const leftWithout = "Not saved: section %d would be left without questions; remove the section first"
	for _, c := range []struct {
		name, text string
		n, section int
	}{
		{"last section", partsExam, 3, 2},
		{"first section", "# First alone\nshuffle: none\n\n## A\n\n1. Q1\n- a *\n- b\n\n## B\n\n2. Q2\n- a *\n- b\n\n3. Q3\n- a *\n- b\n", 1, 1},
		{"one question in one section", "# Solo\n\n## Only part\n\n1. Only question\n- yes *\n- no\n", 1, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			detail := importExam(t, env, cookie, csrf, "exam.md", []byte(c.text))
			id := pathID(t, detail)
			rest := fmt.Sprintf("/questions/%d/remove", c.n)
			alert := fmt.Sprintf(leftWithout, c.section)

			get := do(env.h, "GET", detail+rest, cookie)
			post := editorPost(env, detail, rest, url.Values{"revision": {"1"}}, cookie, csrf)
			requireAlert(t, post, http.StatusUnprocessableEntity, alert)
			requireAlert(t, get, http.StatusUnprocessableEntity, alert)
			if withoutLangSwitch(t, get.Body.String()) != withoutLangSwitch(t, post.Body.String()) {
				t.Errorf("GET answers another page than the POST:\nGET:\n%s\nPOST:\n%s", get.Body.String(), post.Body.String())
			}
			if r := env.revisionCount(t, id); r != 1 {
				t.Errorf("revisions %d, want 1", r)
			}
		})
	}
}

func TestConfirm_RemoveSection(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "two.md", []byte(twoByTwoExam))
	id := pathID(t, detail)
	const second = "This makes a new revision. The section stays readable in earlier revisions."

	for _, c := range []struct {
		k, title, first string
	}{
		{"1", "Part A", "Its questions join the next section, which then starts at question 1."},
		{"2", "Part B", "Its questions join the section before it."},
	} {
		path := detail + "/sections/" + c.k + "/remove"
		requireConfirm(t, getOK(t, env, path, cookie), confirmWant{
			heading:  "Remove section " + c.k,
			question: "Remove section " + c.k + ", ‘" + c.title + "’, from ‘Two by two’?",
			notes:    []string{c.first, second},
			formID:   "remove-section-form", action: path,
			csrf: csrf, revision: "1",
			submit: "Remove section", danger: true,
			cancel: detail + "/edit#s" + c.k,
		})
	}
	if r := env.revisionCount(t, id); r != 1 {
		t.Errorf("after the GETs: %d revisions, want 1", r)
	}
	for _, n := range []string{"0", "3", "x"} {
		requireNotFound(t, env, "GET", detail+"/sections/"+n+"/remove", cookie)
	}

	body := getOK(t, env, detail+"/sections/2/remove", cookie)
	requireRedirect(t, editorPost(env, detail, "/sections/2/remove", url.Values{"revision": {postedRevision(t, body)}}, cookie, csrf), detail+"/edit")
	if got := newestSummary(t, env, detail, cookie); got != "Section 2 removed" {
		t.Errorf("newest summary = %q", got)
	}

	path := detail + "/sections/1/remove"
	requireConfirm(t, getOK(t, env, path, cookie), confirmWant{
		heading:  "Remove section 1",
		question: "Remove section 1, ‘Part A’, from ‘Two by two’?",
		notes:    []string{"The questions stay. The exam has no sections after this.", second},
		formID:   "remove-section-form", action: path,
		csrf: csrf, revision: "2",
		submit: "Remove section", danger: true,
		cancel: detail + "/edit#s1",
	})
	requireNotFound(t, env, "GET", detail+"/sections/2/remove", cookie)
}

func TestConfirm_RemoveTeacher(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)
	tess := env.addTeacher(t, testTeacherEmail)
	env.ownedExam(t, tess.actor, "Tess quiz", false)
	other := env.addTeacher(t, "other@example.test")
	before := env.allowedTeachers(t)
	path := removePath(tess.actor.TeacherID)

	requireConfirm(t, getOK(t, env, path, admin.cookie), confirmWant{
		heading:  "Remove teacher",
		question: "Remove ‘" + testTeacherEmail + "’ from the teacher list?",
		notes: []string{
			"They are signed out at once and cannot sign in again.",
			"Their exams (1) stay, and admins keep seeing them. An exam that is open stays open until an admin closes it.",
			"Adding the same email again gives back the access and the exams.",
		},
		formID: "remove-teacher-form", action: path,
		csrf:   admin.csrf,
		submit: "Remove teacher", danger: true,
		cancel: "/admin/teachers",
	})

	rec := do(env.h, "GET", removePath(other.actor.TeacherID), tess.cookie)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Page not found") {
		t.Errorf("non-admin got %d, want the 404 page:\n%s", rec.Code, rec.Body.String())
	}
	rec = do(env.h, "GET", removePath(admin.actor.TeacherID), admin.cookie)
	requireAlert(t, rec, http.StatusConflict, adminEmailAlert)
	if !strings.Contains(rec.Body.String(), `<table id="teachers">`) {
		t.Errorf("admin refusal is not the Teachers page:\n%s", rec.Body.String())
	}
	for _, p := range []string{"/admin/teachers/abc/remove", removePath(9999)} {
		requireNotFound(t, env, "GET", p, admin.cookie)
	}
	if got := env.allowedTeachers(t); got != before {
		t.Errorf("teacher list after the GETs = %s, want %s", got, before)
	}
	if rec := do(env.h, "GET", "/admin/exams", tess.cookie); rec.Code != http.StatusOK {
		t.Errorf("teacher's session after the GETs: GET /admin/exams = %d", rec.Code)
	}

	requireRedirect(t, postForm(env.h, path, url.Values{"csrf_token": {admin.csrf}}, admin.cookie), "/admin/teachers")
	if got := env.allowedTeachers(t); strings.Contains(got, testTeacherEmail) {
		t.Errorf("teacher list after the removal = %s", got)
	}
}

func TestConfirm_Archive(t *testing.T) {
	env := newTestEnv(t)
	owner := env.addTeacher(t, "a@example.test")
	stranger := env.addTeacher(t, "b@example.test")
	id, _ := env.ownedExam(t, owner.actor, "Algebra", false)
	sched := exam.Schedule{OpensAt: env.clock.Add(time.Hour), ClosesAt: env.clock.Add(2 * time.Hour)}
	if err := env.exams.SetSchedule(context.Background(), owner.actor, id, sched); err != nil {
		t.Fatal(err)
	}
	before := env.getExam(t, id)
	path := examPath(id) + "/archive"

	requireConfirm(t, getOK(t, env, path, owner.cookie), confirmWant{
		heading:  "Archive exam",
		question: "Archive ‘Algebra’?",
		notes: []string{
			"The exam closes now and its schedule is removed. Students in a running attempt keep their time.",
			"It leaves the exams list. Show archived brings it back into view.",
			"Results, History, Download and Copy keep working.",
			"Unarchive returns it closed, without a schedule.",
		},
		formID: "archive-form", action: path,
		csrf:   owner.csrf,
		submit: "Archive",
		cancel: examPath(id),
	})
	if after := env.getExam(t, id); !reflect.DeepEqual(after, before) {
		t.Errorf("the GET changed the exam:\n%+v\nwant\n%+v", after, before)
	}
	requireNotFound(t, env, "GET", path, stranger.cookie)
	requireNotFound(t, env, "GET", "/admin/exams/abc/archive", owner.cookie)

	requireRedirect(t, postForm(env.h, path, url.Values{"csrf_token": {owner.csrf}}, owner.cookie), examPath(id))
	if !env.getExam(t, id).Archived() {
		t.Fatal("posting the form did not archive the exam")
	}
	requireRedirect(t, do(env.h, "GET", path, owner.cookie), examPath(id))
}

func TestConfirm_DeleteAttempt(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id, _ := env.resultsExam(t, 2)
	_, aid := env.startAttempt(t, id, "Ayu")
	path := attemptPath(id, aid) + "/delete"

	requireConfirm(t, getOK(t, env, path, cookie), confirmWant{
		heading:  "Delete attempt",
		question: "Delete the attempt by ‘Ayu’? This cannot be undone.",
		formID:   "delete-attempt-form", action: path,
		csrf:   csrf,
		submit: "Delete attempt", danger: true,
		cancel: attemptsPath(id),
	})
}

func TestConfirm_Triggers(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)
	tessID := env.addTeacher(t, testTeacherEmail).actor.TeacherID
	removeForm := regexp.MustCompile(`<form[^>]*action="[^"]*/remove"`)

	for _, src := range []string{threeQuestionExam, partsExam} {
		detail := importExam(t, env, admin.cookie, admin.csrf, "exam.md", []byte(src))
		body := getOK(t, env, detail+"/edit", admin.cookie)
		if f := removeForm.FindString(body); f != "" {
			t.Errorf("editor page has %s:\n%s", f, body)
		}
		page := getOK(t, env, detail, admin.cookie)
		if strings.Contains(page, `action="`+detail+`/archive"`) {
			t.Errorf("exam page posts Archive directly:\n%s", page)
		}
		link := `<a id="archive-link" class="quiet" href="` + detail + `/archive">Archive</a>`
		if !strings.Contains(page, link) {
			t.Errorf("exam page lacks %s:\n%s", link, page)
		}
	}

	body := getOK(t, env, "/admin/teachers", admin.cookie)
	if f := removeForm.FindString(body); f != "" {
		t.Errorf("Teachers page has %s:\n%s", f, body)
	}
	link := `<a class="danger-link" href="` + removePath(tessID) + `">Remove</a>`
	if row := teacherRowHTML(body, tessID); !strings.Contains(row, link) {
		t.Errorf("teacher's row lacks %s:\n%s", link, row)
	}
	if row := teacherRowHTML(body, admin.actor.TeacherID); strings.Contains(row, "/remove") {
		t.Errorf("admin row offers Remove:\n%s", row)
	}
}
