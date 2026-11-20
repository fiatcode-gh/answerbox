package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

var revisionRowRe = regexp.MustCompile(`(?s)<tr data-revision="(\d+)">(.*?)</tr>`)

// revisionRows returns the revision number and the text of every cell of
// each history-table row, in page order, with the row's markup last.
func revisionRows(body string) [][]string {
	var out [][]string
	for _, m := range revisionRowRe.FindAllStringSubmatch(body, -1) {
		row := []string{m[1]}
		for _, c := range cellRe.FindAllStringSubmatch(m[2], -1) {
			row = append(row, strings.Join(strings.Fields(tagRe.ReplaceAllString(c[1], "")), " "))
		}
		out = append(out, append(row, m[2]))
	}
	return out
}

// examWithoutRevision inserts an exam of one question by hand, as one stored
// before revisions existed, owned by the first admin, and returns its ID.
func (env *testEnv) examWithoutRevision(t *testing.T, code string, open bool) int64 {
	t.Helper()
	res, err := env.db.Exec(`INSERT INTO exam (title, shuffle, show_result, is_open, media_key, created_at, owner_id, code)
		VALUES ('Old exam', 'none', 'score', ?, ?, 0, ?, ?)`, open, "old-"+code, env.owner.TeacherID, code)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	res, err = env.db.Exec(`INSERT INTO question (exam_id, position, body_html) VALUES (?, 1, '<p>Old question</p>')`, id)
	if err != nil {
		t.Fatal(err)
	}
	qid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO question_option (question_id, position, body_html, is_correct)
		VALUES (?, 1, '<p>yes</p>', 1), (?, 2, '<p>no</p>', 0)`, qid, qid); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRevisions_HistoryNewestFirst(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	env.clock = testStart.Add(time.Hour)
	requireRedirect(t, serve(env.h, uploadRequest(t, detail+"/replace", csrf, "b.md", []byte("# B\n\n1. Q\n- a *\n- b\n"), cookie)), detail)
	env.clock = testStart.Add(2*time.Hour + 5*time.Minute)
	requireRedirect(t, serve(env.h, uploadRequest(t, detail+"/replace", csrf, "c.md", []byte("# C\n\n1. Q\n- a *\n- b\n"), cookie)), detail)

	body := getOK(t, env, detail+"/revisions", cookie)
	rows := revisionRows(body)
	want := [][]string{
		{"3", "3", "2026-11-14 18:35", testAdminEmail, "Replaced from file"},
		{"2", "2", "2026-11-14 17:30", testAdminEmail, "Replaced from file"},
		{"1", "1", "2026-11-14 16:30", testAdminEmail, "Imported"},
	}
	if len(rows) != len(want) {
		t.Fatalf("history has %d rows, want %d:\n%s", len(rows), len(want), body)
	}
	for i, w := range want {
		if got := rows[i][:len(w)]; strings.Join(got, "|") != strings.Join(w, "|") {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
		if link := fmt.Sprintf(`href="%s/revisions/%s"`, detail, w[0]); !strings.Contains(rows[i][len(rows[i])-1], link) {
			t.Errorf("row %d has no %s:\n%s", i, link, rows[i][len(rows[i])-1])
		}
	}
	if !strings.Contains(body, `<table id="revisions">`) || !strings.Contains(body, "Times are shown in Asia/Jakarta.") {
		t.Errorf("history lacks the table or the zone line:\n%s", body)
	}
	if m := regexp.MustCompile(`\d\d:\d\d:\d\d`).FindString(body); m != "" {
		t.Errorf("history shows seconds %q", m)
	}
}

var revisionImgRe = regexp.MustCompile(`<img src="(/media/([0-9a-f]{32})/[0-9a-f]{32}\.png)"`)

func TestRevision_ShowsItsOwnContent(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "shapes.zip", zipOf(t, map[string][]byte{
		"shapes/exam.md":     []byte(zipExam),
		"shapes/img/tri.png": testPNG(t),
	}))
	requireRedirect(t, serve(env.h, uploadRequest(t, detail+"/replace", csrf, "geometry.md", []byte(textExam), cookie)), detail)

	first := getOK(t, env, detail+"/revisions/1", cookie)
	for _, want := range []string{"<h1>Revision 1</h1>", "<dd>Shapes</dd>", "Questions and options shuffled", "<dd>Score</dd>", "<dd>60 minutes</dd>", "Look", "three", "four", "Imported"} {
		if !strings.Contains(first, want) {
			t.Errorf("revision 1 lacks %q:\n%s", want, first)
		}
	}
	sections := previewQuestion.FindAllStringSubmatch(first, -1)
	if len(sections) != 1 || strings.Count(sections[0][1], "✓ Correct") != 1 {
		t.Errorf("revision 1: want one question with one ✓ Correct:\n%s", first)
	}
	img := revisionImgRe.FindStringSubmatch(first)
	if img == nil {
		t.Fatalf("revision 1 has no <img src=\"/media/…\">:\n%s", first)
	}
	if rec := do(env.h, "GET", img[1], nil); rec.Code != http.StatusOK {
		t.Errorf("GET %s after Replace = %d, want 200", img[1], rec.Code)
	}

	second := getOK(t, env, detail+"/revisions/2", cookie)
	for _, want := range []string{"<h1>Revision 2</h1>", "<dd>Geometry &amp; Co</dd>", "No shuffling", "Score and review", "First <strong>question</strong>", "Second question", "Replaced from file"} {
		if !strings.Contains(second, want) {
			t.Errorf("revision 2 lacks %q:\n%s", want, second)
		}
	}
	sections = previewQuestion.FindAllStringSubmatch(second, -1)
	if len(sections) != 2 {
		t.Fatalf("revision 2 has %d questions, want 2:\n%s", len(sections), second)
	}
	for i, s := range sections {
		if n := strings.Count(s[1], "✓ Correct"); n != 1 {
			t.Errorf("revision 2 question %d: %d \"✓ Correct\", want 1", i+1, n)
		}
	}
	for _, gone := range []string{"Shapes", "Look", "three", "/media/" + img[2]} {
		if strings.Contains(second, gone) {
			t.Errorf("revision 2 shows %q of revision 1", gone)
		}
	}
}

func TestRevision_NotFound(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	for _, path := range []string{detail + "/revisions/0", detail + "/revisions/99", detail + "/revisions/x", "/admin/exams/999/revisions", "/admin/exams/999/revisions/1"} {
		requireNotFound(t, env, "GET", path, cookie)
	}
}

func TestExamPage_HistoryLinkAndNoRevisionNotice(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	body := getOK(t, env, detail, cookie)
	if !strings.Contains(body, `<a id="history-link" class="button quiet" href="`+detail+`/revisions">History</a>`) {
		t.Errorf("exam page lacks the History link:\n%s", body)
	}
	if strings.Contains(body, `id="no-revision"`) {
		t.Errorf("exam with a revision shows the no-revision notice")
	}

	old := examPath(env.examWithoutRevision(t, "HANDZZ", false))
	notice := `<p id="no-revision" class="frozen-notice">` + enText(t, "examdetail.no_revision") + `</p>`
	if body := getOK(t, env, old, cookie); !strings.Contains(body, notice) {
		t.Errorf("exam page without a revision lacks %s:\n%s", notice, body)
	}
	history := getOK(t, env, old+"/revisions", cookie)
	if !strings.Contains(history, `<p id="no-revision" class="empty">`+enText(t, "examdetail.no_revision")+`</p>`) || strings.Contains(history, `id="revisions"`) {
		t.Errorf("history without a revision: want the notice and no #revisions:\n%s", history)
	}
}
