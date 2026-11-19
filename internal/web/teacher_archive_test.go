package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// press posts a one-press exam action (open, close, archive, unarchive, …)
// as who with its CSRF token.
func (env *testEnv) press(id int64, action string, who teacher) *httptest.ResponseRecorder {
	return postForm(env.h, examPath(id)+"/"+action, url.Values{"csrf_token": {who.csrf}}, who.cookie)
}

// getExam reads the exam through the service as the admin.
func (env *testEnv) getExam(t *testing.T, id int64) exam.Exam {
	t.Helper()
	e, err := env.exams.Get(context.Background(), env.owner, id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return e
}

// requireArchivedClosed checks the stored exam is archived, closed and
// without a schedule.
func (env *testEnv) requireArchivedClosed(t *testing.T, where string, id int64) {
	t.Helper()
	e := env.getExam(t, id)
	if !e.Archived() || e.Open || e.Schedule.Set() {
		t.Errorf("%s: exam archived=%v open=%v schedule=%+v, want archived, closed, unscheduled", where, e.Archived(), e.Open, e.Schedule)
	}
}

// listBody is the exams list at query as who.
func (env *testEnv) listBody(t *testing.T, query string, who teacher) string {
	t.Helper()
	rec := do(env.h, "GET", "/admin/exams"+query, who.cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/exams%s = %d", query, rec.Code)
	}
	return rec.Body.String()
}

// teacherExamPage is the exam page as who.
func (env *testEnv) teacherExamPage(t *testing.T, id int64, who teacher) string {
	t.Helper()
	rec := do(env.h, "GET", examPath(id), who.cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", examPath(id), rec.Code)
	}
	return rec.Body.String()
}

// listRow returns the exam's row of a list body, "" when absent.
func listRow(body string, id int64) string {
	return regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-exam-id="%d">.*?</tr>`, id)).FindString(body)
}

// statusCell is the text of the status cell (the fourth, with the owner
// column of an admin) of a row.
func statusCell(row string, admin bool) string {
	cells := cellRe.FindAllStringSubmatch(row, -1)
	i := 2
	if admin {
		i = 3
	}
	if len(cells) <= i {
		return ""
	}
	return strings.TrimSpace(tagRe.ReplaceAllString(cells[i][1], ""))
}

// examHead is the .exam-head block of the exam page.
func examHead(body string) string {
	return regexp.MustCompile(`(?s)<div class="exam-head">.*?</div>`).FindString(body)
}

func TestArchive_Flow(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	id, _ := env.ownedExam(t, env.owner, "Algebra", true)
	if err := env.exams.Open(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}
	token, _ := env.startAttempt(t, id, "Budi")
	env.submitAttempt(t, token)
	before := env.list(t, id)
	code := env.getExam(t, id).Code

	requireRedirect(t, env.press(id, "archive", who), examPath(id))

	body := env.teacherExamPage(t, id, who)
	head := examHead(body)
	if !strings.Contains(head, `<span class="badge badge-muted">Archived</span>`) ||
		strings.Contains(head, ">Open</span>") || strings.Contains(head, ">Closed</span>") {
		t.Errorf("exam head = %q, want the Archived badge only", head)
	}
	if !strings.Contains(body, `id="archived-note"`) {
		t.Errorf("archived note missing:\n%s", body)
	}
	for _, action := range []string{"open", "close", "schedule", "archive"} {
		if strings.Contains(body, fmt.Sprintf(`action="/admin/exams/%d/%s"`, id, action)) {
			t.Errorf("archived exam page has the %s form", action)
		}
	}
	if !regexp.MustCompile(fmt.Sprintf(`<form id="unarchive-form" method="post" action="/admin/exams/%d/unarchive">`, id)).MatchString(body) {
		t.Errorf("unarchive form missing:\n%s", body)
	}
	for _, want := range []string{
		fmt.Sprintf(`href="/admin/exams/%d/attempts"`, id),
		fmt.Sprintf(`href="/admin/exams/%d/revisions"`, id),
		fmt.Sprintf(`href="/admin/exams/%d/download"`, id),
		`id="copy-form"`,
		`<p id="exam-code" class="exam-code">` + code + `</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("archived exam page lacks %q", want)
		}
	}

	if row := listRow(env.listBody(t, "", who), id); row != "" {
		t.Errorf("default list shows the archived exam: %s", row)
	}
	all := env.listBody(t, "?archived=1", who)
	row := listRow(all, id)
	if row == "" {
		t.Fatalf("?archived=1 lacks the exam:\n%s", all)
	}
	if got := statusCell(row, true); got != "Archived" {
		t.Errorf("status cell = %q, want Archived", got)
	}
	requireForms(t, "archived row", row, id, false, false)
	for _, want := range []string{fmt.Sprintf(`href="/admin/exams/%d"`, id), fmt.Sprintf(`href="/admin/exams/%d/attempts"`, id)} {
		if !strings.Contains(row, want) {
			t.Errorf("archived row lacks %q", want)
		}
	}
	if !strings.Contains(all, `<a id="hide-archived" href="/admin/exams">Hide archived</a>`) {
		t.Errorf("hide-archived link missing:\n%s", all)
	}

	env.requireArchivedClosed(t, "after archive", id)
	if after := env.list(t, id); !reflect.DeepEqual(before, after) {
		t.Errorf("attempts changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestArchive_ListEmptyStates(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	requireNoFilter := func(label, body string) {
		t.Helper()
		for _, absent := range []string{`id="show-archived"`, `id="hide-archived"`, `id="all-archived"`} {
			if strings.Contains(body, absent) {
				t.Errorf("%s: has %s", label, absent)
			}
		}
	}

	body := env.listBody(t, "", who)
	if !strings.Contains(body, "No exams yet. Import one above.") {
		t.Errorf("no exams: empty text missing")
	}
	requireNoFilter("no exams", body)

	first, _ := env.ownedExam(t, env.owner, "First", false)
	body = env.listBody(t, "", who)
	if !strings.Contains(body, `<table id="exams"`) {
		t.Errorf("one exam: table missing")
	}
	requireNoFilter("one exam", body)

	requireRedirect(t, env.press(first, "archive", who), examPath(first))
	body = env.listBody(t, "", who)
	if strings.Contains(body, `<table id="exams"`) || strings.Contains(body, "No exams yet") {
		t.Errorf("all archived: table or no-exams text shown:\n%s", body)
	}
	if !regexp.MustCompile(`(?s)<p id="all-archived" class="empty">All exams are archived\..*?href="/admin/exams\?archived=1".*?</p>`).MatchString(body) {
		t.Errorf("all archived: notice with link missing:\n%s", body)
	}

	body = env.listBody(t, "?archived=1", who)
	if ids := examRowIDs(body); !slices.Equal(ids, []string{strconv.FormatInt(first, 10)}) {
		t.Errorf("?archived=1 rows = %v, want [%d]", ids, first)
	}

	second, _ := env.ownedExam(t, env.owner, "Second", false)
	for _, query := range []string{"", "?archived=0", "?archived=yes"} {
		body = env.listBody(t, query, who)
		if ids := examRowIDs(body); !slices.Equal(ids, []string{strconv.FormatInt(second, 10)}) {
			t.Errorf("%q rows = %v, want [%d]", query, ids, second)
		}
		if !strings.Contains(body, `<a id="show-archived" href="/admin/exams?archived=1">Show archived</a>`) {
			t.Errorf("%q: show-archived link missing:\n%s", query, body)
		}
		if strings.Contains(body, `id="hide-archived"`) || strings.Contains(body, `id="all-archived"`) {
			t.Errorf("%q: hide-archived or all-archived shown", query)
		}
	}
}

func TestArchive_OpenAndScheduleRefused409(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	id, _ := env.ownedExam(t, env.owner, "Algebra", false)
	if err := env.exams.Archive(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}
	schedule := func(opens, closes string) *httptest.ResponseRecorder {
		return postForm(env.h, examPath(id)+"/schedule", url.Values{"csrf_token": {who.csrf}, "opens_at": {opens}, "closes_at": {closes}}, who.cookie)
	}
	for _, c := range []struct {
		label string
		rec   func() *httptest.ResponseRecorder
	}{
		{"open", func() *httptest.ResponseRecorder { return env.press(id, "open", who) }},
		{"schedule", func() *httptest.ResponseRecorder { return schedule("2026-11-20T08:00", "") }},
		{"empty schedule", func() *httptest.ResponseRecorder { return schedule("", "") }},
		{"past close", func() *httptest.ResponseRecorder { return schedule("", "2026-11-01T08:00") }},
	} {
		requireAlert(t, c.rec(), http.StatusConflict, archivedNotice)
		env.requireArchivedClosed(t, c.label, id)
	}

	requireRedirect(t, env.press(id, "close", who), "/admin/exams")
	env.requireArchivedClosed(t, "close", id)
}

func TestUnarchive_Flow(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	id, _ := env.ownedExam(t, env.owner, "Algebra", false)
	requireRedirect(t, env.press(id, "archive", who), examPath(id))

	requireRedirect(t, env.press(id, "unarchive", who), examPath(id))
	body := env.teacherExamPage(t, id, who)
	if !strings.Contains(examHead(body), ">Closed</span>") {
		t.Errorf("exam head = %q, want Closed", examHead(body))
	}
	requireForms(t, "unarchived page", body, id, true, false)
	for _, want := range []string{`id="schedule-form"`, fmt.Sprintf(`<form id="archive-form" method="post" action="/admin/exams/%d/archive">`, id)} {
		if !strings.Contains(body, want) {
			t.Errorf("unarchived page lacks %q", want)
		}
	}
	for _, absent := range []string{`id="archived-note"`, `id="unarchive-form"`} {
		if strings.Contains(body, absent) {
			t.Errorf("unarchived page has %q", absent)
		}
	}
	row := listRow(env.listBody(t, "", who), id)
	if got := statusCell(row, true); got != "Closed" {
		t.Errorf("default list status = %q, want Closed (row %q)", got, row)
	}
	requireRedirect(t, env.press(id, "open", who), "/admin/exams")
	if !env.getExam(t, id).Open {
		t.Error("exam not open after Open")
	}

	requireRedirect(t, env.press(id, "unarchive", who), examPath(id))
	if e := env.getExam(t, id); e.Archived() || !e.Open {
		t.Errorf("unarchive of an unarchived exam changed it: %+v", e)
	}

	requireRedirect(t, env.press(id, "archive", who), examPath(id))
	first := env.getExam(t, id).ArchivedAt
	env.clock = env.clock.Add(time.Hour)
	who = env.adminTeacher(t)
	requireRedirect(t, env.press(id, "archive", who), examPath(id))
	if again := env.getExam(t, id).ArchivedAt; !again.Equal(first) {
		t.Errorf("second archive moved the date %v → %v", first, again)
	}
}

func TestArchive_RequiresCSRF(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	id, _ := env.ownedExam(t, env.owner, "Algebra", false)
	refused := func(action string, archived bool) {
		t.Helper()
		for _, form := range []url.Values{{}, {"csrf_token": {"wrong"}}} {
			rec := postForm(env.h, examPath(id)+"/"+action, form, who.cookie)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s with token %q = %d, want 403", action, form.Get("csrf_token"), rec.Code)
			}
			if got := env.getExam(t, id).Archived(); got != archived {
				t.Errorf("%s without a valid token: archived = %v, want %v", action, got, archived)
			}
		}
	}
	refused("archive", false)
	if err := env.exams.Archive(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}
	refused("unarchive", true)
}

// TestArchive_ShowArchivedOnlyFromURL pins that "Show archived" is read from
// the URL query only. A GET body is never parsed as a form, so the request
// that could carry the value another way is a refused import (POST
// /admin/exams without a file), which renders the exams list from its
// posted form.
func TestArchive_ShowArchivedOnlyFromURL(t *testing.T) {
	env := newTestEnv(t)
	who := env.adminTeacher(t)
	archived, _ := env.ownedExam(t, env.owner, "Archived", false)
	requireRedirect(t, env.press(archived, "archive", who), examPath(archived))
	shown, _ := env.ownedExam(t, env.owner, "Shown", false)
	on := url.Values{"csrf_token": {who.csrf}, "archived": {"1"}}
	off := url.Values{"csrf_token": {who.csrf}}

	for _, c := range []struct {
		method, target string
		form           url.Values
		status         int
		withArchived   bool
	}{
		{"GET", "/admin/exams", on, http.StatusOK, false},
		{"GET", "/admin/exams?archived=1", off, http.StatusOK, true},
		{"POST", "/admin/exams", on, http.StatusUnprocessableEntity, false},
		{"POST", "/admin/exams?archived=1", off, http.StatusUnprocessableEntity, true},
	} {
		rec := serve(env.h, formRequest(c.method, c.target, c.form, who.cookie))
		body := rec.Body.String()
		if rec.Code != c.status || listRow(body, shown) == "" {
			t.Fatalf("%s %s with body %q = %d, want %d listing exam %d:\n%s", c.method, c.target, c.form.Encode(), rec.Code, c.status, shown, body)
		}
		if got := listRow(body, archived) != ""; got != c.withArchived {
			t.Errorf("%s %s with body %q lists the archived exam = %v, want %v", c.method, c.target, c.form.Encode(), got, c.withArchived)
		}
	}
}

func TestArchive_ForEveryoneInScope(t *testing.T) {
	env := newTestEnv(t)
	a := env.addTeacher(t, "a@example.test")
	b := env.addTeacher(t, "b@example.test")
	admin := env.adminTeacher(t)
	id, _ := env.ownedExam(t, a.actor, "Algebra", false)

	requireRedirect(t, env.press(id, "archive", admin), examPath(id))
	body := env.listBody(t, "", a)
	if listRow(body, id) != "" || !strings.Contains(body, `id="all-archived"`) {
		t.Errorf("A's default list after the admin archived:\n%s", body)
	}
	if listRow(env.listBody(t, "?archived=1", a), id) == "" {
		t.Error("A's ?archived=1 lacks the exam")
	}
	if listRow(env.listBody(t, "", admin), id) != "" {
		t.Error("admin's default list shows the archived exam")
	}

	for _, action := range []string{"archive", "unarchive"} {
		if rec := env.press(id, action, b); rec.Code != http.StatusNotFound {
			t.Errorf("B %s on A's exam = %d, want 404", action, rec.Code)
		}
	}
	env.requireArchivedClosed(t, "after B", id)
	body = env.listBody(t, "", b)
	if !strings.Contains(body, "No exams yet.") || strings.Contains(body, `id="all-archived"`) || strings.Contains(body, `id="show-archived"`) {
		t.Errorf("B's default list:\n%s", body)
	}
	if ids := examRowIDs(env.listBody(t, "?archived=1", b)); len(ids) != 0 {
		t.Errorf("B's ?archived=1 rows = %v, want none", ids)
	}
}

func TestArchived_EveryOtherRouteWorks(t *testing.T) {
	env := newTestEnv(t)
	owner := env.addTeacher(t, "a@example.test")
	for _, route := range examRoutes(t) {
		id, aid := env.routeExam(t, route, owner.actor)
		if err := env.exams.Archive(context.Background(), owner.actor, id); err != nil {
			t.Fatal(err)
		}
		rec := env.call(t, route, id, aid, owner)
		want := http.StatusOK
		switch {
		case strings.HasSuffix(route.Path, "/open") || strings.HasSuffix(route.Path, "/schedule"):
			want = http.StatusConflict
		case route.Method == "POST":
			want = http.StatusSeeOther
		}
		if rec.Code != want {
			t.Errorf("%s %s on an archived exam = %d, want %d:\n%s", route.Method, route.Path, rec.Code, want, rec.Body.String())
			continue
		}
		if strings.HasSuffix(route.Path, "/copy") {
			copyID, err := strconv.ParseInt(strings.TrimPrefix(rec.Header().Get("Location"), "/admin/exams/"), 10, 64)
			if err != nil {
				t.Fatalf("copy Location %q: %v", rec.Header().Get("Location"), err)
			}
			if copyID == id || env.getExam(t, copyID).Archived() {
				t.Errorf("copy %d of an archived exam is archived", copyID)
			}
		}
	}
}

func TestArchivedDate_InConfiguredZone(t *testing.T) {
	env := newJakartaEnv(t)
	env.clock = time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	who := env.adminTeacher(t)
	id, _ := env.ownedExam(t, env.owner, "Algebra", false)
	requireRedirect(t, env.press(id, "archive", who), examPath(id))
	body := env.teacherExamPage(t, id, who)
	want := `<p id="archived-note" class="frozen-notice">Archived on Sun, 15 Nov 2026. It stays closed and out of the exams list until you unarchive it.</p>`
	if !strings.Contains(body, want) {
		t.Errorf("archived note missing %q:\n%s", want, body)
	}
}

func TestStart_ArchivedCodeLooksUnknown(t *testing.T) {
	env := newTestEnv(t)
	id := env.openExam(t, simpleExam("score"))
	code := env.examCode(t, id)
	if err := env.exams.Archive(context.Background(), env.owner, id); err != nil {
		t.Fatal(err)
	}

	before := env.attemptRows(t)
	rec := postForm(env.h, "/start", url.Values{"code": {code}, "name": {"Ada"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /start = %d, want 422", rec.Code)
	}
	requireNoOpenExamAlert(t, "start", rec.Body.String())
	if attemptSetCookie(t, rec) != nil {
		t.Error("start set a cookie")
	}
	if got := env.attemptRows(t); got != before {
		t.Errorf("attempt rows %d → %d", before, got)
	}

	archived := do(env.h, "GET", "/?code="+code, nil)
	unknown := do(env.h, "GET", "/?code="+otherCode(code), nil)
	if archived.Code != http.StatusOK || archived.Body.String() != unknown.Body.String() {
		t.Errorf("archived code landing (%d) differs from an unknown code's:\n%s\n---\n%s", archived.Code, archived.Body.String(), unknown.Body.String())
	}
	if strings.Contains(archived.Body.String(), "Algebra") {
		t.Error("landing shows the archived exam's title")
	}
}
