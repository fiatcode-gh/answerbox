package web

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io"
	"maps"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
	"github.com/fiatcode-gh/answerbox/internal/oidcfake"
)

func TestAdminIndex_RedirectsToExams(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	requireRedirect(t, do(env.h, "GET", "/admin/", cookie), "/admin/exams")

	rec := do(env.h, "GET", "/admin/unknown", cookie)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Page not found") {
		t.Fatalf("unknown admin path with a session = %d, want the 404 page", rec.Code)
	}
}

func TestAdminExams_ListAndEmptyState(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)

	rec := do(env.h, "GET", "/admin/exams", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/exams = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No exams yet. Use New exam to write one, or Import from file to upload one.") {
		t.Errorf("empty state text missing:\n%s", body)
	}
	logout := regexp.MustCompile(`(?s)<form[^>]*action="/admin/logout"[^>]*>.*?</form>`).FindString(body)
	if logout == "" || !strings.Contains(logout, `method="post"`) ||
		!strings.Contains(logout, `name="csrf_token" value="`+csrf+`"`) || !strings.Contains(logout, "Log out") {
		t.Errorf("logout form with csrf_token missing:\n%s", body)
	}
	for _, want := range []string{"Answerbox", `href="/admin/exams"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin header lacks %q", want)
		}
	}

	id, err := env.exams.Import(context.Background(), env.owner, []byte("# Algebra & Co\n\n1. Q one\n- a *\n- b\n\n2. Q two\n- c\n- d *\n"))
	if err != nil {
		t.Fatal(err)
	}
	body = do(env.h, "GET", "/admin/exams", cookie).Body.String()
	if strings.Contains(body, "No exams yet") {
		t.Error("empty state shown with an exam")
	}
	if !strings.Contains(body, `<table id="exams"`) {
		t.Fatalf("table#exams missing:\n%s", body)
	}
	row := regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-exam-id="%d">.*?</tr>`, id)).FindString(body)
	if row == "" {
		t.Fatalf("row for exam %d missing:\n%s", id, body)
	}
	if title := fmt.Sprintf(`<td><a href="/admin/exams/%d">Algebra &amp; Co</a></td>`, id); !strings.Contains(row, title) {
		t.Errorf("row lacks the title link %q:\n%s", title, row)
	}
	cells := regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`).FindAllStringSubmatch(row, -1)
	var got []string
	for _, c := range cells {
		got = append(got, strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(c[1], "")))
	}
	e, err := env.exams.Get(context.Background(), env.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Algebra &amp; Co", e.Code, testAdminEmail, "Closed", "2", "0", "Open Results"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("row cells = %q, want %q", got, want)
	}
	for _, header := range []string{"Title", "Code", "Owner", "Status", "Questions", "Attempts"} {
		if !strings.Contains(body, "<th scope=\"col\">"+header+"</th>") {
			t.Errorf("column %q missing", header)
		}
	}

	if err := env.exams.Open(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}
	if body = do(env.h, "GET", "/admin/exams", cookie).Body.String(); !strings.Contains(body, ">Open</span>") {
		t.Errorf("open exam not shown as Open:\n%s", body)
	}
}

func TestExamList_ListBeforeImport(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)

	body := do(env.h, "GET", "/admin/exams", cookie).Body.String()
	empty := "No exams yet. Use New exam to write one, or Import from file to upload one."
	if e, imp := strings.Index(body, empty), strings.Index(body, `<section id="import"`); e < 0 || imp < 0 || e > imp {
		t.Errorf("empty sentence at %d, import section at %d: want the sentence first:\n%s", e, imp, body)
	}
	if n := len(regexp.MustCompile(`<[a-z0-9]+[^>]*>[^<]*No exams yet\.`).FindAllString(body, -1)); n != 1 || strings.Count(body, "No exams yet.") != 1 {
		t.Errorf("elements holding %q = %d, want 1:\n%s", "No exams yet.", n, body)
	}

	if _, err := env.exams.Import(context.Background(), env.owner, []byte("# One\n\n1. Q\n- a *\n- b\n")); err != nil {
		t.Fatal(err)
	}
	body = do(env.h, "GET", "/admin/exams", cookie).Body.String()
	last := -1
	for _, mark := range []string{`<h1>Exams</h1>`, `id="new-exam"`, `<table id="exams"`, `<section id="import"`, `id="import-form"`, `<details class="format-help">`} {
		at := strings.Index(body, mark)
		if at <= last {
			t.Errorf("%q at %d, want after %d:\n%s", mark, at, last, body)
		}
		last = at
	}
	if !regexp.MustCompile(`<a[^>]*id="import-link"[^>]*href="#import"`).MatchString(body) {
		t.Errorf("import-link to #import missing:\n%s", body)
	}
	if !strings.Contains(body, `<button type="submit" class="secondary">Import exam</button>`) {
		t.Errorf("secondary import button missing:\n%s", body)
	}
}

// postedForm returns the hidden fields of the one form in body whose action
// is action, values unescaped.
func postedForm(t *testing.T, body, action string) url.Values {
	t.Helper()
	forms := regexp.MustCompile(`(?s)<form[^>]*action="`+regexp.QuoteMeta(action)+`"[^>]*>.*?</form>`).FindAllString(body, -1)
	if len(forms) != 1 {
		t.Fatalf("forms with action %q = %d, want 1:\n%s", action, len(forms), body)
	}
	fields := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]*)" value="([^"]*)">`).FindAllStringSubmatch(forms[0], -1) {
		fields.Add(html.UnescapeString(m[1]), html.UnescapeString(m[2]))
	}
	return fields
}

func TestExamList_RowFormsLandOnList(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)
	id := detailID(t, importExam(t, env, cookie, csrf, "a.md", []byte(textExam)))

	for _, step := range []struct {
		action string
		open   bool
	}{{"open", true}, {"close", false}} {
		body := do(env.h, "GET", "/admin/exams", cookie).Body.String()
		if strings.Contains(body, `name="from"`) {
			t.Errorf("list holds a from field:\n%s", body)
		}
		path := fmt.Sprintf("/admin/exams/%d/%s", id, step.action)
		fields := postedForm(t, body, path)
		if len(fields) != 1 || fields.Get("csrf_token") != csrf {
			t.Fatalf("%s form fields = %v, want only csrf_token", step.action, fields)
		}
		requireRedirect(t, postForm(env.h, path, fields, cookie), "/admin/exams")
		e, err := env.exams.Get(context.Background(), env.owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Open != step.open {
			t.Errorf("after %s the exam is open = %v, want %v", step.action, e.Open, step.open)
		}
	}
	if body := do(env.h, "GET", "/admin/exams", cookie).Body.String(); strings.Contains(body, `name="from"`) {
		t.Errorf("list holds a from field:\n%s", body)
	}
}

func TestExamPage_ShowsLength(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	id, err := env.exams.Import(context.Background(), env.owner, []byte("# Timed\nduration: 45\n\n1. Q one\n- a *\n- b\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := do(env.h, "GET", fmt.Sprintf("/admin/exams/%d", id), cookie).Body.String()
	if !regexp.MustCompile(`<dt>Length</dt>\s*<dd>45 minutes</dd>`).MatchString(body) {
		t.Errorf("exam page lacks the length 45 minutes:\n%s", body)
	}
}

func TestExamPage_ShowsCodeAndStudentLink(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	ctx := context.Background()
	id, err := env.exams.Import(ctx, env.owner, []byte("# Geometry Quiz\n\n1. Q one\n- a *\n- b\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := env.exams.Get(ctx, env.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTVWXYZ2-9]{6}$`).MatchString(e.Code) {
		t.Fatalf("exam code %q is not 6 symbols of the alphabet", e.Code)
	}
	page := fmt.Sprintf("/admin/exams/%d", id)
	codeLine := `<p id="exam-code" class="exam-code">` + e.Code + `</p>`
	link := `<a id="student-link" href="http://app.test/?code=` + e.Code + `">http://app.test/?code=` + e.Code + `</a>`
	const note = "Students can start with this code only while the exam is open."

	body := do(env.h, "GET", page, cookie).Body.String()
	for _, want := range []string{codeLine, link, note} {
		if !strings.Contains(body, want) {
			t.Errorf("closed exam page lacks %q:\n%s", want, body)
		}
	}

	if err := env.exams.Open(ctx, env.owner, id); err != nil {
		t.Fatal(err)
	}
	body = do(env.h, "GET", page, cookie).Body.String()
	for _, want := range []string{codeLine, link} {
		if !strings.Contains(body, want) {
			t.Errorf("open exam page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, note) {
		t.Errorf("open exam page still shows the closed note:\n%s", body)
	}

	rec := do(env.h, "GET", "/?code="+e.Code, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Geometry Quiz") {
		t.Errorf("GET /?code=%s = %d, want the exam's title:\n%s", e.Code, rec.Code, rec.Body.String())
	}
}

func TestNewHandler_RequiresPublicURL(t *testing.T) {
	env := newTestEnv(t)
	d := env.deps
	d.PublicURL = ""
	if _, err := NewHandler(d); err == nil || !strings.Contains(err.Error(), "PublicURL") {
		t.Errorf("NewHandler without PublicURL = %v, want an error naming PublicURL", err)
	}
}

func TestNewHandler_RequiresNow(t *testing.T) {
	env := newTestEnv(t)
	d := env.deps
	d.Now = nil
	if _, err := NewHandler(d); err == nil || !strings.Contains(err.Error(), "Now") {
		t.Errorf("NewHandler without Now = %v, want an error naming Now", err)
	}
}

// loginThroughHandler signs the admin in through the provider and the
// handler, and reads the CSRF token from the exams page.
func loginThroughHandler(t *testing.T, env *testEnv) (cookie http.Header, csrf string) {
	t.Helper()
	rec := env.googleSignIn(t, oidcfake.Identity{Email: testAdminEmail, Name: testAdminName}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in callback = %d, want 200:\n%s", rec.Code, rec.Body.String())
	}
	c := adminSetCookie(t, rec)
	if c == nil {
		t.Fatal("sign-in set no cookie")
	}
	cookie = adminCookie(c.Value)
	body := do(env.h, "GET", "/admin/exams", cookie).Body.String()
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("exams page has no csrf_token:\n%s", body)
	}
	return cookie, m[1]
}

// uploadRequest builds a multipart POST with an optional csrf_token field and
// an optional file part named "file".
func uploadRequest(t *testing.T, target, csrf, filename string, data []byte, cookie http.Header) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if csrf != "" {
		if err := mw.WriteField("csrf_token", csrf); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
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

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := range 2 {
		for y := range 2 {
			img.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const textExam = `# Geometry & Co
shuffle: none
show_result: review

1. First **question**
- alpha *
- beta

2. Second question
- gamma
- delta *
- epsilon
`

const zipExam = `# Shapes
shuffle: questions, options

1. Look ![triangle](img/tri.png)
- three *
- four
`

var examIDPath = regexp.MustCompile(`^/admin/exams/([0-9]+)$`)

// importExam uploads a file and returns the new exam's detail path.
func importExam(t *testing.T, env *testEnv, cookie http.Header, csrf, filename string, data []byte) string {
	t.Helper()
	rec := serve(env.h, uploadRequest(t, "/admin/exams", csrf, filename, data, cookie))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !examIDPath.MatchString(loc) {
		t.Fatalf("import %s = %d Location %q, want 303 /admin/exams/{id}:\n%s", filename, rec.Code, loc, rec.Body.String())
	}
	return loc
}

var previewQuestion = regexp.MustCompile(`(?s)<section class="preview-question"[^>]*>(.*?)</section>`)

func TestImport_TextAndZip(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)

	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	rec := do(env.h, "GET", detail, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", detail, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Geometry &amp; Co", "No shuffling", "Score and review", ">Closed</span>"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q:\n%s", want, body)
		}
	}
	sections := previewQuestion.FindAllStringSubmatch(body, -1)
	if len(sections) != 2 {
		t.Fatalf("preview has %d questions, want 2:\n%s", len(sections), body)
	}
	wantOrder := [][]string{
		{"First <strong>question</strong>", "alpha", "beta"},
		{"Second question", "gamma", "delta", "epsilon"},
	}
	wantCorrect := []string{"alpha", "delta"}
	for i, s := range sections {
		pos := 0
		for _, w := range wantOrder[i] {
			j := strings.Index(s[1][pos:], w)
			if j < 0 {
				t.Errorf("question %d: %q missing or out of order:\n%s", i+1, w, s[1])
				break
			}
			pos += j + len(w)
		}
		if n := strings.Count(s[1], "✓ Correct"); n != 1 {
			t.Errorf("question %d: %d \"✓ Correct\" marks, want 1", i+1, n)
		}
		correct := regexp.MustCompile(`(?s)<li[^>]*data-correct="true"[^>]*>(.*?)</li>`).FindStringSubmatch(s[1])
		if correct == nil || !strings.Contains(correct[1], wantCorrect[i]) || !strings.Contains(correct[1], "✓ Correct") {
			t.Errorf("question %d: correct option is not %q marked ✓ Correct:\n%s", i+1, wantCorrect[i], s[1])
		}
	}

	zipDetail := importExam(t, env, cookie, csrf, "shapes.zip", zipOf(t, map[string][]byte{
		"shapes/exam.md":     []byte(zipExam),
		"shapes/img/tri.png": testPNG(t),
	}))
	body = do(env.h, "GET", zipDetail, cookie).Body.String()
	for _, want := range []string{"Shapes", "Questions and options shuffled", "<dd>Score</dd>", "<dt>Questions</dt>", "<dd>1</dd>"} {
		if !strings.Contains(body, want) {
			t.Errorf("zip detail lacks %q:\n%s", want, body)
		}
	}
	if !regexp.MustCompile(`<img src="/media/[0-9a-f]{32}/[0-9a-f]{32}\.png"`).MatchString(body) {
		t.Errorf("zip detail has no <img src=\"/media/…\">:\n%s", body)
	}
}

// cancelOnRead cancels the request's parent context on its first Read, as a
// deadline would if it covered a slow upload.
type cancelOnRead struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c *cancelOnRead) Read(p []byte) (int, error) {
	c.cancel()
	return c.r.Read(p)
}

func TestImport_SlowBodyStillImports(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)

	req := uploadRequest(t, "/admin/exams", csrf, "shapes.zip", zipOf(t, map[string][]byte{
		"shapes/exam.md":     []byte(zipExam),
		"shapes/img/tri.png": testPNG(t),
	}), cookie)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	req.Body = io.NopCloser(&cancelOnRead{r: req.Body, cancel: cancel})

	rec := serve(env.h, req)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !examIDPath.MatchString(loc) {
		t.Fatalf("import with the parent context ended during upload = %d Location %q, want 303 /admin/exams/{id}:\n%s",
			rec.Code, loc, rec.Body.String())
	}
	exams, err := env.exams.List(context.Background(), env.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(exams) != 1 {
		t.Fatalf("stored %d exams, want 1", len(exams))
	}
}

func requireNoExams(t *testing.T, env *testEnv) {
	t.Helper()
	exams, err := env.exams.List(context.Background(), env.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(exams) != 0 {
		t.Fatalf("exams stored: %+v", exams)
	}
}

func requireAlert(t *testing.T, rec *httptest.ResponseRecorder, status int, text string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d:\n%s", rec.Code, status, rec.Body.String())
	}
	alert := regexp.MustCompile(`(?s)<div role="alert"[^>]*>(.*?)</div>`).FindStringSubmatch(rec.Body.String())
	if alert == nil || !strings.Contains(alert[1], text) {
		t.Fatalf("no alert containing %q:\n%s", text, rec.Body.String())
	}
}

func TestImport_InvalidShowsMessageAndStoresNothing(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)

	noCorrect := "# Quiz\n\n1. Q one\n- a\n- b\n"
	rec := serve(env.h, uploadRequest(t, "/admin/exams", csrf, "quiz.md", []byte(noCorrect), cookie))
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Import failed: question 1 (line 3) has no correct option")
	if !strings.Contains(rec.Body.String(), `id="import-form"`) {
		t.Errorf("422 is not the exams page:\n%s", rec.Body.String())
	}
	requireNoExams(t, env)

	missingImage := "# Quiz\n\n1. Look ![x](gone.png)\n- a *\n- b\n"
	rec = serve(env.h, uploadRequest(t, "/admin/exams", csrf, "quiz.md", []byte(missingImage), cookie))
	requireAlert(t, rec, http.StatusUnprocessableEntity, `Import failed: question 1: image &#34;gone.png&#34; not found in upload`)
	requireNoExams(t, env)
}

func TestImport_MissingFile422(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)
	for name, req := range map[string]*http.Request{
		"no file part": uploadRequest(t, "/admin/exams", csrf, "", nil, cookie),
		"empty file":   uploadRequest(t, "/admin/exams", csrf, "empty.md", nil, cookie),
	} {
		t.Run(name, func(t *testing.T) {
			requireAlert(t, serve(env.h, req), http.StatusUnprocessableEntity, "Choose an exam file (.md, .txt or .zip) to upload.")
			requireNoExams(t, env)
		})
	}
}

func TestImport_TooLarge413(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)

	file := append([]byte(textExam), bytes.Repeat([]byte{' '}, 10<<20+1-len(textExam))...)
	rec := serve(env.h, uploadRequest(t, "/admin/exams", csrf, "big.md", file, cookie))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "Upload is larger than 10 MB.") {
		t.Fatalf("10 MiB + 1 file = %d, want 413 with message:\n%s", rec.Code, rec.Body.String())
	}
	requireNoExams(t, env)

	rec = serve(env.h, uploadRequest(t, "/admin/exams", csrf, "huge.md", bytes.Repeat([]byte{' '}, 11<<20+1), cookie))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "Upload is larger than 10 MB.") {
		t.Fatalf("body over 11 MiB = %d, want 413 with message:\n%s", rec.Code, rec.Body.String())
	}
	requireNoExams(t, env)
}

func TestImport_RequiresCSRF(t *testing.T) {
	env := newTestEnv(t)
	cookie, _ := loginThroughHandler(t, env)
	rec := serve(env.h, uploadRequest(t, "/admin/exams", "", "quiz.md", []byte(textExam), cookie))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("import without csrf_token = %d, want 403", rec.Code)
	}
	requireNoExams(t, env)
}

func detailID(t *testing.T, path string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(examIDPath.FindStringSubmatch(path)[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReplace_Flow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	id := detailID(t, detail)
	ctx := context.Background()

	body := do(env.h, "GET", detail, cookie).Body.String()
	if !strings.Contains(body, `id="replace-form"`) || strings.Contains(body, "questions are frozen") {
		t.Fatalf("exam without attempts lacks the replace form:\n%s", body)
	}

	replaced := "# Renamed\n\n1. Brand new question\n- yes *\n- no\n"
	rec := serve(env.h, uploadRequest(t, detail+"/replace", csrf, "renamed.md", []byte(replaced), cookie))
	requireRedirect(t, rec, detail)
	body = do(env.h, "GET", detail, cookie).Body.String()
	for _, want := range []string{"Renamed", "Brand new question"} {
		if !strings.Contains(body, want) {
			t.Errorf("replaced preview lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Second question") {
		t.Errorf("replaced preview still shows old content")
	}

	rec = serve(env.h, uploadRequest(t, detail+"/replace", csrf, "bad.md", []byte("# Bad\n\n1. Q\n- a\n- b\n"), cookie))
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Replace failed: question 1 (line 3) has no correct option")
	if !strings.Contains(rec.Body.String(), "Brand new question") {
		t.Errorf("422 is not the detail page:\n%s", rec.Body.String())
	}
	rec = serve(env.h, uploadRequest(t, detail+"/replace", csrf, "", nil, cookie))
	requireAlert(t, rec, http.StatusUnprocessableEntity, "Choose an exam file (.md, .txt or .zip) to upload.")

	if err := env.exams.Open(ctx, env.owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.NewService(env.db, func() time.Time { return env.clock }, rand.NewChaCha8([32]byte{3})).Start(ctx, env.examCode(t, id), "Ada"); err != nil {
		t.Fatal(err)
	}
	body = do(env.h, "GET", detail, cookie).Body.String()
	if !strings.Contains(body, "This exam has attempts, so its questions are frozen.") || strings.Contains(body, `id="replace-form"`) {
		t.Fatalf("frozen exam: want the notice and no #replace-form:\n%s", body)
	}

	rec = serve(env.h, uploadRequest(t, detail+"/replace", csrf, "geometry.md", []byte(textExam), cookie))
	requireAlert(t, rec, http.StatusConflict, "This exam has attempts, so its questions are frozen.")
	questions, err := env.exams.Questions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 || !strings.Contains(string(questions[0].BodyHTML), "Brand new question") {
		t.Fatalf("frozen exam content changed: %+v", questions)
	}

	for _, target := range []string{"/admin/exams/999/replace", "/admin/exams/abc/replace"} {
		rec = serve(env.h, uploadRequest(t, target, csrf, "x.md", []byte(textExam), cookie))
		if rec.Code != http.StatusNotFound {
			t.Errorf("replace %s = %d, want 404", target, rec.Code)
		}
	}
	for _, target := range []string{"/admin/exams/999", "/admin/exams/abc"} {
		if rec := do(env.h, "GET", target, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

func openRows(t *testing.T, body string) []string {
	t.Helper()
	var open []string
	for _, m := range regexp.MustCompile(`(?s)<tr data-exam-id="([0-9]+)">(.*?)</tr>`).FindAllStringSubmatch(body, -1) {
		if strings.Contains(m[2], ">Open</span>") {
			open = append(open, m[1])
		}
	}
	return open
}

func TestOpenClose(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf := loginThroughHandler(t, env)
	a := detailID(t, importExam(t, env, cookie, csrf, "a.md", []byte(textExam)))
	b := detailID(t, importExam(t, env, cookie, csrf, "b.md", []byte(textExam)))
	form := url.Values{"csrf_token": {csrf}}

	requireRedirect(t, postForm(env.h, fmt.Sprintf("/admin/exams/%d/open", a), form, cookie), "/admin/exams")
	requireRedirect(t, postForm(env.h, fmt.Sprintf("/admin/exams/%d/open", b), form, cookie), "/admin/exams")
	body := do(env.h, "GET", "/admin/exams", cookie).Body.String()
	want := []string{strconv.FormatInt(b, 10), strconv.FormatInt(a, 10)}
	if got := openRows(t, body); !slices.Equal(got, want) {
		t.Fatalf("open rows = %v, want %v:\n%s", got, want, body)
	}
	for _, id := range []int64{a, b} {
		if !strings.Contains(body, fmt.Sprintf(`action="/admin/exams/%d/close"`, id)) ||
			strings.Contains(body, fmt.Sprintf(`action="/admin/exams/%d/open"`, id)) {
			t.Errorf("row %d does not offer close only:\n%s", id, body)
		}
	}

	requireRedirect(t, postForm(env.h, fmt.Sprintf("/admin/exams/%d/close", b), form, cookie), "/admin/exams")
	if got := openRows(t, do(env.h, "GET", "/admin/exams", cookie).Body.String()); !slices.Equal(got, want[1:]) {
		t.Fatalf("open rows after closing b = %v, want %v", got, want[1:])
	}

	for _, target := range []string{"/admin/exams/999/open", "/admin/exams/999/close", "/admin/exams/x/open", "/admin/exams/x/close"} {
		if rec := postForm(env.h, target, form, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, rec.Code)
		}
	}
}

func TestFormatHelp_ExampleImports(t *testing.T) {
	env := newTestEnv(t)
	cookie, _ := loginThroughHandler(t, env)
	body := do(env.h, "GET", "/admin/exams", cookie).Body.String()
	help := regexp.MustCompile(`(?s)<details class="format-help">\s*<summary>Exam file format</summary>(.*?)</details>`).FindStringSubmatch(body)
	if help == nil {
		t.Fatalf("<details> \"Exam file format\" missing:\n%s", body)
	}
	pre := regexp.MustCompile(`(?s)<pre[^>]*>(.*?)</pre>`).FindStringSubmatch(help[1])
	if pre == nil {
		t.Fatalf("format help has no <pre> example:\n%s", help[1])
	}
	text := html.UnescapeString(pre[1])
	img := regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`).FindStringSubmatch(text)
	if img == nil {
		t.Fatalf("example has no image:\n%s", text)
	}
	ex, err := examfile.Parse(examfile.Upload{Text: []byte(text), Dir: ".", Files: map[string][]byte{img[1]: testPNG(t)}}, "/media/k")
	if err != nil {
		t.Fatalf("format example does not parse: %v\n%s", err, text)
	}
	if len(ex.Questions) < 2 || len(ex.Images) != 1 || !strings.Contains(text, "```") {
		t.Errorf("example lacks an image question or a code block question: %+v", ex)
	}
	if ex.Shuffle != examfile.ShuffleQuestionsOptions || ex.ShowResult != examfile.ShowReview {
		t.Errorf("example headers parsed as %q / %q", ex.Shuffle, ex.ShowResult)
	}
	for _, rule := range []string{"<code># Title</code>", "<code>## Title</code>", "shuffle:", "show_result:", "duration:", "trailing <code>*</code>", ".zip", "PNG, JPEG, GIF or WebP", "10 MB"} {
		if !strings.Contains(help[1], rule) {
			t.Errorf("format help lacks %q", rule)
		}
	}
}

// scheduleFixture is a Jakarta app with one closed exam and a signed-in
// admin. testStart is 16:30 on 14 Nov 2026 in Jakarta.
type scheduleFixture struct {
	env    *testEnv
	cookie http.Header
	csrf   string
	id     int64
}

func newScheduleFixture(t *testing.T) *scheduleFixture {
	t.Helper()
	env := newJakartaEnv(t)
	cookie, csrf, _ := env.signIn(t)
	id, err := env.exams.Import(context.Background(), env.owner, []byte(textExam))
	if err != nil {
		t.Fatal(err)
	}
	return &scheduleFixture{env: env, cookie: cookie, csrf: csrf, id: id}
}

func (f *scheduleFixture) schedule(opens, closes string) *httptest.ResponseRecorder {
	return postForm(f.env.h, examPath(f.id)+"/schedule", url.Values{"csrf_token": {f.csrf}, "opens_at": {opens}, "closes_at": {closes}}, f.cookie)
}

func (f *scheduleFixture) action(t *testing.T, name string) {
	t.Helper()
	requireRedirect(t, postForm(f.env.h, examPath(f.id)+"/"+name, url.Values{"csrf_token": {f.csrf}}, f.cookie), "/admin/exams")
}

func (f *scheduleFixture) page(t *testing.T) string {
	t.Helper()
	rec := do(f.env.h, "GET", examPath(f.id), f.cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("exam page = %d", rec.Code)
	}
	return rec.Body.String()
}

// row is the exam's row of the exams list.
func (f *scheduleFixture) row(t *testing.T) string {
	t.Helper()
	body := do(f.env.h, "GET", "/admin/exams", f.cookie).Body.String()
	row := regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-exam-id="%d">.*?</tr>`, f.id)).FindString(body)
	if row == "" {
		t.Fatalf("exams list has no row for exam %d:\n%s", f.id, body)
	}
	return row
}

// at moves the clock to now and signs in again, since a move of days
// outlives the session.
func (f *scheduleFixture) at(t *testing.T, now time.Time) {
	t.Helper()
	f.env.clock = now
	f.cookie, f.csrf, _ = f.env.signIn(t)
}

func (f *scheduleFixture) stored(t *testing.T) exam.Schedule {
	t.Helper()
	e, err := f.env.exams.Get(context.Background(), f.env.owner, f.id)
	if err != nil {
		t.Fatal(err)
	}
	return e.Schedule
}

// scheduleTimes is the text of the schedule line of the exam page.
func scheduleTimes(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<p id="schedule-times">(.*?)</p>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("exam page has no schedule-times:\n%s", body)
	}
	return html.UnescapeString(m[1])
}

// inputValue is the value of the input with id in body.
func inputValue(t *testing.T, body, id string) string {
	t.Helper()
	m := regexp.MustCompile(`<input[^>]*id="` + id + `"[^>]*>`).FindString(body)
	v := regexp.MustCompile(`value="([^"]*)"`).FindStringSubmatch(m)
	if v == nil {
		t.Fatalf("no input %s with a value:\n%s", id, body)
	}
	return html.UnescapeString(v[1])
}

// requireForms checks which of the open and close forms of exam id body has.
func requireForms(t *testing.T, where, body string, id int64, open, close bool) {
	t.Helper()
	for action, want := range map[string]bool{"open": open, "close": close} {
		if got := strings.Contains(body, fmt.Sprintf(`action="/admin/exams/%d/%s"`, id, action)); got != want {
			t.Errorf("%s has the %s form = %v, want %v:\n%s", where, action, got, want, body)
		}
	}
}

// requireBadge checks the Open or Closed badge.
func requireBadge(t *testing.T, where, body string, open bool) {
	t.Helper()
	want, other := ">Closed</span>", ">Open</span>"
	if open {
		want, other = other, want
	}
	if !strings.Contains(body, want) || strings.Contains(body, other) {
		t.Errorf("%s lacks the badge %q:\n%s", where, want, body)
	}
}

const joinNote = "Students can start with this code only while the exam is open."

func TestSchedule_SetShowsOnPages(t *testing.T) {
	f := newScheduleFixture(t)
	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))

	opens, closes := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 17, 2, 30, 0, 0, time.UTC)
	if got := f.stored(t); !got.OpensAt.Equal(opens) || !got.ClosesAt.Equal(closes) {
		t.Fatalf("stored schedule = %v – %v, want %v – %v", got.OpensAt, got.ClosesAt, opens, closes)
	}

	body := f.page(t)
	if got := scheduleTimes(t, body); got != "Opens 2026-11-17 08:00 · Closes 2026-11-17 09:30" {
		t.Errorf("schedule times = %q", got)
	}
	if !strings.Contains(body, "Times are shown in Asia/Jakarta.") {
		t.Errorf("exam page lacks the zone note:\n%s", body)
	}
	if o, c := inputValue(t, body, "schedule-opens"), inputValue(t, body, "schedule-closes"); o != "2026-11-17T08:00" || c != "2026-11-17T09:30" {
		t.Errorf("inputs = %q, %q", o, c)
	}
	requireBadge(t, "exam page", body, false)
	requireForms(t, "exam page", body, f.id, true, true)

	row := f.row(t)
	// Each time is its own line, so a plain teacher's list fits a 1280 px window.
	for _, want := range []string{`<small class="schedule">Opens 2026-11-17 08:00</small>`, `<small class="schedule">Closes 2026-11-17 09:30</small>`} {
		if !strings.Contains(row, want) {
			t.Errorf("list row lacks %q:\n%s", want, row)
		}
	}
	requireBadge(t, "list row", row, false)
	requireForms(t, "list row", row, f.id, true, true)
	if list := do(f.env.h, "GET", "/admin/exams", f.cookie).Body.String(); !strings.Contains(list, `<p class="zone-note">Times are shown in Asia/Jakarta.</p>`) {
		t.Errorf("exams list lacks the zone note:\n%s", list)
	}

	f.at(t, opens.Add(30*time.Minute))
	body = f.page(t)
	requireBadge(t, "exam page inside the window", body, true)
	requireForms(t, "exam page inside the window", body, f.id, true, true)
	if strings.Contains(body, joinNote) {
		t.Errorf("exam page inside the window shows the join note:\n%s", body)
	}
	row = f.row(t)
	requireBadge(t, "list row inside the window", row, true)
	requireForms(t, "list row inside the window", row, f.id, true, true)

	f.at(t, closes)
	requireBadge(t, "exam page after the window", f.page(t), false)
	requireBadge(t, "list row after the window", f.row(t), false)
}

func TestSchedule_ListZoneNoteOnlyWithASchedule(t *testing.T) {
	f := newScheduleFixture(t)
	if list := do(f.env.h, "GET", "/admin/exams", f.cookie).Body.String(); strings.Contains(list, "Times are shown in") {
		t.Errorf("exams list without a schedule shows the zone note:\n%s", list)
	}
}

func TestSchedule_OneTimeOnly(t *testing.T) {
	t.Run("close time only", func(t *testing.T) {
		f := newScheduleFixture(t)
		requireRedirect(t, f.schedule("", "2026-11-17T09:30"), examPath(f.id))
		body := f.page(t)
		requireBadge(t, "exam page", body, true)
		if got := scheduleTimes(t, body); got != "Closes 2026-11-17 09:30" {
			t.Errorf("schedule times = %q", got)
		}
		if strings.Contains(f.row(t), "Opens") {
			t.Errorf("list row shows an open time:\n%s", f.row(t))
		}
	})
	t.Run("open time only", func(t *testing.T) {
		f := newScheduleFixture(t)
		requireRedirect(t, f.schedule("2026-11-17T08:00", ""), examPath(f.id))
		body := f.page(t)
		requireBadge(t, "exam page", body, false)
		if got := scheduleTimes(t, body); got != "Opens 2026-11-17 08:00" {
			t.Errorf("schedule times = %q", got)
		}
		if strings.Contains(f.row(t), "Closes") {
			t.Errorf("list row shows a close time:\n%s", f.row(t))
		}
		f.at(t, time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC))
		requireBadge(t, "exam page at the open time", f.page(t), true)
	})
}

func TestSchedule_Validation422(t *testing.T) {
	for _, c := range []struct {
		name, opens, closes, alert string
	}{
		{"both empty", "", "", "Enter an open time, a close time or both. To remove the schedule, press Open or Close."},
		{"open unparsable", "tomorrow", "", "Enter a valid date and time."},
		{"close unparsable", "", "2026-11-17T09:30:00x", "Enter a valid date and time."},
		{"close before open", "2026-11-17T09:30", "2026-11-17T08:00", "The close time must be after the open time."},
		{"close equal to open", "2026-11-17T09:30", "2026-11-17T09:30", "The close time must be after the open time."},
		{"close in the past", "", "2026-11-14T16:00", "The close time must be in the future."},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newScheduleFixture(t)
			rec := f.schedule(c.opens, c.closes)
			body := rec.Body.String()
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422:\n%s", rec.Code, body)
			}
			if !strings.Contains(body, `<div role="alert" class="alert">`+c.alert+`</div>`) {
				t.Errorf("alert %q missing:\n%s", c.alert, body)
			}
			if o, cl := inputValue(t, body, "schedule-opens"), inputValue(t, body, "schedule-closes"); o != c.opens || cl != c.closes {
				t.Errorf("inputs = %q, %q, want the typed %q, %q", o, cl, c.opens, c.closes)
			}
			if f.stored(t).Set() {
				t.Errorf("schedule stored: %+v", f.stored(t))
			}
		})
	}
}

func TestSchedule_ButtonsClear(t *testing.T) {
	f := newScheduleFixture(t)
	requireCleared := func(open bool) {
		t.Helper()
		body := f.page(t)
		if got := scheduleTimes(t, body); got != "No schedule. The exam follows the Open and Close buttons." {
			t.Errorf("schedule times = %q", got)
		}
		if o, c := inputValue(t, body, "schedule-opens"), inputValue(t, body, "schedule-closes"); o != "" || c != "" {
			t.Errorf("inputs = %q, %q, want empty", o, c)
		}
		requireBadge(t, "exam page", body, open)
		requireForms(t, "exam page", body, f.id, !open, open)
	}

	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))
	f.action(t, "open")
	requireCleared(true)

	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))
	f.action(t, "close")
	requireCleared(false)
}

func TestSchedule_UnknownExam404(t *testing.T) {
	f := newScheduleFixture(t)
	for _, target := range []string{"/admin/exams/999/schedule", "/admin/exams/x/schedule"} {
		for _, opens := range []string{"2026-11-17T08:00", "tomorrow"} {
			form := url.Values{"csrf_token": {f.csrf}, "opens_at": {opens}}
			if rec := postForm(f.env.h, target, form, f.cookie); rec.Code != http.StatusNotFound {
				t.Errorf("POST %s opens_at=%s = %d, want 404", target, opens, rec.Code)
			}
		}
	}
}

// pathID returns the exam id of an exam detail path.
func pathID(t *testing.T, detail string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(strings.TrimPrefix(detail, "/admin/exams/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDownload_Route(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	ctx := context.Background()
	text := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	zipped := importExam(t, env, cookie, csrf, "shapes.zip", zipOf(t, map[string][]byte{
		"shapes/exam.md":     []byte(zipExam),
		"shapes/img/tri.png": testPNG(t),
	}))

	for _, c := range []struct{ detail, ext, contentType string }{
		{text, ".md", "text/markdown; charset=utf-8"},
		{zipped, ".zip", "application/zip"},
	} {
		id := pathID(t, c.detail)
		e, err := env.exams.Get(ctx, env.owner, id)
		if err != nil {
			t.Fatal(err)
		}
		want, err := env.exams.Download(ctx, env.owner, id)
		if err != nil {
			t.Fatal(err)
		}
		rec := do(env.h, "GET", c.detail+"/download", cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s/download = %d:\n%s", c.detail, rec.Code, rec.Body.String())
		}
		h := rec.Header()
		for name, value := range map[string]string{
			"Content-Type":           c.contentType,
			"Content-Disposition":    `attachment; filename="exam-` + e.Code + c.ext + `"`,
			"Cache-Control":          "no-store",
			"X-Content-Type-Options": "nosniff",
		} {
			if got := h.Get(name); got != value {
				t.Errorf("%s/download %s = %q, want %q", c.detail, name, got, value)
			}
		}
		if !bytes.Equal(rec.Body.Bytes(), want.Data) {
			t.Errorf("%s/download body differs from Exams.Download", c.detail)
		}
		if c.ext == ".zip" && !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
			t.Errorf("zip download does not start with PK")
		}
		link := `<a id="download-link" class="quiet" href="` + c.detail + `/download">Download</a>`
		if body := getOK(t, env, c.detail, cookie); !strings.Contains(body, link) {
			t.Errorf("exam page %s lacks %s", c.detail, link)
		}
	}

	old := examPath(env.examWithoutRevision(t, "DWNKDZ", false))
	if body := getOK(t, env, old, cookie); strings.Contains(body, `download-link`) {
		t.Errorf("exam without a revision shows the download link")
	}
	rec := do(env.h, "GET", old+"/download", cookie)
	alert := `<div role="alert" class="alert">` + html.EscapeString(noRevisionNotice) + `</div>`
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), alert) {
		t.Errorf("GET %s/download = %d, want 409 with %s:\n%s", old, rec.Code, alert, rec.Body.String())
	}

	requireNotFound(t, env, "GET", "/admin/exams/999/download", cookie)
	requireNotFound(t, env, "GET", "/admin/exams/x/download", cookie)
}

func (env *testEnv) examCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM exam`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCopy_Flow(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	ctx := context.Background()
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	src, err := env.exams.Get(ctx, env.owner, pathID(t, detail))
	if err != nil {
		t.Fatal(err)
	}

	form := regexp.MustCompile(`(?s)<form id="copy-form" method="post" action="` + detail + `/copy">.*?</form>`).
		FindString(getOK(t, env, detail, cookie))
	if !strings.Contains(form, `name="csrf_token" value="`+csrf+`"`) || !strings.Contains(form, "Copy exam") {
		t.Errorf("exam page copy form = %q, want one with the CSRF field", form)
	}

	n := env.examCount(t)
	if rec := postForm(env.h, detail+"/copy", url.Values{}, cookie); rec.Code != http.StatusForbidden {
		t.Errorf("POST %s/copy without token = %d, want 403", detail, rec.Code)
	}
	if got := env.examCount(t); got != n {
		t.Errorf("exams after a refused copy = %d, want %d", got, n)
	}

	rec := postForm(env.h, detail+"/copy", url.Values{"csrf_token": {csrf}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST %s/copy = %d:\n%s", detail, rec.Code, rec.Body.String())
	}
	copyPath := rec.Header().Get("Location")
	copyID := pathID(t, copyPath)
	if copyID == src.ID {
		t.Fatalf("copy redirects to the source %s", copyPath)
	}
	c, err := env.exams.Get(ctx, env.owner, copyID)
	if err != nil {
		t.Fatal(err)
	}
	body := getOK(t, env, copyPath, cookie)
	for _, want := range []string{"<h1>" + html.EscapeString(src.Title) + "</h1>", ">Closed</span>", `<p id="exam-code" class="exam-code">` + c.Code + `</p>`} {
		if !strings.Contains(body, want) {
			t.Errorf("copy page lacks %s", want)
		}
	}
	if c.Code == src.Code {
		t.Errorf("copy has the source's code %q", c.Code)
	}
	rows := revisionRows(getOK(t, env, copyPath+"/revisions", cookie))
	want := fmt.Sprintf("Copied from %s (%s), revision 1", html.EscapeString(src.Title), src.Code)
	if len(rows) != 1 || rows[0][0] != "1" || rows[0][4] != want {
		t.Errorf("copy history = %q, want one row with %q", rows, want)
	}

	old := examPath(env.examWithoutRevision(t, "CPYKDZ", false))
	if body := getOK(t, env, old, cookie); strings.Contains(body, `copy-form`) {
		t.Errorf("exam without a revision shows the copy form")
	}
	rec = postForm(env.h, old+"/copy", url.Values{"csrf_token": {csrf}}, cookie)
	alert := `<div role="alert" class="alert">` + html.EscapeString(noRevisionNotice) + `</div>`
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), alert) {
		t.Errorf("POST %s/copy = %d, want 409 with %s:\n%s", old, rec.Code, alert, rec.Body.String())
	}

	for _, path := range []string{"/admin/exams/999/copy", "/admin/exams/x/copy"} {
		if rec := postForm(env.h, path, url.Values{"csrf_token": {csrf}}, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestCopy_AdminOwnsCopyOfTeachersExam(t *testing.T) {
	env := newTestEnv(t)
	b := env.addTeacher(t, "b@example.test")
	admin := env.adminTeacher(t)
	id, _ := env.ownedExam(t, b.actor, "Beta", false)

	rec := postForm(env.h, examPath(id)+"/copy", url.Values{"csrf_token": {admin.csrf}}, admin.cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin copy = %d:\n%s", rec.Code, rec.Body.String())
	}
	copyID := pathID(t, rec.Header().Get("Location"))
	rowOf := func(body string, id int64) string {
		return regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-exam-id="%d">.*?</tr>`, id)).FindString(body)
	}
	if row := rowOf(getOK(t, env, "/admin/exams", admin.cookie), copyID); !strings.Contains(row, `<td class="owner">`+testAdminEmail+`</td>`) {
		t.Errorf("admin list row of the copy lacks owner %s:\n%s", testAdminEmail, row)
	}
	list := getOK(t, env, "/admin/exams", b.cookie)
	if rowOf(list, copyID) != "" {
		t.Errorf("teacher B's list shows the admin's copy %d", copyID)
	}
	if rowOf(list, id) == "" {
		t.Errorf("teacher B's list lacks B's own exam %d", id)
	}
}

// stateForms are the open and close forms of exam id in body.
func stateForms(body string, id int64) []string {
	return regexp.MustCompile(fmt.Sprintf(`(?s)<form[^>]*action="/admin/exams/%d/(?:open|close)"[^>]*>.*?</form>`, id)).FindAllString(body, -1)
}

func TestExamPage_Order(t *testing.T) {
	f := newScheduleFixture(t)
	body := f.page(t)
	last := -1
	for _, mark := range []string{`class="exam-head"`, `class="exam-state"`, `class="join"`, `class="exam-links"`, `class="schedule"`, `<dl class="settings">`, `class="more"`, `class="replace"`, `<h2>Preview</h2>`} {
		at := strings.Index(body, mark)
		if at <= last {
			t.Errorf("%q at %d, want after %d:\n%s", mark, at, last, body)
		}
		last = at
	}
}

func TestExamPage_StateActions(t *testing.T) {
	f := newScheduleFixture(t)
	openButton := `<button type="submit">Open exam</button>`
	closeButton := `<button type="submit">Close exam</button>`
	closeSecondary := `<button type="submit" class="secondary">Close exam</button>`
	check := func(state string, want []string, open, close bool) {
		t.Helper()
		body := f.page(t)
		requireForms(t, state, body, f.id, open, close)
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: page lacks %q:\n%s", state, w, body)
			}
		}
		if n := strings.Count(body, `<button type="submit">`); n != 1 {
			t.Errorf("%s: primary buttons = %d, want 1:\n%s", state, n, body)
		}
		forms := stateForms(body, f.id)
		for _, form := range forms {
			if !strings.Contains(form, `<input type="hidden" name="from" value="exam">`) {
				t.Errorf("%s: form lacks the from field:\n%s", state, form)
			}
		}
		if n := strings.Count(body, `name="from"`); n != len(forms) {
			t.Errorf("%s: from fields = %d, want %d", state, n, len(forms))
		}
	}

	check("closed", []string{openButton}, true, false)
	f.action(t, "open")
	check("opened by the button", []string{closeButton}, false, true)
	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))
	check("scheduled", []string{openButton, closeSecondary}, true, true)

	if err := f.env.exams.Archive(context.Background(), f.env.owner, f.id); err != nil {
		t.Fatal(err)
	}
	check("archived", nil, false, false)
	body := f.page(t)
	state := regexp.MustCompile(`(?s)<section class="exam-state">.*?</section>`).FindString(body)
	if !regexp.MustCompile(`(?s)<form id="unarchive-form"[^>]*>.*?<button type="submit">Unarchive</button>.*?</form>`).MatchString(state) {
		t.Errorf("archived: section.exam-state lacks the unarchive form:\n%s", body)
	}
	for _, absent := range []string{`id="schedule-form"`, `id="archive-form"`} {
		if strings.Contains(body, absent) {
			t.Errorf("archived page has %s", absent)
		}
	}
}

func TestExamPage_ScheduleOnDemand(t *testing.T) {
	check := func(name, body, details, summary string) {
		t.Helper()
		for _, want := range []string{details, summary} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %q:\n%s", name, want, body)
			}
		}
		if n := strings.Count(body, `class="zone-note"`); n != 1 {
			t.Errorf("%s: zone notes = %d, want 1", name, n)
		}
		start := strings.Index(body, `<details id="schedule-details"`)
		end := strings.Index(body[max(start, 0):], `</details>`)
		if start < 0 || end < 0 || strings.Index(body, `class="zone-note"`) < start+end {
			t.Errorf("%s: zone note not after the schedule details:\n%s", name, body)
		}
	}

	f := newScheduleFixture(t)
	check("no schedule", f.page(t), `<details id="schedule-details" class="disclosure">`, `<summary>Set a schedule</summary>`)

	rec := f.schedule("tomorrow", "2026-11-17T09:30")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused save = %d, want 422", rec.Code)
	}
	refused := rec.Body.String()
	check("refused save", refused, `<details id="schedule-details" class="disclosure" open>`, `<summary>Set a schedule</summary>`)
	if o, c := inputValue(t, refused, "schedule-opens"), inputValue(t, refused, "schedule-closes"); o != "tomorrow" || c != "2026-11-17T09:30" {
		t.Errorf("refused save inputs = %q, %q, want the typed values", o, c)
	}

	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))
	check("schedule set", f.page(t), `<details id="schedule-details" class="disclosure" open>`, `<summary>Change the schedule</summary>`)
}

func TestExamPage_MoreActions(t *testing.T) {
	f := newScheduleFixture(t)
	body := f.page(t)
	start, end := strings.Index(body, `class="more"`), strings.Index(body, `<h2>Preview</h2>`)
	if start < 0 || end < start {
		t.Fatalf("more actions section missing:\n%s", body)
	}
	more := body[start:end]
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`id="download-link" class="quiet"`),
		regexp.MustCompile(`(?s)<form id="copy-form"[^>]*>.*?<button type="submit" class="quiet">Copy exam</button>.*?</form>`),
		regexp.MustCompile(`(?s)<form id="archive-form"[^>]*>.*?<button type="submit" class="quiet">Archive</button>.*?</form>`),
		regexp.MustCompile(`(?s)<form id="replace-form"[^>]*>.*?<button type="submit" class="secondary">Replace exam</button>.*?</form>`),
	} {
		if !want.MatchString(more) {
			t.Errorf("more actions lack %s:\n%s", want, more)
		}
	}
}

func TestExamPage_CopyLink(t *testing.T) {
	f := newScheduleFixture(t)
	body := f.page(t)
	link := "http://app.test/?code=" + f.env.examCode(t, f.id)
	button := `<p class="join-actions"><button type="button" id="copy-link" class="secondary" data-copy="` + link +
		`" data-done="Copied" data-failed="Could not copy. Select the link and copy it." hidden>Copy link</button> <span id="copy-status" class="copy-status" role="status"></span></p>`
	if !strings.Contains(body, button) {
		t.Errorf("page lacks %s:\n%s", button, body)
	}
	script := regexp.MustCompile(`<script src="(/static/copy\.js\?v=[0-9a-f]{8})" defer></script>`).FindStringSubmatch(body)
	if script == nil {
		t.Fatalf("page does not load copy.js:\n%s", body)
	}
	rec := do(f.env.h, "GET", html.UnescapeString(script[1]), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Errorf("GET %s = %d %q, want 200 JavaScript", script[1], rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, path := range []string{"/admin/exams", examPath(f.id) + "/revisions"} {
		if body := do(f.env.h, "GET", path, f.cookie).Body.String(); strings.Contains(body, "<script") {
			t.Errorf("%s loads a script", path)
		}
	}
}

func TestNoRevision_SentenceOnce(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	old := examPath(env.examWithoutRevision(t, "NRVKDZ", false))
	notice := html.EscapeString(noRevisionNotice)

	page := getOK(t, env, old, cookie)
	if n := strings.Count(page, notice); n != 1 || !strings.Contains(page, `<p id="no-revision" class="frozen-notice">`+notice+`</p>`) {
		t.Errorf("exam page holds the notice %d times, want once in #no-revision:\n%s", n, page)
	}
	alert := `<div role="alert" class="alert">` + notice + `</div>`
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"GET download": do(env.h, "GET", old+"/download", cookie),
		"POST copy":    postForm(env.h, old+"/copy", url.Values{"csrf_token": {csrf}}, cookie),
		"GET edit":     do(env.h, "GET", old+"/edit", cookie),
	} {
		body := rec.Body.String()
		if rec.Code != http.StatusConflict || strings.Count(body, notice) != 1 || !strings.Contains(body, alert) || strings.Contains(body, `id="no-revision"`) {
			t.Errorf("%s = %d, want 409 with the notice once, in the alert:\n%s", name, rec.Code, body)
		}
	}
}

func TestOpenClose_FromExamPageLandsOnExamPage(t *testing.T) {
	f := newScheduleFixture(t)
	press := func(action string, open bool) {
		t.Helper()
		path := examPath(f.id) + "/" + action
		fields := postedForm(t, f.page(t), path)
		if len(fields) != 2 || fields.Get("csrf_token") != f.csrf || fields.Get("from") != "exam" || len(fields["from"]) != 1 {
			t.Fatalf("%s form fields = %v, want csrf_token and from=exam", action, fields)
		}
		requireRedirect(t, postForm(f.env.h, path, fields, f.cookie), examPath(f.id))
		if e := f.env.getExam(t, f.id); e.Open != open || e.Schedule.Set() {
			t.Errorf("after %s: open = %v schedule = %+v, want open %v and no schedule", action, e.Open, e.Schedule, open)
		}
	}

	press("open", true)
	press("close", false)

	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))
	requireForms(t, "scheduled page", f.page(t), f.id, true, true)
	press("open", true)
	requireRedirect(t, f.schedule("2026-11-17T08:00", "2026-11-17T09:30"), examPath(f.id))
	press("close", false)
}

func TestOpenClose_UnknownFromLandsOnList(t *testing.T) {
	f := newScheduleFixture(t)
	other, err := f.env.exams.Import(context.Background(), f.env.owner, []byte(textExam))
	if err != nil {
		t.Fatal(err)
	}
	froms := [][]string{nil, {""}, {"list"}, {"EXAM"}, {"exam "}, {"exams"}, {examPath(other)}, {"https://evil.example/"}, {"//evil.example"}, {`\\evil.example`}, {"list", "exam"}}
	seen := map[string]bool{}
	post := func(action string, from []string) *httptest.ResponseRecorder {
		form := url.Values{"csrf_token": {f.csrf}}
		if from != nil {
			form["from"] = from
		}
		rec := postForm(f.env.h, examPath(f.id)+"/"+action, form, f.cookie)
		seen[rec.Header().Get("Location")] = true
		return rec
	}
	for _, c := range []struct {
		action string
		open   bool
	}{{"open", true}, {"close", false}} {
		for _, from := range froms {
			setOpen := f.env.exams.Close
			if !c.open {
				setOpen = f.env.exams.Open
			}
			if err := setOpen(context.Background(), f.env.owner, f.id); err != nil {
				t.Fatal(err)
			}
			rec := post(c.action, from)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/exams" {
				t.Errorf("%s with from %q = %d Location %q, want 303 /admin/exams", c.action, from, rec.Code, rec.Header().Get("Location"))
			}
			if got := f.env.getExam(t, f.id).Open; got != c.open {
				t.Errorf("%s with from %q: open = %v, want %v", c.action, from, got, c.open)
			}
		}
		post(c.action, []string{"exam"})
	}
	want := map[string]bool{"/admin/exams": true, examPath(f.id): true}
	if !maps.Equal(seen, want) {
		t.Errorf("Location values = %v, want %v", seen, want)
	}
}

func TestOpenClose_RefusalsIgnoreFrom(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	stranger := env.addTeacher(t, "b@example.test")
	id, _ := env.ownedExam(t, env.owner, "Algebra", false)
	press := func(id, action string, who teacher, header http.Header) *httptest.ResponseRecorder {
		h := who.cookie.Clone()
		for k, v := range header {
			h[k] = v
		}
		return postForm(env.h, "/admin/exams/"+id+"/"+action, url.Values{"csrf_token": {who.csrf}, "from": {"exam"}}, h)
	}
	sid := strconv.FormatInt(id, 10)

	for _, action := range []string{"open", "close"} {
		for _, target := range []string{"999", "x"} {
			if rec := press(target, action, who, nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", action, target, rec.Code)
			}
		}
	}
	if rec := press(sid, "open", stranger, nil); rec.Code != http.StatusNotFound {
		t.Errorf("open of another teacher's exam = %d, want 404", rec.Code)
	}
	if rec := postForm(env.h, examPath(id)+"/open", url.Values{"from": {"exam"}}, who.cookie); rec.Code != http.StatusForbidden {
		t.Errorf("open without csrf_token = %d, want 403", rec.Code)
	}
	if rec := press(sid, "open", who, http.Header{"Sec-Fetch-Site": {"cross-site"}}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site open = %d, want 403", rec.Code)
	}
	if env.getExam(t, id).Open {
		t.Fatal("a refused open opened the exam")
	}
	if err := env.exams.Open(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}
	if rec := press(sid, "close", stranger, nil); rec.Code != http.StatusNotFound {
		t.Errorf("close of another teacher's exam = %d, want 404", rec.Code)
	}
	if rec := postForm(env.h, examPath(id)+"/close", url.Values{"from": {"exam"}}, who.cookie); rec.Code != http.StatusForbidden {
		t.Errorf("close without csrf_token = %d, want 403", rec.Code)
	}
	if rec := press(sid, "close", who, http.Header{"Sec-Fetch-Site": {"cross-site"}}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site close = %d, want 403", rec.Code)
	}
	if !env.getExam(t, id).Open {
		t.Fatal("a refused close closed the exam")
	}

	if err := env.exams.Archive(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}
	requireAlert(t, press(sid, "open", who, nil), http.StatusConflict, archivedNotice)
	env.requireArchivedClosed(t, "open with from=exam", id)
	requireRedirect(t, press(sid, "close", who, nil), examPath(id))
	env.requireArchivedClosed(t, "close with from=exam", id)
}
