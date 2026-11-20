package web

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// summaryText is the catalogue text of key in l formatted with args, as
// localizer.summary builds one clause.
func summaryText(t *testing.T, l lang, key string, args ...any) string {
	t.Helper()
	text := catalogueText(t, l, key)
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// summaryCase is a stored summary and the clauses its Indonesian is built
// from, each a key with its arguments.
type summaryCase struct {
	stored  string
	clauses [][]any // key, then args
}

// text is the summary the clauses of c make in l: settings names
// are given as "summary.setting.*" keys and joined with ", ".
func (c summaryCase) text(t *testing.T, l lang) string {
	t.Helper()
	var parts []string
	for _, clause := range c.clauses {
		args := make([]any, 0, len(clause)-1)
		for _, a := range clause[1:] {
			if names, ok := a.([]string); ok {
				var words []string
				for _, k := range names {
					words = append(words, summaryText(t, l, k))
				}
				a = strings.Join(words, ", ")
			}
			args = append(args, a)
		}
		parts = append(parts, summaryText(t, l, clause[0].(string), args...))
	}
	return strings.Join(parts, "; ")
}

var allSettingKeys = []string{"summary.setting.title", "summary.setting.shuffle", "summary.setting.result_shown", "summary.setting.monitor", "summary.setting.length"}

var summaryCases = []summaryCase{
	{"Imported", [][]any{{"summary.imported"}}},
	{"Replaced from file", [][]any{{"summary.replaced"}}},
	{"Created in the editor", [][]any{{"summary.created"}}},
	{"Copied from A (b); c, revision 9 (ABC234), revision 12", [][]any{{"summary.copied", "A (b); c, revision 9", "ABC234", 12}}},
	{"Question 4 edited", [][]any{{"summary.question_edited", 4}}},
	{"Question 2 edited; correct option changed", [][]any{{"summary.question_edited", 2}, {"summary.correct_changed"}}},
	{"Question 3 edited; accepted answers changed", [][]any{{"summary.question_edited", 3}, {"summary.answers_changed"}}},
	{"Question 5 added", [][]any{{"summary.question_added", 5}}},
	{"Question 1 removed", [][]any{{"summary.question_removed", 1}}},
	{"Question 3 moved to 1", [][]any{{"summary.question_moved", 3, 1}}},
	{"Question 3 moved to section 2", [][]any{{"summary.question_to_section", 3, 2}}},
	{"Settings changed: title, shuffle, result shown, monitor, length", [][]any{{"summary.settings_changed", allSettingKeys}}},
	{"Settings changed: monitor", [][]any{{"summary.settings_changed", []string{"summary.setting.monitor"}}}},
	{"Settings changed: ", [][]any{{"summary.settings_changed", []string(nil)}}},
	{"Section 2 started at question 3", [][]any{{"summary.section_started", 2, 3}}},
	{"Section 1 edited", [][]any{{"summary.section_edited", 1}}},
	{"Section 3 removed", [][]any{{"summary.section_removed", 3}}},
}

func TestLocalizer_Summary(t *testing.T) {
	for _, c := range summaryCases {
		if got := (localizer{langEN}).summary(c.stored); got != c.stored {
			t.Errorf("English summary(%q) = %q, want it as stored", c.stored, got)
		}
		// The English keys hold the app's own forms.
		if got := c.text(t, langEN); got != c.stored {
			t.Errorf("English keys of %q make %q", c.stored, got)
		}
		want := c.text(t, langID)
		if got := (localizer{langID}).summary(c.stored); got != want {
			t.Errorf("Indonesian summary(%q) = %q, want %q", c.stored, got, want)
		}
		if want == c.stored {
			t.Errorf("Indonesian of %q is the English", c.stored)
		}
	}
	for _, stored := range []string{"Unchecked", "Restored by hand (ABC234)", "Question 1 edited; something else", "Settings changed: colour"} {
		for _, l := range []lang{langEN, langID} {
			if got := (localizer{l}).summary(stored); got != stored {
				t.Errorf("%s summary(%q) = %q, want it as stored", l, stored, got)
			}
		}
	}
}

// fixtureSummaries are the summaries of the fixture's physics exam, newest
// first, and of its copy (code filled in by the test).
var fixtureSummaries = []summaryCase{
	{"Question 1 moved to 2", [][]any{{"summary.question_moved", 1, 2}}},
	{"Section 2 started at question 3", [][]any{{"summary.section_started", 2, 3}}},
	{"Question 1 edited; correct option changed", [][]any{{"summary.question_edited", 1}, {"summary.correct_changed"}}},
	{"Settings changed: title, length", [][]any{{"summary.settings_changed", []string{"summary.setting.title", "summary.setting.length"}}}},
	{"Imported", [][]any{{"summary.imported"}}},
}

var copiedCodeRe = regexp.MustCompile(`Copied from Ujian Fisika Baru \(([A-Z0-9]{6})\), revision 5`)

// savedLine is the line under a revision's heading in both languages.
func savedLine(t *testing.T, c summaryCase) shown {
	t.Helper()
	write := func(l lang, summary string) string {
		return string(formatMarkup(markupEscaper.Replace(catalogueText(t, l, "revision.saved")),
			[]any{"2026-11-14 09:30", "UTC", fixtureAdminEmail, summary}))
	}
	return shown{write(langID, c.text(t, langID)), write(langEN, c.stored)}.in(`<p class="zone-note">{}</p>`)
}

func TestHistory_Indonesian(t *testing.T) {
	names := []string{"revisions", "revisions-copy", "revision-copy-1"}
	for n := 1; n <= len(fixtureSummaries); n++ {
		names = append(names, "revision-"+strconv.Itoa(n))
	}
	id := fixturePages(t, langID, names...)
	en := fixturePages(t, langEN, names...)

	m := copiedCodeRe.FindStringSubmatch(en["revisions-copy"])
	if m == nil {
		t.Fatalf("copy history lacks the copied summary:\n%s", en["revisions-copy"])
	}
	copied := summaryCase{m[0], [][]any{{"summary.copied", "Ujian Fisika Baru", m[1], 5}}}

	cell := func(c summaryCase) shown {
		return shown{argEscaper.Replace(c.text(t, langID)), argEscaper.Replace(c.stored)}.in(`<td>{}</td>`)
	}
	th := `<th scope="col">{}</th>`
	listTexts := []shown{
		tx(t, "history.title", "Ujian Fisika Baru").in("<title>{} · Answerbox</title>"),
		tx(t, "history.heading").in("<h1>{}</h1>"),
		tx(t, "history.col.revision").in(th),
		tx(t, "history.col.saved").in(th),
		tx(t, "history.col.teacher").in(th),
		tx(t, "history.col.summary").in(th),
		tx(t, "history.col.actions").in(`<th scope="col"><span class="visually-hidden">{}</span></th>`),
		tx(t, "history.view").in(`/revisions/1">{}</a>`),
		tx(t, "common.times_zone", "UTC").in(`<p class="zone-note">{}</p>`),
	}
	list := listTexts
	for _, c := range fixtureSummaries {
		list = append(list, cell(c))
	}
	requireIndonesian(t, "history", id["revisions"], list...)
	requireIndonesian(t, "copy history", id["revisions-copy"], append(listTexts, cell(copied))...)
	for _, page := range []string{"revisions", "revisions-copy"} {
		requireSameHooks(t, page, id[page], en[page],
			`<tr data-revision="\d+">`, `data-exam-id="\d+"`, `href="/admin/exams/\d+/revisions/\d+"`, `href="/admin/exams/\d+"`, `<td class="numeric">[^<]*</td>`)
	}

	var stored []string
	for _, row := range revisionRows(en["revisions"]) {
		stored = append(stored, row[4])
	}
	if want := []string{fixtureSummaries[0].stored, fixtureSummaries[1].stored, fixtureSummaries[2].stored, fixtureSummaries[3].stored, fixtureSummaries[4].stored}; strings.Join(stored, "|") != strings.Join(want, "|") {
		t.Errorf("English history summaries = %q, want %q", stored, want)
	}

	revision := func(name string, n int, c summaryCase) {
		t.Helper()
		requireIndonesian(t, name, id[name],
			tx(t, "revision.title", n, "Ujian Fisika Baru").in("<title>{} · Answerbox</title>"),
			tx(t, "revision.back").in(`/revisions">{}</a></p>`),
			tx(t, "revision.heading", n).in("<h1>{}</h1>"),
			savedLine(t, c),
			tx(t, "revision.title_label").in("<dt>{}</dt>"),
			tx(t, "setting.length").in("<dt>{}</dt>"),
			tx(t, "setting.minutes", 40).in("<dd>{}</dd>"),
			tx(t, "setting.shuffle").in("<dt>{}</dt>"),
			gox(t, "setting.shuffle.none").in("<dd>{}</dd>"),
			tx(t, "setting.result_shown").in("<dt>{}</dt>"),
			gox(t, "setting.show_result.score").in("<dd>{}</dd>"),
			tx(t, "setting.monitor").in("<dt>{}</dt>"),
			tx(t, "setting.monitor_on").in("<dd>{}</dd>"),
			tx(t, "setting.questions").in("<dt>{}</dt>"),
			tx(t, "revision.questions").in("<h2>{}</h2>"))
		requireSameHooks(t, name, id[name], en[name], `data-revision="\d+"`, `data-exam-id="\d+"`, `href="/admin/exams/\d+/revisions"`)
		if line := savedLine(t, c).en; !strings.Contains(en[name], line) {
			t.Errorf("%s: English lacks %q", name, line)
		}
	}
	for i, c := range fixtureSummaries {
		n := len(fixtureSummaries) - i
		if n == 1 {
			continue // imported with 60 minutes; its settings differ
		}
		revision("revision-"+strconv.Itoa(n), n, c)
	}
	requireIndonesian(t, "revision-1", id["revision-1"], savedLine(t, fixtureSummaries[4]))
	revision("revision-copy-1", 1, copied)
	requireUnknownSummaryAsStored(t)
}

// requireUnknownSummaryAsStored checks that a stored summary the app does
// not write is shown as stored in both languages.
func requireUnknownSummaryAsStored(t *testing.T) {
	t.Helper()
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	examID, err := strconv.ParseInt(strings.TrimPrefix(detail, "/admin/exams/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	const odd = "Restored by hand; Question 1 edited"
	if _, err := env.db.Exec(`INSERT INTO exam_revision (exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		SELECT exam_id, 2, created_at, teacher_id, ?, title, shuffle, show_result, monitor, duration_minutes, media_key, content
		FROM exam_revision WHERE exam_id = ? AND number = 1`, odd, examID); err != nil {
		t.Fatal(err)
	}
	for _, l := range []lang{langID, langEN} {
		rows := revisionRows(getOK(t, env, detail+"/revisions", langHeader(cookie, l)))
		if len(rows) != 2 || rows[0][4] != odd {
			t.Errorf("%s history rows = %q, want %q first", l, rows, odd)
		}
		if l == langID && rows[1][4] != summaryText(t, langID, "summary.imported") {
			t.Errorf("id history: revision 1 = %q, want the Indonesian of Imported", rows[1][4])
		}
		if body := getOK(t, env, detail+"/revisions/2", langHeader(cookie, l)); !strings.Contains(body, ": "+odd+"</p>") {
			t.Errorf("%s revision 2 does not show %q as stored:\n%s", l, odd, body)
		}
	}
}

func TestHistory_StoredSummaryStaysEnglish(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	detail := importExam(t, env, cookie, csrf, "geometry.md", []byte(textExam))
	form := url.Values{"revision": {"1"}, "title": {"Geometry II"}, "shuffle": {"none"}, "show_result": {"review"}, "monitor": {"on"}, "duration": {"45"}}
	requireRedirect(t, editorPost(env, detail, "/settings", form, langHeader(cookie, langID), csrf), detail+"/edit")
	var summary string
	if err := env.db.QueryRow(`SELECT summary FROM exam_revision WHERE exam_id = ? AND number = 2`,
		strings.TrimPrefix(detail, "/admin/exams/")).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != "Settings changed: title, length" {
		t.Errorf("stored summary = %q, want the English sentence", summary)
	}
	rows := revisionRows(getOK(t, env, detail+"/revisions", langHeader(cookie, langID)))
	if want := (summaryCase{summary, [][]any{{"summary.settings_changed", []string{"summary.setting.title", "summary.setting.length"}}}}).text(t, langID); len(rows) != 2 || rows[0][4] != want {
		t.Errorf("id history rows = %q, want %q first", rows, want)
	}
}
