package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
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
	token, err := env.attempts.Start(context.Background(), env.examCode(t, examID), name)
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
		{fmt.Sprint(ayu), "Ayu", "16:30", "17:30", "Timed out", "0 / 3", "—", "—", "0", "View Delete"},
		{fmt.Sprint(budi), "Budi", "16:31", "16:35", "Submitted", "2 / 3", "—", "—", "0", "View Delete"},
		{fmt.Sprint(citra), "Citra", "16:50", "—", "In progress", "1 / 3", "18:59", code, "0", "View Delete"},
	}
	got := tableRows(body)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows =\n%q\nwant\n%q", got, want)
	}
	for _, s := range []string{`<span id="attempts-date">Sat, 14 Nov 2026</span>`, `title="2026-11-14 16:30"`, `title="2026-11-14 17:30"`} {
		if !strings.Contains(body, s) {
			t.Errorf("list lacks %s:\n%s", s, body)
		}
	}
	citraRow := regexp.MustCompile(`(?s)<tr data-attempt-id="` + fmt.Sprint(citra) + `">.*?</tr>`).FindString(body)
	if cells := cellRe.FindAllString(citraRow, -1); len(cells) < 3 || cells[2] != `<td class="numeric">—</td>` {
		t.Errorf("in-progress Finished cell = %q, want <td class=\"numeric\">—</td>", cells)
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

// startedCells returns the Started column of the attempts table in page order.
func startedCells(body string) []string {
	var cells []string
	for _, row := range tableRows(body) {
		cells = append(cells, row[2])
	}
	return cells
}

func TestAttempts_DatesWhenStartsDiffer(t *testing.T) {
	cases := []struct {
		name    string
		env     func(*testing.T) *testEnv
		started string
		date    bool
	}{
		{"Jakarta", newJakartaEnv, "[14 Nov 23:30 15 Nov 00:15]", false},
		{"UTC", newTestEnv, "[16:30 17:15]", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := c.env(t)
			cookie, _, _ := env.signIn(t)
			id, _ := env.resultsExam(t, 2)
			env.clock = testStart.Add(7 * time.Hour)
			env.startAttempt(t, id, "Ayu")
			env.clock = testStart.Add(7*time.Hour + 45*time.Minute)
			env.startAttempt(t, id, "Budi")

			body := getOK(t, env, attemptsPath(id), cookie)
			if got := fmt.Sprint(startedCells(body)); got != c.started {
				t.Errorf("Started cells = %s, want %s", got, c.started)
			}
			if c.date {
				if !strings.Contains(body, `<span id="attempts-date">Sat, 14 Nov 2026</span>`) {
					t.Errorf("shared date missing:\n%s", body)
				}
			} else if strings.Contains(body, `id="attempts-date"`) {
				t.Errorf("shared date shown although starts differ:\n%s", body)
			}
		})
	}
}

func TestAttempts_FinishedOnAnotherDate(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, _, _ := env.signIn(t)
	id, _ := env.resultsExam(t, 2)
	env.clock = testStart.Add(7 * time.Hour)
	token, _ := env.startAttempt(t, id, "Ayu")
	env.clock = testStart.Add(7*time.Hour + 45*time.Minute)
	env.submitAttempt(t, token)

	body := getOK(t, env, attemptsPath(id), cookie)
	rows := tableRows(body)
	if len(rows) != 1 || rows[0][2] != "23:30" || rows[0][3] != "15 Nov 00:15" {
		t.Errorf("rows = %q, want Started 23:30 and Finished 15 Nov 00:15", rows)
	}
	for _, s := range []string{`title="2026-11-15 00:15"`, `<span id="attempts-date">Sat, 14 Nov 2026</span>`} {
		if !strings.Contains(body, s) {
			t.Errorf("list lacks %s:\n%s", s, body)
		}
	}
}

func TestTeacherPages_NoSeconds(t *testing.T) {
	env := newJakartaEnv(t)
	cookie, _, _ := env.signIn(t)
	id, _ := env.resultsExam(t, 2)
	token, aid := env.startAttempt(t, id, "Ayu")
	env.clock = testStart.Add(time.Minute)
	if _, err := env.attempts.RecordEvent(t.Context(), token, "copy", 0); err != nil {
		t.Fatal(err)
	}
	env.clock = testStart.Add(2 * time.Minute)
	if _, err := env.attempts.RecordEvent(t.Context(), token, "right_click", 0); err != nil {
		t.Fatal(err)
	}
	env.clock = testStart.Add(3 * time.Minute)
	env.submitAttempt(t, token)

	seconds := regexp.MustCompile(`\d\d:\d\d:\d\d`)
	list := getOK(t, env, attemptsPath(id), cookie)
	if m := seconds.FindString(list); m != "" {
		t.Errorf("attempts list shows seconds %q", m)
	}
	detail := getOK(t, env, fmt.Sprintf("%s/%d", attemptsPath(id), aid), cookie)
	if m := seconds.FindString(detail); m != "" {
		t.Errorf("attempt page shows seconds %q", m)
	}
	if !strings.Contains(detail, "2026-11-14 16:30") {
		t.Errorf("attempt page lacks 2026-11-14 16:30:\n%s", detail)
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

// nameFilterEnv starts Ayu Lestari, BAYU Pratama and Citra one minute apart
// and submits Citra. It returns BAYU's token.
func nameFilterEnv(t *testing.T) (*testEnv, http.Header, int64, []exam.Question, string) {
	t.Helper()
	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	id, qs := env.resultsExam(t, 2)
	env.startAttempt(t, id, "Ayu Lestari")
	env.clock = testStart.Add(time.Minute)
	bayu, _ := env.startAttempt(t, id, "BAYU Pratama")
	env.clock = testStart.Add(2 * time.Minute)
	citra, _ := env.startAttempt(t, id, "Citra")
	env.submitAttempt(t, citra)
	return env, cookie, id, qs, bayu
}

func TestAttempts_NameFilter(t *testing.T) {
	t.Run("no attempts", func(t *testing.T) {
		env := newTestEnv(t)
		cookie, _, _ := env.signIn(t)
		id, _ := env.resultsExam(t, 2)
		body := getOK(t, env, attemptsPath(id)+"?name=x", cookie)
		if !strings.Contains(body, "No attempts yet.") || strings.Contains(body, `id="name-filter"`) {
			t.Errorf("empty exam should say No attempts yet. without the filter form:\n%s", body)
		}
	})

	env, cookie, id, _, _ := nameFilterEnv(t)
	cases := []struct {
		query, names, value string
	}{
		{"?name=ayu", "Ayu Lestari BAYU Pratama", "ayu"},
		{"?name=%20AYU%20", "Ayu Lestari BAYU Pratama", "AYU"},
		{"?name=yu%20l", "Ayu Lestari", "yu l"},
	}
	for _, c := range cases {
		body := getOK(t, env, attemptsPath(id)+c.query, cookie)
		if got := strings.Join(rowNames(body), " "); got != c.names {
			t.Errorf("%s: rows = %q, want %q", c.query, got, c.names)
		}
		if want := `name="name" value="` + c.value + `"`; !strings.Contains(body, want) {
			t.Errorf("%s: body lacks %s:\n%s", c.query, want, body)
		}
		if !strings.Contains(body, `id="filter-clear"`) {
			t.Errorf("%s: filtered page lacks the Show all link", c.query)
		}
	}
	if body := getOK(t, env, attemptsPath(id)+"?name=ayu", cookie); !strings.Contains(body, "2 of 3 attempts — 2 in progress, 0 submitted, 0 timed out") {
		t.Errorf("filtered counts line missing:\n%s", body)
	}

	for _, q := range []string{"?name=", ""} {
		body := getOK(t, env, attemptsPath(id)+q, cookie)
		if got := strings.Join(rowNames(body), " "); got != "Ayu Lestari BAYU Pratama Citra" {
			t.Errorf("%q: rows = %q, want all three", q, got)
		}
		if !strings.Contains(body, "3 attempts — 2 in progress, 1 submitted, 0 timed out") {
			t.Errorf("%q: unfiltered counts line missing:\n%s", q, body)
		}
		if strings.Contains(body, `id="filter-clear"`) {
			t.Errorf("%q: unfiltered page shows the Show all link", q)
		}
		form := regexp.MustCompile(`<form[^>]*id="name-filter"[^>]*>`).FindString(body)
		if !strings.Contains(form, `method="get"`) || !strings.Contains(form, `action="`+attemptsPath(id)+`"`) {
			t.Errorf("%q: filter form = %q, want method get to %s", q, form, attemptsPath(id))
		}
	}

	body := getOK(t, env, attemptsPath(id)+"?name=zzz", cookie)
	if strings.Contains(body, `<table id="attempts"`) {
		t.Error("no-match page shows the table")
	}
	for _, s := range []string{"No attempts match ‘zzz’.", "0 of 3 attempts", `name="name" value="zzz"`, `id="filter-clear"`, `id="name-filter"`} {
		if !strings.Contains(body, s) {
			t.Errorf("no-match page lacks %s:\n%s", s, body)
		}
	}
	exportLink := regexp.MustCompile(`<a[^>]*id="export-csv"[^>]*>`).FindString(body)
	if !strings.Contains(exportLink, `href="`+attemptsPath(id)+`.csv"`) {
		t.Errorf("a#export-csv should link the unfiltered CSV: %q", exportLink)
	}

	body = getOK(t, env, attemptsPath(id)+"?name=%3Cb%3E%22", cookie)
	if !strings.Contains(body, `value="&lt;b&gt;&#34;"`) || strings.Contains(body, `<b>"`) {
		t.Errorf("filter value not escaped:\n%s", body)
	}
}

func TestAttempts_NameFilterKeptAcrossSort(t *testing.T) {
	env, cookie, id, qs, bayu := nameFilterEnv(t)
	env.pick(t, bayu, qs[0], 1)

	body := getOK(t, env, attemptsPath(id)+"?name=ayu&sort=score&dir=desc", cookie)
	if got := strings.Join(rowNames(body), " "); got != "BAYU Pratama Ayu Lestari" {
		t.Errorf("rows = %q, want BAYU Pratama then Ayu Lestari", got)
	}
	for _, s := range []string{
		`<a href="?sort=name&amp;dir=asc&amp;name=ayu" class="sort-link">Name`,
		`<a href="?sort=score&amp;dir=asc&amp;name=ayu" class="sort-link">Score`,
		`id="filter-clear" href="?sort=score&amp;dir=desc"`,
		`name="sort" value="score"`,
		`name="dir" value="desc"`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("page lacks %s:\n%s", s, body)
		}
	}

	env.clock = testStart.Add(3 * time.Minute)
	env.startAttempt(t, id, "Dina b&c")
	body = getOK(t, env, attemptsPath(id)+"?name=a+b%26c", cookie)
	if got := strings.Join(rowNames(body), " "); got != "Dina b&amp;c" {
		t.Errorf("rows = %q, want Dina b&amp;c", got)
	}
	// html/template writes + as &#43; in the attribute; the browser's href is
	// the decoded text.
	nameLink := regexp.MustCompile(`<a href="([^"]*)" class="sort-link">Name`).FindStringSubmatch(body)
	if len(nameLink) != 2 || html.UnescapeString(nameLink[1]) != "?sort=name&dir=asc&name=a+b%26c" {
		t.Errorf("Name link = %q, want href ?sort=name&dir=asc&name=a+b%%26c", nameLink)
	}
}

func TestAttempts_CSVIgnoresNameFilter(t *testing.T) {
	env, cookie, id, _, _ := nameFilterEnv(t)
	rec := do(env.h, "GET", attemptsPath(id)+".csv?name=ayu", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET csv = %d", rec.Code)
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("CSV has %d records, want the header plus 3:\n%q", len(records), records)
	}
	var names []string
	for _, r := range records[1:] {
		names = append(names, r[0])
	}
	if got := strings.Join(names, ", "); got != "Ayu Lestari, BAYU Pratama, Citra" {
		t.Errorf("CSV names = %q, want all three", got)
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
	for _, want := range []string{"Ayu", "In progress", "1 / 3", "2026-11-14 09:30"} {
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

func TestAttempt_Rename(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	other, _ := env.resultsExam(t, 2)
	id, qs := env.resultsExam(t, 2)
	token, aid := env.startAttempt(t, id, "Ayu Lestrai")
	env.pick(t, token, qs[0], 1)
	path := attemptPath(id, aid)
	renameForm := regexp.MustCompile(`(?s)<form[^>]*id="rename-form"[^>]*>.*?</form>`)
	nameIs := func(want string) {
		t.Helper()
		if got := env.list(t, id)[0].StudentName; got != want {
			t.Fatalf("stored name = %q, want %q", got, want)
		}
	}

	body := getOK(t, env, path, cookie)
	form := renameForm.FindString(body)
	for _, want := range []string{`method="post"`, `action="` + path + `/rename"`,
		`name="csrf_token" value="` + csrf + `"`, `name="name" value="Ayu Lestrai"`} {
		if !strings.Contains(form, want) {
			t.Errorf("rename form lacks %s:\n%s", want, body)
		}
	}

	if rec := postForm(env.h, path+"/rename", url.Values{"name": {"Ayu Lestari"}}, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without csrf_token = %d, want 403", rec.Code)
	}
	nameIs("Ayu Lestrai")

	rename := func(target, name string) *httptest.ResponseRecorder {
		return postForm(env.h, target, url.Values{"csrf_token": {csrf}, "name": {name}}, cookie)
	}
	for _, target := range []string{attemptPath(other, aid) + "/rename", attemptPath(id, aid+1) + "/rename", attemptsPath(id) + "/x/rename"} {
		if rec := rename(target, "Ayu Lestari"); rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404", target, rec.Code)
		}
		nameIs("Ayu Lestrai")
	}

	for _, name := range []string{"", "   ", strings.Repeat("a", 101)} {
		rec := rename(path+"/rename", name)
		requireAlert(t, rec, http.StatusUnprocessableEntity, "Enter a name (1 to 100 characters).")
		if !strings.Contains(rec.Body.String(), "<h1>Ayu Lestrai</h1>") {
			t.Errorf("refused rename %q: heading is not the stored name:\n%s", name, rec.Body.String())
		}
		if len(name) == 101 && !strings.Contains(renameForm.FindString(rec.Body.String()), `name="name" value="`+name+`"`) {
			t.Errorf("refused rename does not keep the typed value:\n%s", rec.Body.String())
		}
		nameIs("Ayu Lestrai")
	}

	before := env.list(t, id)[0]
	requireRedirect(t, rename(path+"/rename", "  Ayu Lestari  "), path)
	after := env.list(t, id)[0]
	if after.StudentName != "Ayu Lestari" {
		t.Fatalf("stored name = %q, want Ayu Lestari", after.StudentName)
	}
	after.StudentName = before.StudentName
	if after != before {
		t.Fatalf("rename changed more than the name:\nbefore %+v\nafter  %+v", before, after)
	}
	body = getOK(t, env, path, cookie)
	if !strings.Contains(body, "<h1>Ayu Lestari</h1>") || !strings.Contains(renameForm.FindString(body), `name="name" value="Ayu Lestari"`) {
		t.Errorf("attempt page does not show the new name:\n%s", body)
	}
	if names := rowNames(getOK(t, env, attemptsPath(id), cookie)); len(names) != 1 || names[0] != "Ayu Lestari" {
		t.Errorf("attempts list names = %q, want [Ayu Lestari]", names)
	}

	env.submitAttempt(t, token)
	requireRedirect(t, rename(path+"/rename", "Ayu L."), path)
	nameIs("Ayu L.")
}
