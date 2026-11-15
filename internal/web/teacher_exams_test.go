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
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
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
	if !strings.Contains(body, "No exams yet. Import one above.") {
		t.Errorf("empty state text missing:\n%s", body)
	}
	logout := regexp.MustCompile(`(?s)<form[^>]*action="/admin/logout"[^>]*>.*?</form>`).FindString(body)
	if logout == "" || !strings.Contains(logout, `method="post"`) ||
		!strings.Contains(logout, `name="csrf_token" value="`+csrf+`"`) || !strings.Contains(logout, "Log out") {
		t.Errorf("logout form with csrf_token missing:\n%s", body)
	}
	for _, want := range []string{"go-exam", `href="/admin/exams"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin header lacks %q", want)
		}
	}

	id, err := env.exams.Import(context.Background(), []byte("# Algebra & Co\n\n1. Q one\n- a *\n- b\n\n2. Q two\n- c\n- d *\n"))
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
	cells := regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`).FindAllStringSubmatch(row, -1)
	var got []string
	for _, c := range cells {
		got = append(got, strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(c[1], "")))
	}
	want := []string{"Algebra &amp; Co", "Closed", "2", "0", "Open View Results"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("row cells = %q, want %q", got, want)
	}
	for _, header := range []string{"Title", "Status", "Questions", "Attempts"} {
		if !strings.Contains(body, "<th scope=\"col\">"+header+"</th>") {
			t.Errorf("column %q missing", header)
		}
	}

	if err := env.exams.Open(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if body = do(env.h, "GET", "/admin/exams", cookie).Body.String(); !strings.Contains(body, ">Open</span>") {
		t.Errorf("open exam not shown as Open:\n%s", body)
	}
}

// loginThroughHandler signs in with the login form and reads the CSRF token
// from the exams page, as a browser would.
func loginThroughHandler(t *testing.T, env *testEnv) (cookie http.Header, csrf string) {
	t.Helper()
	rec := postForm(env.h, "/admin/login", url.Values{"password": {testPassword}}, nil)
	requireRedirect(t, rec, "/admin/exams")
	c := adminSetCookie(t, rec)
	if c == nil {
		t.Fatal("login set no cookie")
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
	exams, err := env.exams.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(exams) != 1 {
		t.Fatalf("stored %d exams, want 1", len(exams))
	}
}

func requireNoExams(t *testing.T, env *testEnv) {
	t.Helper()
	exams, err := env.exams.List(context.Background())
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

	if err := env.exams.Open(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.NewService(env.db, func() time.Time { return env.clock }, rand.NewChaCha8([32]byte{3})).Start(ctx, "Ada"); err != nil {
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
	if got := openRows(t, body); len(got) != 1 || got[0] != strconv.FormatInt(b, 10) {
		t.Fatalf("open rows = %v, want only %d:\n%s", got, b, body)
	}
	if !strings.Contains(body, fmt.Sprintf(`action="/admin/exams/%d/close"`, b)) ||
		!strings.Contains(body, fmt.Sprintf(`action="/admin/exams/%d/open"`, a)) {
		t.Errorf("row actions do not match the state:\n%s", body)
	}

	requireRedirect(t, postForm(env.h, fmt.Sprintf("/admin/exams/%d/close", b), form, cookie), "/admin/exams")
	if got := openRows(t, do(env.h, "GET", "/admin/exams", cookie).Body.String()); len(got) != 0 {
		t.Fatalf("open rows after close = %v, want none", got)
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
	for _, rule := range []string{"<code># Title</code>", "shuffle:", "show_result:", "trailing <code>*</code>", ".zip", "PNG, JPEG, GIF or WebP", "10 MB"} {
		if !strings.Contains(help[1], rule) {
			t.Errorf("format help lacks %q", rule)
		}
	}
}
