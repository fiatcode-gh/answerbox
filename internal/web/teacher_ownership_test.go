package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

// examRoute is a route of adminRoutes that acts on one exam.
type examRoute struct{ Method, Path string }

// examRoutes returns every adminRoutes entry under /admin/exams/1. It fails
// when fewer than today's thirty-two remain, so a route cannot drop out
// silently.
func examRoutes(t *testing.T) []examRoute {
	t.Helper()
	var out []examRoute
	for _, r := range adminRoutes {
		if strings.HasPrefix(r.Path, "/admin/exams/1") {
			out = append(out, examRoute(r))
		}
	}
	if len(out) < 32 {
		t.Fatalf("adminRoutes has %d exam routes, want at least 32", len(out))
	}
	return out
}

// teacher is a signed-in teacher of a test: actor, cookie and CSRF token.
type teacher struct {
	actor  exam.Actor
	cookie http.Header
	csrf   string
}

// addTeacher lists email and signs it in.
func (env *testEnv) addTeacher(t *testing.T, email string) teacher {
	t.Helper()
	id := sqlitetest.Teacher(t, env.db, email)
	cookie, csrf, _ := env.signInAs(t, email)
	return teacher{actor: exam.Actor{TeacherID: id}, cookie: cookie, csrf: csrf}
}

// adminTeacher signs the configured admin in.
func (env *testEnv) adminTeacher(t *testing.T) teacher {
	t.Helper()
	cookie, csrf, _ := env.signIn(t)
	return teacher{actor: env.owner, cookie: cookie, csrf: csrf}
}

// ownedExam imports a closed exam of two questions for owner; with an
// attempt, it opens the exam, starts one attempt by "Ayu" and closes it
// again. It returns the exam and attempt ids (0 without an attempt).
func (env *testEnv) ownedExam(t *testing.T, owner exam.Actor, title string, withAttempt bool) (examID, attemptID int64) {
	t.Helper()
	ctx := context.Background()
	id, err := env.exams.Import(ctx, owner, []byte("# "+title+"\n\n1. Q one\n- a *\n- b\n\n2. Q two\n- c *\n- d\n"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !withAttempt {
		return id, 0
	}
	if err := env.exams.Open(ctx, owner, id); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, aid := env.startAttempt(t, id, "Ayu")
	if err := env.exams.Close(ctx, owner, id); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return id, aid
}

// call sends route for the exam and attempt as who, with the fields a POST
// needs to get past validation.
func (env *testEnv) call(t *testing.T, route examRoute, examID, attemptID int64, who teacher) *httptest.ResponseRecorder {
	t.Helper()
	rest := strings.TrimPrefix(route.Path, "/admin/exams/1")
	if after, ok := strings.CutPrefix(rest, "/attempts/1"); ok {
		rest = "/attempts/" + strconv.FormatInt(attemptID, 10) + after
	}
	target := examPath(examID) + rest
	switch {
	case route.Method == "GET":
		return do(env.h, "GET", target, who.cookie)
	case strings.HasSuffix(target, "/replace"):
		return serve(env.h, uploadRequest(t, target, who.csrf, "exam.md", []byte("# Replaced\n\n1. Q\n- a *\n- b\n"), who.cookie))
	default:
		return postForm(env.h, target, url.Values{
			"csrf_token": {who.csrf}, "name": {"Renamed"}, "opens_at": {"2026-11-20T08:00"},
			"revision": {"1"}, "to": {"2"}, "title": {"Edited"}, "shuffle": {"questions"},
			"show_result": {"score"}, "monitor": {"on"}, "duration": {"45"},
			"do": {"save"}, "text": {"Edited question"}, "option": {"a", "b"}, "correct": {"0"}, "mark": {"right"},
		}, who.cookie)
	}
}

func TestOwnership_ForeignExamIs404OnEveryRoute(t *testing.T) {
	env := newTestEnv(t)
	a := env.addTeacher(t, "a@example.test")
	b := env.addTeacher(t, "b@example.test")
	id, aid := env.ownedExam(t, a.actor, "Algebra", true)

	for _, route := range examRoutes(t) {
		if rec := env.call(t, route, id, aid, b); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as another teacher = %d, want 404", route.Method, route.Path, rec.Code)
		}
	}

	e, err := env.exams.Get(context.Background(), a.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "Algebra" || e.Open || e.Schedule.Set() || e.Archived() {
		t.Errorf("A's exam changed: %+v", e)
	}
	rows := env.list(t, id)
	if len(rows) != 1 || rows[0].ID != aid || rows[0].StudentName != "Ayu" {
		t.Errorf("A's attempts changed: %+v", rows)
	}
}

func TestOwnership_OwnerAndAdminCanUseEveryRoute(t *testing.T) {
	env := newTestEnv(t)
	owner := env.addTeacher(t, "a@example.test")
	admin := env.adminTeacher(t)

	for _, who := range []struct {
		name string
		t    teacher
	}{{"owner", owner}, {"admin", admin}} {
		for _, route := range examRoutes(t) {
			id, aid := env.routeExam(t, route, owner.actor)
			rec := env.call(t, route, id, aid, who.t)
			want := http.StatusOK
			if route.Method == "POST" {
				want = http.StatusSeeOther
			}
			if rec.Code != want {
				t.Errorf("%s: %s %s = %d, want %d:\n%s", who.name, route.Method, route.Path, rec.Code, want, rec.Body.String())
			}
			// Leave no exam open for the next route.
			if err := env.exams.Close(context.Background(), owner.actor, id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// routeExam imports the closed exam route runs on for owner: one section
// over two questions for a section route, a fill-in exam with one submitted
// attempt that typed an answer to question 1 for the override route,
// otherwise ownedExam's exam, with an attempt unless route is Replace or an
// editor route, which refuse one.
func (env *testEnv) routeExam(t *testing.T, route examRoute, owner exam.Actor) (examID, attemptID int64) {
	t.Helper()
	if strings.HasSuffix(route.Path, "/override") {
		return env.typedAttempt(t, owner)
	}
	if !strings.Contains(route.Path, "/sections/") {
		noAttempt := strings.HasSuffix(route.Path, "/replace") || editorRoute(route.Path)
		return env.ownedExam(t, owner, "Algebra", !noAttempt)
	}
	id, err := env.exams.Import(context.Background(), owner, []byte("# Parts\n\n## Part\n\n1. Q one\n- a *\n- b\n\n2. Q two\n- c *\n- d\n"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return id, 0
}

// typedAttempt imports a closed exam of a fill-in question 1 and a
// multiple-choice question 2 for owner with one submitted attempt by "Ayu"
// that typed an answer to question 1.
func (env *testEnv) typedAttempt(t *testing.T, owner exam.Actor) (examID, attemptID int64) {
	t.Helper()
	ctx := context.Background()
	id, err := env.exams.Import(ctx, owner, []byte("# Typed\n\n1. Capital of France?\n= Paris\n\n2. Q two\n- c *\n- d\n"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := env.exams.Open(ctx, owner, id); err != nil {
		t.Fatalf("Open: %v", err)
	}
	token, aid := env.startAttempt(t, id, "Ayu")
	qs, err := env.exams.Questions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	env.typeAnswer(t, token, qs[0], "Lyon")
	env.submitAttempt(t, token)
	if err := env.exams.Close(ctx, owner, id); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return id, aid
}

// editorRoute reports whether path is a route of the exam editor.
func editorRoute(path string) bool {
	return strings.Contains(path, "/edit") || strings.Contains(path, "/settings") || strings.Contains(path, "/questions/") || strings.Contains(path, "/sections/")
}

// examRowIDs returns the exam ids of the rows of the exams table.
func examRowIDs(body string) []string {
	var ids []string
	for _, m := range regexp.MustCompile(`<tr data-exam-id="(\d+)">`).FindAllStringSubmatch(body, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func TestExamList_ScopedAndOwnerColumn(t *testing.T) {
	env := newTestEnv(t)
	a := env.addTeacher(t, "a@example.test")
	b := env.addTeacher(t, "b@example.test")
	admin := env.adminTeacher(t)
	aID, _ := env.ownedExam(t, a.actor, "Alpha", false)
	bID, _ := env.ownedExam(t, b.actor, "Beta", false)
	aCode, bCode := env.examCode(t, aID), env.examCode(t, bID)
	const ownerHeader = `<th scope="col">Owner</th>`

	body := do(env.h, "GET", "/admin/exams", b.cookie).Body.String()
	if got := examRowIDs(body); strings.Join(got, ",") != strconv.FormatInt(bID, 10) {
		t.Errorf("B's exam rows = %v, want only %d", got, bID)
	}
	if strings.Contains(body, ownerHeader) || strings.Contains(body, "Alpha") {
		t.Errorf("B's list shows the Owner column or A's exam:\n%s", body)
	}
	if !strings.Contains(body, `<td class="code">`+bCode+`</td>`) || strings.Contains(body, aCode) {
		t.Errorf("B's list lacks B's code %s or shows A's code %s:\n%s", bCode, aCode, body)
	}

	body = do(env.h, "GET", "/admin/exams", admin.cookie).Body.String()
	if got := examRowIDs(body); strings.Join(got, ",") != fmt.Sprintf("%d,%d", bID, aID) {
		t.Errorf("admin's exam rows = %v, want %d and %d", got, bID, aID)
	}
	if !strings.Contains(body, ownerHeader) {
		t.Errorf("admin's list lacks the Owner column:\n%s", body)
	}
	for _, code := range []string{aCode, bCode} {
		if !strings.Contains(body, `<td class="code">`+code+`</td>`) {
			t.Errorf("admin's list lacks code %s:\n%s", code, body)
		}
	}
	for id, email := range map[int64]string{aID: "a@example.test", bID: "b@example.test"} {
		row := regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-exam-id="%d">.*?</tr>`, id)).FindString(body)
		if !strings.Contains(row, `<td class="owner">`+email+`</td>`) {
			t.Errorf("row of exam %d lacks owner %s:\n%s", id, email, row)
		}
	}
}

func TestImport_OwnerIsTheImporter(t *testing.T) {
	env := newTestEnv(t)
	b := env.addTeacher(t, "b@example.test")
	rec := serve(env.h, uploadRequest(t, "/admin/exams", b.csrf, "exam.md", []byte("# Beta\n\n1. Q\n- a *\n- b\n"), b.cookie))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import = %d:\n%s", rec.Code, rec.Body.String())
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(rec.Header().Get("Location"), "/admin/exams/"), 10, 64)
	if err != nil {
		t.Fatalf("Location %q: %v", rec.Header().Get("Location"), err)
	}
	e, err := env.exams.Get(context.Background(), env.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.OwnerID != b.actor.TeacherID || e.OwnerEmail != "b@example.test" {
		t.Errorf("owner = %d %q, want B (%d)", e.OwnerID, e.OwnerEmail, b.actor.TeacherID)
	}
}

func TestOpen_TeachersExamsOpenAtTheSameTime(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	a := env.addTeacher(t, "a@example.test")
	b := env.addTeacher(t, "b@example.test")
	admin := env.adminTeacher(t)
	aID, _ := env.ownedExam(t, a.actor, "Geometry", false)
	bID, _ := env.ownedExam(t, b.actor, "Beta", false)
	if err := env.exams.Open(ctx, a.actor, aID); err != nil {
		t.Fatal(err)
	}
	post := func(who teacher, id int64, action string) *httptest.ResponseRecorder {
		rec := postForm(env.h, examPath(id)+"/"+action, url.Values{"csrf_token": {who.csrf}}, who.cookie)
		if strings.Contains(rec.Body.String(), "Another teacher") {
			t.Errorf("%s of exam %d mentions another teacher:\n%s", action, id, rec.Body.String())
		}
		return rec
	}
	wantOpen := func(aOpen, bOpen bool) {
		t.Helper()
		for id, want := range map[int64]bool{aID: aOpen, bID: bOpen} {
			e, err := env.exams.Get(ctx, env.owner, id)
			if err != nil {
				t.Fatal(err)
			}
			if e.Open != want {
				t.Errorf("exam %d open = %v, want %v", id, e.Open, want)
			}
		}
	}

	requireRedirect(t, post(b, bID, "open"), "/admin/exams")
	wantOpen(true, true)

	requireRedirect(t, post(admin, aID, "close"), "/admin/exams")
	wantOpen(false, true)
	requireRedirect(t, post(admin, bID, "close"), "/admin/exams")
	wantOpen(false, false)
	requireRedirect(t, post(admin, aID, "open"), "/admin/exams")
	requireRedirect(t, post(admin, bID, "open"), "/admin/exams")
	wantOpen(true, true)

	requireRedirect(t, post(a, aID, "close"), "/admin/exams")
	wantOpen(false, true)
	if rec := post(b, aID, "open"); rec.Code != http.StatusNotFound {
		t.Errorf("B opening A's exam = %d, want 404", rec.Code)
	}
	wantOpen(false, true)
}
