package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// partsExam has "Part A" with instructions over questions 1 and 2 and
// "Part B" without instructions over question 3.
const partsExam = `# Parts
shuffle: none

## Part A
Read **carefully**.

1. Alpha question
- a1 *
- a2

2. Beta question
- b1
- b2 *

## Part B

3. Gamma question
- g1 *
- g2
`

// twoByTwoExam has two sections of two questions each.
const twoByTwoExam = `# Two by two
shuffle: none

## Part A

1. Q1
- a *
- b

2. Q2
- a *
- b

## Part B

3. Q3
- a *
- b

4. Q4
- a *
- b
`

var sectionTextRe = regexp.MustCompile(`(?s)<textarea id="section-text" name="text" rows="6">\n(.*?)</textarea>`)

// shownSectionText is the text of the section form's text area, which must
// open with a line break.
func shownSectionText(t *testing.T, body string) string {
	t.Helper()
	m := sectionTextRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no section text area opening with a line break:\n%s", body)
	}
	return html.UnescapeString(m[1])
}

// editorHead is the heading block of section k on the editor page, up to
// the question that follows it; "" when there is none.
func editorHead(body string, k string) string {
	start := strings.Index(body, `<div class="section-head editor-section" id="s`+k+`">`)
	if start < 0 {
		return ""
	}
	rest := body[start:]
	if end := strings.Index(rest, `<section class="preview-question"`); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// requireBefore wants a before b in body, both present.
func requireBefore(t *testing.T, body, a, b string) {
	t.Helper()
	i, j := strings.Index(body, a), strings.Index(body, b)
	if i < 0 || j < 0 || i > j {
		t.Errorf("want %s (at %d) before %s (at %d):\n%s", a, i, b, j, body)
	}
}

// newestSummary is the summary of the newest revision of exam detail.
func newestSummary(t *testing.T, env *testEnv, detail string, cookie http.Header) string {
	t.Helper()
	rows := revisionRows(getOK(t, env, detail+"/revisions", cookie))
	if len(rows) == 0 {
		t.Fatal("history has no rows")
	}
	return rows[0][4]
}

func TestEditor_SectionsOnPage(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))

	body := getOK(t, env, detail+"/edit", cookie)
	requireBefore(t, body, `id="s1"`, `id="q1"`)
	requireBefore(t, body, `id="q2"`, `id="s2"`)
	requireBefore(t, body, `id="s2"`, `id="q3"`)
	for k, want := range map[string][]string{
		"1": {
			`<h2 class="section-title">Part A</h2>`,
			`<div class="section-body question-body"><p>Read <strong>carefully</strong>.</p>`,
			`<a class="edit-section" href="` + detail + `/sections/1">Edit section</a>`,
			`<form class="remove-section" method="post" action="` + detail + `/sections/1/remove">`,
		},
		"2": {
			`<h2 class="section-title">Part B</h2>`,
			`<a class="edit-section" href="` + detail + `/sections/2">Edit section</a>`,
			`<form class="remove-section" method="post" action="` + detail + `/sections/2/remove">`,
		},
	} {
		head := editorHead(body, k)
		for _, w := range want {
			if !strings.Contains(head, w) {
				t.Errorf("section %s heading lacks %s:\n%s", k, w, head)
			}
		}
		form := regexp.MustCompile(`(?s)<form class="remove-section".*?</form>`).FindString(head)
		if !strings.Contains(form, `name="csrf_token" value="`+csrf+`"`) || !strings.Contains(form, `name="revision" value="1"`) || !strings.Contains(form, `>Remove section</button>`) {
			t.Errorf("section %s remove form lacks the CSRF token, revision 1 or its button:\n%s", k, form)
		}
	}
	if strings.Contains(editorHead(body, "2"), `section-body`) {
		t.Errorf("section 2 without instructions shows a body")
	}

	qs := editorSectionRe.FindAllStringSubmatch(body, -1)
	if len(qs) != 3 {
		t.Fatalf("editor has %d questions, want 3", len(qs))
	}
	want := []struct{ start, up, down, remove bool }{
		{false, false, true, true},
		{true, true, true, true},
		{false, false, false, false},
	}
	for i, w := range want {
		q := qs[i][2]
		start := strings.Contains(q, `class="start-section"`)
		up, down := strings.Contains(q, `class="move-up"`), strings.Contains(q, `class="move-down"`)
		remove := strings.Contains(q, `class="remove-question"`)
		if start != w.start || up != w.up || down != w.down || remove != w.remove {
			t.Errorf("question %d: start %v up %v down %v remove %v, want %+v", i+1, start, up, down, remove, w)
		}
	}
	link := `<a class="start-section" href="` + detail + `/questions/2/section">Start a section here</a>`
	if !strings.Contains(qs[1][2], link) || !strings.Contains(qs[1][2], `name="to" value="1"`) || !strings.Contains(qs[1][2], `name="to" value="3"`) {
		t.Errorf("question 2 lacks %s or its move targets:\n%s", link, qs[1][2])
	}

	plain := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))
	body = getOK(t, env, plain+"/edit", cookie)
	if strings.Contains(body, "editor-section") {
		t.Errorf("exam without sections shows a section heading:\n%s", body)
	}
	for i, q := range editorSectionRe.FindAllStringSubmatch(body, -1) {
		if got := strings.Contains(q[2], `class="start-section"`); got != (i == 0) {
			t.Errorf("question %d of an exam without sections: start-section %v", i+1, got)
		}
	}
	if !strings.Contains(body, `<a class="start-section" href="`+plain+`/questions/1/section">Start a section here</a>`) {
		t.Errorf("question 1 lacks the start link:\n%s", body)
	}
}

func TestEditor_AloneQuestionInEarlierSection(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "alone.md", []byte("# Alone\n## A\n1. Q1\n- a *\n- b\n## B\n2. Q2\n- a *\n- b\n3. Q3\n- a *\n- b\n"))

	qs := editorSectionRe.FindAllStringSubmatch(getOK(t, env, detail+"/edit", cookie), -1)
	if len(qs) != 3 {
		t.Fatalf("editor has %d questions, want 3", len(qs))
	}
	q := qs[0][2]
	for _, class := range []string{"move-up", "move-down", "remove-question"} {
		if strings.Contains(q, `class="`+class+`"`) {
			t.Errorf("only question of section A, before section B, shows %s:\n%s", class, q)
		}
	}
}

func TestSection_StartFlow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))

	body := getOK(t, env, detail+"/questions/1/section", cookie)
	for _, want := range []string{
		`<main class="container admin section-page"`,
		`<h1>New section at question 1</h1>`,
		`<form id="section-form" method="post" enctype="multipart/form-data" action="` + detail + `/questions/1/section">`,
		`name="csrf_token" value="` + csrf + `"`,
		`name="revision" value="1"`,
		`<input type="file" id="section-image" name="image" accept="image/png,image/jpeg,image/gif,image/webp"`,
		`<button type="submit" id="save">Save section</button>`,
		`href="` + detail + `/edit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("start form lacks %s:\n%s", want, body)
		}
	}
	if got := inputValue(t, body, "section-title"); got != "" {
		t.Errorf("title = %q, want empty", got)
	}
	if got := shownSectionText(t, body); got != "" {
		t.Errorf("text = %q, want empty", got)
	}

	form := url.Values{"revision": {"1"}, "title": {"Bagian A"}, "text": {"Pilih satu."}}
	requireRedirect(t, formPost(t, env, detail+"/questions/1/section", form, nil, cookie, csrf), detail+"/edit#q1")
	if got := newestSummary(t, env, detail, cookie); got != "Section 1 started at question 1" {
		t.Errorf("newest summary = %q", got)
	}
	head := editorHead(getOK(t, env, detail+"/edit", cookie), "1")
	if !strings.Contains(head, `<h2 class="section-title">Bagian A</h2>`) || !strings.Contains(head, `<p>Pilih satu.</p>`) {
		t.Errorf("editor heading after start:\n%s", head)
	}

	for _, n := range []string{"1", "9", "x"} {
		requireNotFound(t, env, "GET", detail+"/questions/"+n+"/section", cookie)
	}
}

func TestSection_EditFlow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))
	id := pathID(t, detail)

	body := getOK(t, env, detail+"/sections/1", cookie)
	if !strings.Contains(body, `<h1>Section 1</h1>`) || !strings.Contains(body, `action="`+detail+`/sections/1"`) {
		t.Errorf("section form lacks its heading or action:\n%s", body)
	}
	if got := inputValue(t, body, "section-title"); got != "Part A" {
		t.Errorf("title = %q, want Part A", got)
	}
	if got := shownSectionText(t, body); got != "Read **carefully**." {
		t.Errorf("text = %q, want the stored instructions", got)
	}

	// A code block on the first line is shown as saved.
	form := url.Values{"revision": {"1"}, "title": {"Part A2"}, "text": {"```\ncode\n```"}}
	requireRedirect(t, formPost(t, env, detail+"/sections/1", form, nil, cookie, csrf), detail+"/edit#s1")
	if got := newestSummary(t, env, detail, cookie); got != "Section 1 edited" {
		t.Errorf("newest summary = %q", got)
	}
	body = getOK(t, env, detail+"/sections/1", cookie)
	if got := shownSectionText(t, body); got != "```\ncode\n```" {
		t.Errorf("text = %q, want the saved code block", got)
	}
	if got := inputValue(t, body, "section-title"); got != "Part A2" {
		t.Errorf("title = %q, want Part A2", got)
	}

	requireNotFound(t, env, "GET", detail+"/sections/3", cookie)
	requireNotFound(t, env, "GET", detail+"/sections/x", cookie)
	if rec := formPost(t, env, detail+"/sections/3", url.Values{"revision": {"2"}, "title": {"T"}}, nil, cookie, csrf); rec.Code != http.StatusNotFound {
		t.Errorf("POST section 3 = %d, want 404", rec.Code)
	}
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

func TestSection_RemoveFlow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))

	requireRedirect(t, editorPost(env, detail, "/sections/1/remove", url.Values{"revision": {"1"}}, cookie, csrf), detail+"/edit")
	if got := newestSummary(t, env, detail, cookie); got != "Section 1 removed" {
		t.Errorf("newest summary = %q", got)
	}
	body := getOK(t, env, detail+"/edit", cookie)
	if n := strings.Count(body, "editor-section"); n != 1 {
		t.Errorf("editor has %d section headings, want 1", n)
	}
	requireBefore(t, body, `id="s1"`, `id="q1"`)
	if !strings.Contains(editorHead(body, "1"), `<h2 class="section-title">Part B</h2>`) {
		t.Errorf("remaining heading is not Part B:\n%s", body)
	}

	requireRedirect(t, editorPost(env, detail, "/sections/1/remove", url.Values{"revision": {"2"}}, cookie, csrf), detail+"/edit")
	if body := getOK(t, env, detail+"/edit", cookie); strings.Contains(body, "editor-section") {
		t.Errorf("editor still shows a section heading:\n%s", body)
	}
	if rec := editorPost(env, detail, "/sections/1/remove", url.Values{"revision": {"3"}}, cookie, csrf); rec.Code != http.StatusNotFound {
		t.Errorf("removing a missing section = %d, want 404", rec.Code)
	}
}

func TestSection_MoveAcross(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "two.md", []byte(twoByTwoExam))

	requireRedirect(t, editorPost(env, detail, "/questions/3/move", url.Values{"revision": {"1"}, "to": {"2"}}, cookie, csrf), detail+"/edit")
	if got := newestSummary(t, env, detail, cookie); got != "Question 3 moved to section 1" {
		t.Errorf("newest summary = %q", got)
	}
	body := getOK(t, env, detail+"/edit", cookie)
	requireBefore(t, body, `id="q3"`, `id="s2"`)
	requireBefore(t, body, `id="s2"`, `id="q4"`)
}

func TestSection_RefusedKeepsTypedValues(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "three.md", []byte(threeQuestionExam))
	id := pathID(t, detail)

	form := url.Values{"revision": {"1"}, "title": {"   "}, "text": {"Typed text"}}
	rec := formPost(t, env, detail+"/questions/1/section", form, testPNG(t), cookie, csrf)
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Not saved: section title is empty")
	body := rec.Body.String()
	if got := shownSectionText(t, body); got != "Typed text" {
		t.Errorf("text = %q, want the typed text", got)
	}
	if got := inputValue(t, body, "section-title"); got != "   " {
		t.Errorf("title = %q, want the typed title", got)
	}
	if !strings.Contains(body, imageDroppedNote) {
		t.Errorf("refusal does not say the image was dropped:\n%s", body)
	}
	if files := mediaFiles(t, env); len(files) != 0 {
		t.Errorf("media files = %q, want none", files)
	}
	if n := env.revisionCount(t, id); n != 1 {
		t.Errorf("revisions = %d, want 1", n)
	}
	if strings.Contains(getOK(t, env, detail+"/questions/1/section", cookie), imageDroppedNote) {
		t.Errorf("a fresh form says an image was dropped")
	}

	parts := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))
	rec = editorPost(env, parts, "/questions/3/remove", url.Values{"revision": {"1"}}, cookie, csrf)
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Not saved: section 2 would be left without questions; remove the section first")
	if !strings.Contains(rec.Body.String(), `<h1>Edit questions</h1>`) {
		t.Errorf("refusal is not the editor page:\n%s", rec.Body.String())
	}
}

func TestSection_StaleKeepsTypedValues(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))
	id := pathID(t, detail)
	requireRedirect(t, formPost(t, env, detail+"/sections/1", url.Values{"revision": {"1"}, "title": {"Part A2"}}, nil, cookie, csrf), detail+"/edit#s1")

	for _, path := range []string{"/sections/2", "/questions/2/section"} {
		// The typed text starts with an empty line, which the form keeps.
		form := url.Values{"revision": {"1"}, "title": {"Typed title"}, "text": {"\nTyped body"}}
		rec := formPost(t, env, detail+path, form, nil, cookie, csrf)
		requireAlert(t, rec, http.StatusConflict, staleNotice)
		body := rec.Body.String()
		if inputValue(t, body, "section-title") != "Typed title" || shownSectionText(t, body) != "\nTyped body" {
			t.Errorf("stale %s lost the typed values:\n%s", path, body)
		}
		if !strings.Contains(body, `name="revision" value="1"`) {
			t.Errorf("stale %s does not keep revision 1:\n%s", path, body)
		}
	}
	rec := editorPost(env, detail, "/sections/1/remove", url.Values{"revision": {"1"}}, cookie, csrf)
	requireAlert(t, rec, http.StatusConflict, staleNotice)
	if !strings.Contains(rec.Body.String(), `<h1>Edit questions</h1>`) {
		t.Errorf("stale remove is not the editor page:\n%s", rec.Body.String())
	}
	if n := env.revisionCount(t, id); n != 2 {
		t.Errorf("revisions = %d, want 2", n)
	}
}

func TestSection_ImageUpload(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))

	form := url.Values{"revision": {"1"}, "title": {"Part A"}, "text": {"With image"}}
	requireRedirect(t, formPost(t, env, detail+"/sections/1", form, testPNG(t), cookie, csrf), detail+"/edit#s1")

	m := editorImageRe.FindStringSubmatch(editorHead(getOK(t, env, detail+"/edit", cookie), "1"))
	if m == nil {
		t.Fatal("section heading shows no uploaded image")
	}
	if files := mediaFiles(t, env); len(files) != 1 {
		t.Errorf("media files = %q, want one", files)
	}
	if text := shownSectionText(t, getOK(t, env, detail+"/sections/1", cookie)); !strings.HasSuffix(text, "\n![]("+m[2]+")") {
		t.Errorf("section text = %q, want it to end with the image line", text)
	}
}

func TestSection_FrozenAndNoRevision(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	frozen, _ := env.ownedExam(t, env.owner, "Frozen", true)
	requireEditorRefused(t, env, frozen, cookie, csrf, frozenNotice, sectionRequests(examPath(frozen)))
	old := env.examWithoutRevision(t, "EDTSNR", false)
	requireEditorRefused(t, env, old, cookie, csrf, noRevisionNotice, sectionRequests(examPath(old)))
}

func TestSection_RequiresCSRF(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "parts.md", []byte(partsExam))
	id := pathID(t, detail)
	for _, req := range sectionRequests(detail) {
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
	src, err := env.exams.Source(context.Background(), env.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Sections) != 2 || src.Sections[0].Title != "Part A" {
		t.Errorf("sections = %+v, want the imported two", src.Sections)
	}
}
