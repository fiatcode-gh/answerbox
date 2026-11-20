package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
)

// countsx is the counts line of the results list in both languages: total
// attempts, shown of them when filtered, then the status counts.
func countsx(t *testing.T, total, shownRows int, filtered bool, inProgress, submitted, timedOut int) shown {
	t.Helper()
	write := func(l lang) string {
		count := fmt.Sprintf(catalogueText(t, l, pluralKey("results.count", total)), total)
		if filtered {
			count = fmt.Sprintf(catalogueText(t, l, pluralKey("results.count_of", total)), total, shownRows)
		}
		return argEscaper.Replace(fmt.Sprintf(catalogueText(t, l, "results.counts_line"), count, inProgress, submitted, timedOut))
	}
	return shown{write(langID), write(langEN)}
}

// join puts the texts one after another with sep between them.
func join(sep string, texts ...shown) shown {
	var out shown
	for i, s := range texts {
		if i > 0 {
			out.id += sep
			out.en += sep
		}
		out.id += s.id
		out.en += s.en
	}
	return out
}

// requireEnglishText checks a counts line or similar against the English
// the page wrote before the move into the catalogue.
func requireEnglishText(t *testing.T, label string, got shown, want string) {
	t.Helper()
	if got.en != want {
		t.Errorf("%s: English %q, want %q", label, got.en, want)
	}
}

// requireSameHooks checks that every match of each pattern is the same in
// the Indonesian and the English page.
func requireSameHooks(t *testing.T, label, id, en string, patterns ...string) {
	t.Helper()
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		got, want := re.FindAllString(id, -1), re.FindAllString(en, -1)
		if len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("%s: %s = %q, English %q", label, p, got, want)
		}
	}
}

func TestResults_Indonesian(t *testing.T) {
	names := []string{"attempts", "attempts-filter-one", "attempts-filter-none", "attempts-empty"}
	id := fixturePages(t, langID, names...)
	en := fixturePages(t, langEN, names...)

	list := id["attempts"]
	th := `<th scope="col">{}</th>`
	wide := `<th scope="col" class="wrap">{}</th>`
	all := countsx(t, 3, 3, false, 1, 1, 1)
	requireEnglishText(t, "counts", all, "3 attempts — 1 in progress, 1 submitted, 1 timed out")
	requireIndonesian(t, "list", list,
		tx(t, "results.title", "Ujian Matematika").in("<title>{} · Answerbox</title>"),
		tx(t, "results.eyebrow").in(`<p class="eyebrow">{}</p>`),
		tx(t, "results.filter_label").in(`<label for="filter-name" class="visually-hidden">{}</label>`),
		tx(t, "results.filter_label").in(`placeholder="{}"`),
		tx(t, "results.filter").in(`<button type="submit" class="secondary">{}</button>`),
		tx(t, "results.export").in(`/attempts.csv">{}</a>`),
		tx(t, "results.live_text").in(`data-live-text="{}"`),
		tx(t, "results.stale_text").in(`data-stale-text="{}"`),
		all.in(`<p class="counts">{}</p>`),
		join("", tx(t, "results.date"), shown{` <span id="attempts-date">Sab, 14 Nov 2026</span>. `, ` <span id="attempts-date">Sat, 14 Nov 2026</span>. `},
			tx(t, "common.times_zone", "UTC")).in(`<p class="zone-note">{}</p>`),
		tx(t, "results.col.name").in(`class="sort-link">{}</a>`),
		tx(t, "results.col.started").in(`<th scope="col" aria-sort="ascending">{} <span aria-hidden="true">▲</span></th>`),
		tx(t, "results.col.finished").in(th),
		tx(t, "results.col.status").in(th),
		tx(t, "results.col.score").in(`class="sort-link">{}</a>`),
		tx(t, "results.col.time_left").in(wide),
		tx(t, "results.col.resume_code").in(wide),
		tx(t, "results.col.flags").in(th),
		tx(t, "results.col.actions").in(th),
		gox(t, "status.timed_out").in(`<span class="badge badge-muted">{}</span>`),
		gox(t, "status.submitted").in(`<span class="badge badge-ok">{}</span>`),
		gox(t, "status.in_progress").in(`<span class="badge badge-warn">{}</span>`),
		tx(t, "results.view").in(`">{}</a> <a class="danger-link"`),
		tx(t, "results.delete").in(`/delete">{}</a></td>`))
	requireSameHooks(t, "list", list, en["attempts"],
		`<tr data-attempt-id="\d+">`, `data-flags="\d+"`, `data-live="[a-z]+"`, `href="\?sort=[^"]*"`, `aria-sort="[a-z]+"`,
		`<td class="numeric"[^>]*>[^<]*</td>`, `<code class="resume-code">[^<]*</code>`, `class="wrap section-score">[^<]*<`)

	one := countsx(t, 3, 1, true, 0, 1, 0)
	requireEnglishText(t, "filtered counts", one, "1 of 3 attempts — 0 in progress, 1 submitted, 0 timed out")
	requireIndonesian(t, "filtered", id["attempts-filter-one"],
		one.in(`<p class="counts">{}</p>`),
		tx(t, "results.show_all").in(`<a id="filter-clear" href="?sort=started&amp;dir=asc">{}</a>`))
	requireSameHooks(t, "filtered", id["attempts-filter-one"], en["attempts-filter-one"], `<tr data-attempt-id="\d+">`, `href="\?sort=[^"]*"`)

	none := countsx(t, 3, 0, true, 0, 0, 0)
	requireEnglishText(t, "no match counts", none, "0 of 3 attempts — 0 in progress, 0 submitted, 0 timed out")
	requireIndonesian(t, "no match", id["attempts-filter-none"],
		none.in(`<p class="counts">{}</p>`),
		tx(t, "results.no_match", "zaki").in(`<p class="empty" id="no-match">{}</p>`))
	requireIndonesian(t, "empty", id["attempts-empty"], tx(t, "results.empty").in(`<p class="empty">{}</p>`))
	requireSameHooks(t, "empty", id["attempts-empty"], en["attempts-empty"], `data-live="[a-z]+"`)

	env := newTestEnv(t)
	cookie, _, _ := env.signIn(t)
	exam, _ := env.resultsExam(t, 1)
	token, _ := env.startAttempt(t, exam, "Ayu")
	env.submitAttempt(t, token)
	single := countsx(t, 1, 1, false, 0, 1, 0)
	requireEnglishText(t, "one attempt", single, "1 attempt — 0 in progress, 1 submitted, 0 timed out")
	requireIndonesian(t, "one attempt", getOK(t, env, attemptsPath(exam), langHeader(cookie, langID)), single.in(`<p class="counts">{}</p>`))
	if body := getOK(t, env, attemptsPath(exam), langHeader(cookie, langEN)); !strings.Contains(body, single.in(`<p class="counts">{}</p>`).en) {
		t.Errorf("one attempt: English counts line missing:\n%s", body)
	}
}

func TestLocalizer_StatusEvent(t *testing.T) {
	statuses := []struct {
		st          attempt.Status
		text, badge string
	}{
		{attempt.InProgress, "In progress", "badge-warn"},
		{attempt.Submitted, "Submitted", "badge-ok"},
		{attempt.TimedOut, "Timed out", "badge-muted"},
	}
	for _, c := range statuses {
		if got := (localizer{langEN}).status(c.st); got != (statusLabel{c.text, c.badge}) {
			t.Errorf("en status(%s) = %+v, want %q %q", c.st, got, c.text, c.badge)
		}
		id := (localizer{langID}).status(c.st)
		if want := catalogueText(t, langID, "status."+string(c.st)); id.Text != want || id.Badge != c.badge {
			t.Errorf("id status(%s) = %+v, want %q %q", c.st, id, want, c.badge)
		}
		if id.Text == c.text {
			t.Errorf("id status(%s) is the English %q", c.st, id.Text)
		}
	}

	events := []struct {
		kind attempt.EventKind
		text string
	}{
		{attempt.LeftPage, "Left the exam page"},
		{attempt.Copy, "Tried to copy"},
		{attempt.Paste, "Tried to paste"},
		{attempt.RightClick, "Right-clicked"},
		{attempt.Resize, "Resized the window"},
	}
	for _, c := range events {
		if got := (localizer{langEN}).event(c.kind); got != c.text {
			t.Errorf("en event(%s) = %q, want %q", c.kind, got, c.text)
		}
		id := (localizer{langID}).event(c.kind)
		if want := catalogueText(t, langID, "event."+string(c.kind)); id != want || id == c.text {
			t.Errorf("id event(%s) = %q, want %q and not the English", c.kind, id, want)
		}
	}
}

func TestAttemptPage_Indonesian(t *testing.T) {
	names := []string{"attempt-submitted", "attempt-in-progress", "attempt-rename-refused", "attempt-override-refused", "confirm-delete-attempt"}
	id := fixturePages(t, langID, names...)
	en := fixturePages(t, langEN, names...)

	page := id["attempt-submitted"]
	dt := `<dt>{}</dt>`
	requireIndonesian(t, "attempt", page,
		tx(t, "attempt.back", "Ujian Matematika").in(`/attempts">← {}</a>`),
		gox(t, "status.submitted").in(`<span class="badge badge-ok">{}</span>`),
		tx(t, "attempt.score").in(dt),
		tx(t, "attempt.started").in(dt),
		tx(t, "attempt.finished").in(dt),
		tx(t, "attempt.content").in(dt),
		tx(t, "attempt.revision", 1).in(`/revisions/1">{}</a>`),
		join(" ", tx(t, "common.times_zone", "UTC"), tx(t, "attempt.file_order")).in(`<p class="zone-note">{}</p>`),
		tx(t, "attempt.rename_label").in(`<label for="rename-name">{}</label>`),
		tx(t, "attempt.rename").in(`<button type="submit" class="secondary">{}</button>`),
		tx(t, "attempt.flags").in(`<h2>{}</h2>`),
		join(`</th><th scope="col">`, tx(t, "attempt.col.time"), tx(t, "attempt.col.event"), tx(t, "attempt.col.question")).in(`<tr><th scope="col">{}</th></tr>`),
		gox(t, "event.left_page").in(`<td>{}</td>`),
		gox(t, "event.copy").in(`<td>{}</td>`),
		tx(t, "attempt.question", 1).in(`<a href="#q1">{}</a>`),
		tx(t, "attempt.answers").in(`<h2 class="answers-heading">{}</h2>`),
		tx(t, "attempt.question", 1).in(`<p class="eyebrow">{}</p>`),
		join("", tx(t, "attempt.question", 4), tx(t, "attempt.no_answer").in(` · <span class="no-answer">{}</span>`)).in(`<p class="eyebrow">{}</p>`),
		tx(t, "attempt.typed").in(`<p class="typed-answer">{} <span class="typed-text">Batavia</span></p>`),
		tx(t, "attempt.accepted").in(`<p class="accepted-label">{}</p>`),
		tx(t, "attempt.auto_wrong").in(`<p class="auto-mark" data-auto="false">{}</p>`),
		thx(t, "attempt.marked_right_html", fixtureAdminEmail, "2026-11-14 10:10").in(`<p class="override-mark" data-override="true">{}</p>`),
		tx(t, "attempt.mark_label").in(`<span class="override-label">{}</span>`),
		tx(t, "attempt.mark_right").in(`name="mark" value="right" class="secondary">{}</button>`),
		tx(t, "attempt.mark_wrong").in(`name="mark" value="wrong" class="secondary">{}</button>`),
		tx(t, "attempt.remove_override").in(`name="mark" value="none" class="secondary">{}</button>`),
		tx(t, "attempt.correct").in(`<span class="correct-mark">{}</span>`),
		tx(t, "attempt.pick").in(`<span class="pick-mark">{}</span>`),
		tx(t, "attempt.pick").in(`<span class="pick-mark">✗ {}</span>`))
	requireSameHooks(t, "attempt", page, en["attempt-submitted"],
		`<li data-correct="[a-z]+" data-picked="[a-z]+">`, `data-auto="[a-z]+"`, `data-override="[a-z]+"`, `name="mark" value="[a-z]+"`,
		`<td class="numeric">[^<]*</td>`, `href="#q\d+"`, `<dd class="numeric">[^<]*</dd>`)

	requireIndonesian(t, "in progress", id["attempt-in-progress"],
		gox(t, "status.in_progress").in(`<span class="badge badge-warn">{}</span>`),
		gox(t, "event.paste").in(`<td>{}</td>`))
	requireIndonesian(t, "rename refused", id["attempt-rename-refused"], alertDiv(gox(t, "attempt.rename_refused")))
	requireIndonesian(t, "override refused", id["attempt-override-refused"], alertDiv(gox(t, "attempt.override_refused")))

	requireIndonesian(t, "delete", id["confirm-delete-attempt"],
		gox(t, "attempt.confirm_delete.heading").in("<title>{} · Answerbox</title>"),
		gox(t, "attempt.confirm_delete.heading").in("<h1>{}</h1>"),
		gox(t, "attempt.confirm_delete.question", "Tono").in(`<p class="confirm-question">{}</p>`),
		gox(t, "attempt.confirm_delete.submit").in(`<button type="submit" class="danger">{}</button>`),
		tx(t, "confirm.cancel").in(`">{}</a>`))
	if !strings.Contains(id["confirm-delete-attempt"], "‘Tono’") {
		t.Errorf("delete: the name is not in ‘ ’")
	}
	requireSameHooks(t, "delete", id["confirm-delete-attempt"], en["confirm-delete-attempt"], `<form id="[^"]*" method="post" action="[^"]*">`)

	// A shuffled exam: the screen-number sentence, and no flags at all.
	env := newTestEnv(t)
	cookie, csrf, _ := env.signIn(t)
	cookieID := langHeader(cookie, langID)
	shuffled, qs := env.resultsExam(t, 3)
	token, aid := env.startAttempt(t, shuffled, "Ayu")
	_, quiet := env.startAttempt(t, shuffled, "Budi")
	n := 0
	for i, q := range pageQuestions(t, env.examPage(t, studentCookie(token))) {
		if q.ID == qs[2].ID {
			n = i + 1
		}
	}
	if _, err := env.attempts.RecordEvent(t.Context(), token, attempt.LeftPage, qs[2].ID); err != nil {
		t.Fatal(err)
	}
	screen := tx(t, "attempt.flag_screen", n, 3)
	requireEnglishText(t, "screen sentence", screen, fmt.Sprintf("Question %d on screen (file question 3)", n))
	requireIndonesian(t, "screen", getOK(t, env, attemptPath(shuffled, aid), cookieID), screen.in(`<a href="#q3">{}</a>`))
	requireIndonesian(t, "no flags", getOK(t, env, attemptPath(shuffled, quiet), cookieID), tx(t, "attempt.no_flags").in(`<p class="empty">{}</p>`))

	// Fill-in: an automatic right mark overridden to wrong, and a mark
	// refused while the attempt is in progress.
	fill := env.openExam(t, fillInExam("score", "Lyon", "metre"))
	fqs := env.fileQuestions(t, fill)
	ayu, ayuID := env.startAttempt(t, fill, "Ayu")
	env.typeAnswer(t, ayu, fqs[0], "Paris")
	env.submitAttempt(t, ayu)
	form := func(mark string) url.Values { return url.Values{"csrf_token": {csrf}, "mark": {mark}} }
	if rec := postForm(env.h, overridePath(fill, ayuID, "1"), form("wrong"), cookieID); rec.Code != http.StatusSeeOther {
		t.Fatalf("mark wrong = %d", rec.Code)
	}
	at := env.clock.In(env.deps.Location).Format(fullTimeLayout)
	requireIndonesian(t, "marked wrong", detailSection(t, getOK(t, env, attemptPath(fill, ayuID), cookieID), 1),
		tx(t, "attempt.auto_right").in(`<p class="auto-mark" data-auto="true">{}</p>`),
		thx(t, "attempt.marked_wrong_html", testAdminEmail, at).in(`<p class="override-mark" data-override="false">{}</p>`))

	budi, budiID := env.startAttempt(t, fill, "Budi")
	env.typeAnswer(t, budi, fqs[0], "Paris")
	for _, l := range []lang{langID, langEN} {
		rec := postForm(env.h, overridePath(fill, budiID, "1"), form("right"), langHeader(cookie, l))
		want := gox(t, "attempt.override_in_progress")
		text := want.en
		if l == langID {
			text = want.id
		}
		requireAlert(t, rec, http.StatusConflict, text)
	}
}

// csvFetch downloads the fixture's CSV with extra request headers.
func csvFetch(t *testing.T, env *testEnv, c pageCase, extra http.Header) *httptest.ResponseRecorder {
	t.Helper()
	h := c.header.Clone()
	for k, vs := range extra {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	rec := do(env.h, "GET", c.path, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", c.path, rec.Code)
	}
	return rec
}

func TestCSV_Language(t *testing.T) {
	env, cases := pageCases(t)
	i := slices.IndexFunc(cases, func(c pageCase) bool { return c.name == "attempts-csv" })
	if i < 0 {
		t.Fatal("no fixture case attempts-csv")
	}
	c := cases[i]
	idCookie := http.Header{"Cookie": {langCookieName + "=id"}}
	en := csvFetch(t, env, c, nil)
	id := csvFetch(t, env, c, idCookie)
	header := csvFetch(t, env, c, http.Header{"Accept-Language": {"id"}})
	enCookie := csvFetch(t, env, c, http.Header{"Cookie": {langCookieName + "=en"}, "Accept-Language": {"id"}})

	if header.Body.String() != id.Body.String() {
		t.Errorf("Accept-Language id body differs from the id cookie body:\n%s\n%s", header.Body.String(), id.Body.String())
	}
	if enCookie.Body.String() != en.Body.String() {
		t.Errorf("en cookie with Accept-Language id is not English:\n%s", enCookie.Body.String())
	}
	for _, h := range []string{"Content-Type", "Content-Disposition"} {
		if id.Header().Get(h) != en.Header().Get(h) {
			t.Errorf("%s: id %q, en %q", h, id.Header().Get(h), en.Header().Get(h))
		}
	}

	read := func(body string) [][]string {
		records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		if err != nil {
			t.Fatalf("parse CSV: %v\n%s", err, body)
		}
		return records
	}
	idRecords, enRecords := read(id.Body.String()), read(en.Body.String())
	if wantEN := "name,status,score,total,Bagian Satu,Bagian Dua,started,finished,flags"; strings.Join(enRecords[0], ",") != wantEN {
		t.Errorf("English header = %q, want %q", enRecords[0], wantEN)
	}
	var wantHeader []string
	for _, k := range []string{"name", "status", "score", "total"} {
		wantHeader = append(wantHeader, catalogueText(t, langID, "csv.col."+k))
	}
	wantHeader = append(wantHeader, "Bagian Satu", "Bagian Dua")
	for _, k := range []string{"started", "finished", "flags"} {
		wantHeader = append(wantHeader, catalogueText(t, langID, "csv.col."+k))
	}
	if !slices.Equal(idRecords[0], wantHeader) {
		t.Errorf("Indonesian header = %q, want %q", idRecords[0], wantHeader)
	}
	for _, k := range []string{"name", "status", "score", "total", "started", "finished", "flags"} {
		word := catalogueText(t, langID, "csv.col."+k)
		if word != strings.ToLower(word) || strings.ContainsAny(word, `,"`) || strings.ContainsAny(word[:1], "=+-@") ||
			len(strings.Fields(word)) < 1 || len(strings.Fields(word)) > 2 {
			t.Errorf("csv.col.%s = %q: want one or two lower-case words without a comma, a quote or a formula start", k, word)
		}
	}

	if len(idRecords) != len(enRecords) || len(idRecords) != 4 {
		t.Fatalf("records: id %d, en %d, want 4", len(idRecords), len(enRecords))
	}
	statuses := []attempt.Status{attempt.TimedOut, attempt.Submitted, attempt.InProgress}
	for r := 1; r < len(enRecords); r++ {
		idRow, enRow := idRecords[r], enRecords[r]
		if len(idRow) != len(enRow) || len(idRow) != len(wantHeader) {
			t.Fatalf("row %d: %d and %d columns, want %d", r, len(idRow), len(enRow), len(wantHeader))
		}
		if want := catalogueText(t, langID, "csv.status."+string(statuses[r-1])); idRow[1] != want {
			t.Errorf("row %d: Indonesian status %q, want %q", r, idRow[1], want)
		}
		if want := catalogueText(t, langEN, "csv.status."+string(statuses[r-1])); enRow[1] != want {
			t.Errorf("row %d: English status %q, want %q", r, enRow[1], want)
		}
		if idRow[1] == enRow[1] {
			t.Errorf("row %d: Indonesian status is the English %q", r, enRow[1])
		}
		for col := range enRow {
			if col != 1 && idRow[col] != enRow[col] {
				t.Errorf("row %d column %d: id %q, en %q", r, col, idRow[col], enRow[col])
			}
		}
	}
}
