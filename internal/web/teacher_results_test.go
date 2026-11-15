package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

func newJakartaEnv(t *testing.T) *testEnv {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	return newTestEnvWith(t, sqlitetest.Open(t), envConfig{location: loc})
}

// resultsExam opens an exam of n shuffled questions with three options each,
// option 1 correct, and returns its ID and questions in file order.
func (env *testEnv) resultsExam(t *testing.T, n int) (int64, []exam.Question) {
	t.Helper()
	id := env.openExam(t, examText("questions, options", "score", n, 3))
	qs, err := env.exams.Questions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return id, qs
}

// startAttempt starts an attempt through the service at the current clock and
// returns its token and ID.
func (env *testEnv) startAttempt(t *testing.T, examID int64, name string) (string, int64) {
	t.Helper()
	token, err := env.attempts.Start(context.Background(), name)
	if err != nil {
		t.Fatalf("Start(%q): %v", name, err)
	}
	rows := env.list(t, examID)
	return token, rows[len(rows)-1].ID
}

// pick saves option position optPos (1-based, file order) for question q.
func (env *testEnv) pick(t *testing.T, token string, q exam.Question, optPos int) {
	t.Helper()
	if _, err := env.attempts.SaveAnswer(context.Background(), token, q.ID, q.Options[optPos-1].ID); err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}
}

func (env *testEnv) submitAttempt(t *testing.T, token string) {
	t.Helper()
	if err := env.attempts.Submit(context.Background(), token); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

var (
	attemptRowRe = regexp.MustCompile(`(?s)<tr data-attempt-id="(\d+)">(.*?)</tr>`)
	cellRe       = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	tagRe        = regexp.MustCompile(`<[^>]+>`)
)

// tableRows returns the text of every cell of every attempts-table row, in
// page order, keyed by nothing: row[0] is the attempt ID.
func tableRows(body string) [][]string {
	var out [][]string
	for _, m := range attemptRowRe.FindAllStringSubmatch(body, -1) {
		row := []string{m[1]}
		for _, c := range cellRe.FindAllStringSubmatch(m[2], -1) {
			row = append(row, strings.Join(strings.Fields(tagRe.ReplaceAllString(c[1], "")), " "))
		}
		out = append(out, row)
	}
	return out
}

// rowNames returns the Name column of the attempts table in page order.
func rowNames(body string) []string {
	var names []string
	for _, row := range tableRows(body) {
		names = append(names, row[1])
	}
	return names
}

func getOK(t *testing.T, env *testEnv, path string, cookie http.Header) string {
	t.Helper()
	rec := do(env.h, "GET", path, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d:\n%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func requireNotFound(t *testing.T, env *testEnv, method, path string, cookie http.Header) {
	t.Helper()
	if rec := do(env.h, method, path, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("%s %s = %d, want 404", method, path, rec.Code)
	}
}

func TestAttempts_EmptyState(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	id, _ := env.resultsExam(t, 2)

	body := getOK(t, env, attemptsPath(id), cookie)
	if !strings.Contains(body, "No attempts yet.") {
		t.Errorf("empty state text missing:\n%s", body)
	}
	if strings.Contains(body, `<table id="attempts"`) {
		t.Error("attempts table shown without attempts")
	}
	requireNotFound(t, env, "GET", attemptsPath(id+1), cookie)
	requireNotFound(t, env, "GET", "/admin/exams/abc/attempts", cookie)
}

func TestAttempts_ListRows(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, _, _ := env.signIn(t)
	id, qs := env.resultsExam(t, 3)

	_, ayu := env.startAttempt(t, id, "Ayu")
	env.clock = testStart.Add(time.Minute)
	budiToken, budi := env.startAttempt(t, id, "Budi")
	env.pick(t, budiToken, qs[0], 1)
	env.pick(t, budiToken, qs[1], 1)
	env.pick(t, budiToken, qs[2], 2)
	env.clock = testStart.Add(5 * time.Minute)
	env.submitAttempt(t, budiToken)
	env.clock = testStart.Add(20 * time.Minute)
	citraToken, citra := env.startAttempt(t, id, "Citra")
	env.pick(t, citraToken, qs[1], 1)
	env.clock = testStart.Add(61*time.Minute + 500*time.Millisecond) // Ayu is past her deadline and grace

	rows := env.list(t, id)
	code := rows[2].ResumeCode
	if code == "" {
		t.Fatal("in-progress attempt has no resume code")
	}

	body := getOK(t, env, attemptsPath(id), cookie)
	if !strings.Contains(body, `<table id="attempts"`) {
		t.Fatalf("table#attempts missing:\n%s", body)
	}
	for _, header := range []string{"Name", "Started", "Finished", "Status", "Score", "Time left", "Resume code", "Flags"} {
		if !regexp.MustCompile(`(?s)<th[^>]*>\s*(<a[^>]*>)?` + header + `\b`).MatchString(body) {
			t.Errorf("column %q missing", header)
		}
	}
	want := [][]string{
		{fmt.Sprint(ayu), "Ayu", "2026-11-14 16:30:00", "2026-11-14 17:30:00", "Timed out", "0 / 3", "—", "—", "0", "View Delete"},
		{fmt.Sprint(budi), "Budi", "2026-11-14 16:31:00", "2026-11-14 16:35:00", "Submitted", "2 / 3", "—", "—", "0", "View Delete"},
		{fmt.Sprint(citra), "Citra", "2026-11-14 16:50:00", "—", "In progress", "1 / 3", "18:59", code, "0", "View Delete"},
	}
	got := tableRows(body)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows =\n%q\nwant\n%q", got, want)
	}
	if strings.Count(body, `<code class="resume-code">`) != 1 || !strings.Contains(body, `<code class="resume-code">`+code+`</code>`) {
		t.Errorf("resume code should appear once, in code.resume-code:\n%s", body)
	}
	for _, aid := range []int64{ayu, budi, citra} {
		view := fmt.Sprintf(`href="%s/%d"`, attemptsPath(id), aid)
		del := fmt.Sprintf(`href="%s/%d/delete"`, attemptsPath(id), aid)
		if !strings.Contains(body, view) || !strings.Contains(body, del) {
			t.Errorf("attempt %d lacks View %s or Delete %s links", aid, view, del)
		}
	}
	if !strings.Contains(body, "3 attempts — 1 in progress, 1 submitted, 1 timed out") {
		t.Errorf("counts line missing:\n%s", body)
	}
	if !strings.Contains(body, "Times are shown in Asia/Jakarta.") {
		t.Errorf("zone line missing:\n%s", body)
	}
	exportLink := regexp.MustCompile(`<a[^>]*id="export-csv"[^>]*>`).FindString(body)
	if !strings.Contains(exportLink, `href="`+attemptsPath(id)+`.csv"`) {
		t.Errorf("a#export-csv missing or wrong: %q", exportLink)
	}
}

func TestAttempts_Sort(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	id, qs := env.resultsExam(t, 2)
	scores := []struct {
		name  string
		score int
	}{{"bob", 1}, {"Alice", 2}, {"carol", 1}, {"alice", 0}}
	for i, s := range scores {
		env.clock = testStart.Add(time.Duration(i) * time.Minute)
		token, _ := env.startAttempt(t, id, s.name)
		for q := range s.score {
			env.pick(t, token, qs[q], 1)
		}
	}

	cases := []struct {
		query     string
		names     string
		ariaSort  string // column text that carries aria-sort
		direction string
	}{
		{"", "bob Alice carol alice", "Started", "ascending"},
		{"?sort=name&dir=asc", "Alice alice bob carol", "Name", "ascending"},
		{"?sort=name&dir=desc", "carol bob Alice alice", "Name", "descending"},
		{"?sort=score&dir=desc", "Alice bob carol alice", "Score", "descending"},
		{"?sort=score&dir=asc", "alice bob carol Alice", "Score", "ascending"},
		{"?sort=score", "alice bob carol Alice", "Score", "ascending"},
		{"?sort=nope&dir=sideways", "bob Alice carol alice", "Started", "ascending"},
		{"?dir=desc&sort=", "alice carol Alice bob", "Started", "descending"},
	}
	sortedTh := regexp.MustCompile(`(?s)<th[^>]*aria-sort="([a-z]+)"[^>]*>(.*?)</th>`)
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			body := getOK(t, env, attemptsPath(id)+c.query, cookie)
			if got := strings.Join(rowNames(body), " "); got != c.names {
				t.Errorf("order = %q, want %q", got, c.names)
			}
			ths := sortedTh.FindAllStringSubmatch(body, -1)
			if len(ths) != 1 {
				t.Fatalf("%d headers carry aria-sort, want 1:\n%s", len(ths), body)
			}
			arrow := map[string]string{"ascending": "▲", "descending": "▼"}[c.direction]
			if ths[0][1] != c.direction || !strings.Contains(ths[0][2], c.ariaSort) || !strings.Contains(ths[0][2], arrow) {
				t.Errorf("sorted header = %q %q, want %s %s with %s", ths[0][1], ths[0][2], c.ariaSort, c.direction, arrow)
			}
		})
	}

	links := []struct{ query, name, score string }{
		{"", "?sort=name&amp;dir=asc", "?sort=score&amp;dir=desc"},
		{"?sort=name&dir=asc", "?sort=name&amp;dir=desc", "?sort=score&amp;dir=desc"},
		{"?sort=name&dir=desc", "?sort=name&amp;dir=asc", "?sort=score&amp;dir=desc"},
		{"?sort=score&dir=desc", "?sort=name&amp;dir=asc", "?sort=score&amp;dir=asc"},
		{"?sort=score&dir=asc", "?sort=name&amp;dir=asc", "?sort=score&amp;dir=desc"},
	}
	for _, l := range links {
		body := getOK(t, env, attemptsPath(id)+l.query, cookie)
		if !regexp.MustCompile(`<a href="` + regexp.QuoteMeta(l.name) + `"[^>]*>Name`).MatchString(body) {
			t.Errorf("%q: Name link should be %s:\n%s", l.query, l.name, body)
		}
		if !regexp.MustCompile(`<a href="` + regexp.QuoteMeta(l.score) + `"[^>]*>Score`).MatchString(body) {
			t.Errorf("%q: Score link should be %s:\n%s", l.query, l.score, body)
		}
	}
}

func TestAttempts_CSV(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, _, _ := env.signIn(t)
	id, qs := env.resultsExam(t, 3)

	env.startAttempt(t, id, "Late")
	env.clock = testStart.Add(10*time.Minute + 750*time.Millisecond)
	formula, _ := env.startAttempt(t, id, "=SUM(A1)")
	env.pick(t, formula, qs[0], 1)
	env.pick(t, formula, qs[2], 1)
	env.clock = testStart.Add(15 * time.Minute)
	env.submitAttempt(t, formula)
	env.clock = testStart.Add(30 * time.Minute)
	quoted, _ := env.startAttempt(t, id, `Doe, "JJ"`)
	env.pick(t, quoted, qs[1], 2)
	env.clock = testStart.Add(61 * time.Minute)

	rec := do(env.h, "GET", attemptsPath(id)+".csv", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET csv = %d:\n%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd, want := rec.Header().Get("Content-Disposition"), fmt.Sprintf(`attachment; filename="exam-%d-attempts.csv"`, id); cd != want {
		t.Errorf("Content-Disposition = %q, want %q", cd, want)
	}
	body := rec.Body.String()
	if first, _, _ := strings.Cut(body, "\n"); first != "name,status,score,total,started,finished,flags" {
		t.Errorf("header row = %q", first)
	}
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v\n%s", err, body)
	}
	want := [][]string{
		{"name", "status", "score", "total", "started", "finished", "flags"},
		{"Late", "timed out", "0", "3", "2026-11-14T16:30:00+07:00", "2026-11-14T17:30:00+07:00", "0"},
		{"'=SUM(A1)", "submitted", "2", "3", "2026-11-14T16:40:00+07:00", "2026-11-14T16:45:00+07:00", "0"},
		{`Doe, "JJ"`, "in progress", "0", "3", "2026-11-14T17:00:00+07:00", "", "0"},
	}
	if fmt.Sprintf("%q", records) != fmt.Sprintf("%q", want) {
		t.Errorf("records =\n%q\nwant\n%q", records, want)
	}

	requireNotFound(t, env, "GET", attemptsPath(id+1)+".csv", cookie)
}

func TestCSVName_FormulaGuard(t *testing.T) {
	for in, want := range map[string]string{
		"=SUM(A1)": "'=SUM(A1)", "+1": "'+1", "-2": "'-2", "@x": "'@x", "\tx": "'\tx", "\rx": "'\rx",
		"Ayu": "Ayu", "a=b": "a=b", "": "",
	} {
		if got := csvName(in); got != want {
			t.Errorf("csvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAttempt_DetailPage(t *testing.T) {
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	other, _ := env.resultsExam(t, 2)
	id, qs := env.resultsExam(t, 3)
	token, aid := env.startAttempt(t, id, "Ayu")
	env.pick(t, token, qs[0], 1)
	env.pick(t, token, qs[1], 3)

	path := fmt.Sprintf("%s/%d", attemptsPath(id), aid)
	body := getOK(t, env, path, cookie)
	for _, want := range []string{"Ayu", "In progress", "1 / 3", "2026-11-14 09:30:00"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	sections := regexp.MustCompile(`(?s)<section class="detail-question"[^>]*>.*?</section>`).FindAllString(body, -1)
	if len(sections) != 3 {
		t.Fatalf("%d question sections, want 3:\n%s", len(sections), body)
	}
	options := regexp.MustCompile(`(?s)<li[^>]*>.*?</li>`)
	picked := map[int]int{0: 1, 1: 3} // question index → picked option position
	for i, sec := range sections {
		if !strings.Contains(sec, fmt.Sprintf("<p>Question %d</p>", i+1)) {
			t.Errorf("section %d is not question %d in file order:\n%s", i, i+1, sec)
		}
		if n := strings.Count(sec, "✓ Correct"); n != 1 {
			t.Errorf("question %d shows %d \"✓ Correct\", want 1", i+1, n)
		}
		opts := options.FindAllString(sec, -1)
		if len(opts) != 3 {
			t.Fatalf("question %d has %d options, want 3", i+1, len(opts))
		}
		for j, o := range opts {
			if !strings.Contains(o, fmt.Sprintf("Option %d.%d", i+1, j+1)) {
				t.Errorf("question %d option %d out of file order: %s", i+1, j+1, o)
			}
			if want := picked[i] == j+1; strings.Contains(o, "Student's pick") != want {
				t.Errorf("question %d option %d: Student's pick shown = %v, want %v", i+1, j+1, !want, want)
			}
			if want := j == 0; strings.Contains(o, "✓ Correct") != want {
				t.Errorf("question %d option %d: ✓ Correct shown = %v, want %v", i+1, j+1, !want, want)
			}
		}
		if want := i == 2; strings.Contains(sec, "No answer") != want {
			t.Errorf("question %d: No answer shown = %v, want %v", i+1, !want, want)
		}
	}

	requireNotFound(t, env, "GET", fmt.Sprintf("%s/%d", attemptsPath(other), aid), cookie)
	requireNotFound(t, env, "GET", fmt.Sprintf("%s/%d", attemptsPath(id), aid+1), cookie)
	requireNotFound(t, env, "GET", attemptsPath(id)+"/x", cookie)
}

func TestAttempt_Delete(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	other, _ := env.resultsExam(t, 2)
	id, qs := env.resultsExam(t, 2)
	token, aid := env.startAttempt(t, id, "Ayu")
	env.pick(t, token, qs[0], 1)
	path := fmt.Sprintf("%s/%d/delete", attemptsPath(id), aid)

	body := getOK(t, env, path, cookie)
	if !strings.Contains(body, "Delete the attempt by ‘Ayu’? This cannot be undone.") {
		t.Errorf("confirmation text missing:\n%s", body)
	}
	form := regexp.MustCompile(`(?s)<form[^>]*action="` + regexp.QuoteMeta(path) + `"[^>]*>.*?</form>`).FindString(body)
	if !strings.Contains(form, `method="post"`) || !strings.Contains(form, `name="csrf_token" value="`+csrf+`"`) ||
		!strings.Contains(form, "Delete attempt") {
		t.Errorf("delete form with csrf_token missing:\n%s", body)
	}
	if !regexp.MustCompile(`<a[^>]*href="` + regexp.QuoteMeta(attemptsPath(id)) + `"[^>]*>Cancel</a>`).MatchString(body) {
		t.Errorf("Cancel link to the list missing:\n%s", body)
	}
	if len(env.list(t, id)) != 1 {
		t.Fatal("GET confirmation changed the attempts")
	}
	requireNotFound(t, env, "GET", fmt.Sprintf("%s/%d/delete", attemptsPath(other), aid), cookie)

	if rec := postForm(env.h, path, nil, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without csrf_token = %d, want 403", rec.Code)
	}
	if len(env.list(t, id)) != 1 {
		t.Fatal("POST without csrf_token deleted the attempt")
	}
	withToken := url.Values{"csrf_token": {csrf}}
	if rec := postForm(env.h, fmt.Sprintf("%s/%d/delete", attemptsPath(other), aid), withToken, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("POST under the wrong exam = %d, want 404", rec.Code)
	}
	requireRedirect(t, postForm(env.h, path, withToken, cookie), attemptsPath(id))
	if rows := env.list(t, id); len(rows) != 0 {
		t.Fatalf("attempt still listed after delete: %+v", rows)
	}
	if rec := postForm(env.h, path, withToken, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("second POST = %d, want 404", rec.Code)
	}
}
