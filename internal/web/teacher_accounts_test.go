package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/oidcfake"
	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite/sqlitetest"
)

const (
	teachersLink        = `href="/admin/teachers"`
	invalidEmailAlert   = "Enter a valid email address."
	adminEmailAlert     = "Admin emails are set in ANSWERBOX_ADMIN_EMAILS and cannot be removed here."
	teachersExpiredForm = "This form has expired. Go back, reload the page and try again."
)

func removePath(id int64) string {
	return "/admin/teachers/" + strconv.FormatInt(id, 10) + "/remove"
}

// teacherRowHTML returns the row of the teachers table for id, or "".
func teacherRowHTML(body string, id int64) string {
	return regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-teacher-id="%d">.*?</tr>`, id)).FindString(body)
}

// allowedTeachers returns the emails with allowed = 1, ordered by email.
func (env *testEnv) allowedTeachers(t *testing.T) string {
	t.Helper()
	rows, err := env.db.Query(`SELECT email FROM teacher WHERE allowed = 1 ORDER BY email`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		emails = append(emails, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(emails, ",")
}

func TestTeachersPage_AdminOnly(t *testing.T) {
	env := newTestEnv(t)
	plain := env.addTeacher(t, testTeacherEmail)
	other := sqlitetest.Teacher(t, env.db, "other@example.test")
	before := env.allowedTeachers(t)

	for _, rec := range []*httptest.ResponseRecorder{
		do(env.h, "GET", "/admin/teachers", plain.cookie),
		postForm(env.h, "/admin/teachers", url.Values{"csrf_token": {plain.csrf}, "email": {"new@example.test"}}, plain.cookie),
		postForm(env.h, removePath(other), url.Values{"csrf_token": {plain.csrf}}, plain.cookie),
	} {
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Page not found") {
			t.Errorf("non-admin got %d, want the 404 page:\n%s", rec.Code, rec.Body.String())
		}
	}
	if got := env.allowedTeachers(t); got != before {
		t.Errorf("teacher list changed to %s, want %s", got, before)
	}
	if body := do(env.h, "GET", "/admin/exams", plain.cookie).Body.String(); strings.Contains(body, teachersLink) {
		t.Errorf("non-admin's exams page links to the Teachers page:\n%s", body)
	}

	admin := env.adminTeacher(t)
	rec := do(env.h, "GET", "/admin/teachers", admin.cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET /admin/teachers = %d:\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), teachersLink) {
		t.Errorf("admin's header lacks the Teachers link:\n%s", rec.Body.String())
	}
}

func TestTeachersPage_List(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)
	id := sqlitetest.Teacher(t, env.db, testTeacherEmail)

	body := do(env.h, "GET", "/admin/teachers", admin.cookie).Body.String()
	for _, col := range []string{"Email", "Name", "Last sign-in", "Exams", "Actions"} {
		if !strings.Contains(body, `<th scope="col">`+col+`</th>`) {
			t.Errorf("teachers table lacks column %s", col)
		}
	}
	adminRow := teacherRowHTML(body, admin.actor.TeacherID)
	if !strings.Contains(adminRow, `<span class="badge badge-muted">Admin</span>`) || strings.Contains(adminRow, "/remove") {
		t.Errorf("admin row lacks the badge or has a remove form:\n%s", adminRow)
	}
	row := teacherRowHTML(body, id)
	for _, want := range []string{"<td>—</td>", "<td>Never</td>", `<td class="numeric">0</td>`, `href="` + removePath(id) + `"`} {
		if !strings.Contains(row, want) {
			t.Errorf("new teacher's row lacks %q:\n%s", want, row)
		}
	}

	env.clock = env.clock.Add(90 * time.Minute)
	if _, err := env.admin.SignIn(context.Background(), adminauth.Identity{Email: testTeacherEmail, EmailVerified: true, Name: "Tess Teacher"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.exams.Import(context.Background(), exam.Actor{TeacherID: id}, []byte("# T\n\n1. Q\n- a *\n- b\n")); err != nil {
		t.Fatal(err)
	}
	row = teacherRowHTML(do(env.h, "GET", "/admin/teachers", admin.cookie).Body.String(), id)
	for _, want := range []string{"<td>Tess Teacher</td>", "<td>" + env.clock.Format(fullTimeLayout) + "</td>", `<td class="numeric">1</td>`} {
		if !strings.Contains(row, want) {
			t.Errorf("signed-in teacher's row lacks %q:\n%s", want, row)
		}
	}
}

func TestTeachers_AddAndSignIn(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)

	rec := postForm(env.h, "/admin/teachers", url.Values{"csrf_token": {admin.csrf}, "email": {"New@Example.Test"}}, admin.cookie)
	requireRedirect(t, rec, "/admin/teachers")
	body := do(env.h, "GET", "/admin/teachers", admin.cookie).Body.String()
	if !strings.Contains(body, "<td>new@example.test") {
		t.Errorf("teachers page lacks new@example.test:\n%s", body)
	}
	rec = env.googleSignIn(t, oidcfake.Identity{Email: "new@example.test", Name: "Nina New"}, nil)
	if rec.Code != http.StatusOK || adminSetCookie(t, rec) == nil {
		t.Fatalf("sign-in of the added teacher = %d, cookie %v", rec.Code, adminSetCookie(t, rec))
	}

	before := env.allowedTeachers(t)
	rec = postForm(env.h, "/admin/teachers", url.Values{"csrf_token": {admin.csrf}, "email": {"nope"}}, admin.cookie)
	requireAlert(t, rec, http.StatusUnprocessableEntity, invalidEmailAlert)
	if !strings.Contains(rec.Body.String(), `value="nope"`) {
		t.Errorf("the typed email is not kept:\n%s", rec.Body.String())
	}
	if got := env.allowedTeachers(t); got != before {
		t.Errorf("teacher list changed to %s", got)
	}
}

func TestTeachers_RemoveSignsOutAtOnce(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)
	tess := env.addTeacher(t, testTeacherEmail)
	examID, _ := env.ownedExam(t, tess.actor, "Tess quiz", false)

	rec := postForm(env.h, removePath(tess.actor.TeacherID), url.Values{"csrf_token": {admin.csrf}}, admin.cookie)
	requireRedirect(t, rec, "/admin/teachers")

	rec = do(env.h, "GET", "/admin/exams", tess.cookie)
	requireRedirect(t, rec, "/admin/login")
	requireCookieExpired(t, rec)
	requireRedirect(t, env.googleSignIn(t, oidcfake.Identity{Email: testTeacherEmail, Name: "Tess"}, nil), "/admin/login?error=not_allowed")

	body := do(env.h, "GET", "/admin/exams", admin.cookie).Body.String()
	row := regexp.MustCompile(fmt.Sprintf(`(?s)<tr data-exam-id="%d">.*?</tr>`, examID)).FindString(body)
	if !strings.Contains(row, "Tess quiz") || !strings.Contains(row, `<td class="owner">`+testTeacherEmail+`</td>`) {
		t.Errorf("admin's exams list lacks the removed teacher's exam with its owner:\n%s", body)
	}
	if strings.Contains(do(env.h, "GET", "/admin/teachers", admin.cookie).Body.String(), testTeacherEmail) {
		t.Error("the removed teacher is still on the Teachers page")
	}
}

func TestTeachers_RemoveAdminRefused(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)

	rec := postForm(env.h, removePath(admin.actor.TeacherID), url.Values{"csrf_token": {admin.csrf}}, admin.cookie)
	requireAlert(t, rec, http.StatusConflict, adminEmailAlert)
	if rec := do(env.h, "GET", "/admin/exams", admin.cookie); rec.Code != http.StatusOK {
		t.Fatalf("admin's session after the refusal: GET /admin/exams = %d", rec.Code)
	}
	if got := env.allowedTeachers(t); got != testAdminEmail {
		t.Errorf("teacher list = %s, want only the admin", got)
	}

	for _, path := range []string{"/admin/teachers/abc/remove", removePath(9999)} {
		if rec := postForm(env.h, path, url.Values{"csrf_token": {admin.csrf}}, admin.cookie); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestTeachers_RequireCSRF(t *testing.T) {
	env := newTestEnv(t)
	admin := env.adminTeacher(t)
	tessID := sqlitetest.Teacher(t, env.db, testTeacherEmail)
	before := env.allowedTeachers(t)

	for name, token := range map[string][]string{"missing": nil, "wrong": {admin.csrf + "x"}} {
		for _, c := range []struct {
			path string
			form url.Values
		}{
			{"/admin/teachers", url.Values{"email": {"new@example.test"}}},
			{removePath(tessID), url.Values{}},
		} {
			if token != nil {
				c.form.Set("csrf_token", token[0])
			}
			rec := postForm(env.h, c.path, c.form, admin.cookie)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), html.EscapeString(teachersExpiredForm)) {
				t.Errorf("%s token, POST %s = %d, want 403 with the expired-form message", name, c.path, rec.Code)
			}
		}
	}

	cross := admin.cookie.Clone()
	cross.Set("Sec-Fetch-Site", "cross-site")
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/admin/teachers", url.Values{"csrf_token": {admin.csrf}, "email": {"new@example.test"}}},
		{removePath(tessID), url.Values{"csrf_token": {admin.csrf}}},
	} {
		if rec := postForm(env.h, c.path, c.form, cross); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s = %d, want 403", c.path, rec.Code)
		}
	}
	if got := env.allowedTeachers(t); got != before {
		t.Errorf("teacher list changed to %s, want %s", got, before)
	}
}

func TestSession_DemotedAdminLosesTeachersPage(t *testing.T) {
	const demoted = "b@example.test"
	db := sqlitetest.Open(t)
	first := newTestEnvWith(t, db, envConfig{adminEmails: []string{testAdminEmail, demoted}})
	cookie, _, _ := first.signInAs(t, demoted)
	if rec := do(first.h, "GET", "/admin/teachers", cookie); rec.Code != http.StatusOK {
		t.Fatalf("as admin: GET /admin/teachers = %d", rec.Code)
	}

	second := newTestEnvWith(t, db, envConfig{adminEmails: []string{testAdminEmail}})
	rec := do(second.h, "GET", "/admin/exams", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("demoted: GET /admin/exams = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), teachersLink) {
		t.Errorf("demoted admin's header still links to the Teachers page")
	}
	if rec := do(second.h, "GET", "/admin/teachers", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("demoted: GET /admin/teachers = %d, want 404", rec.Code)
	}
}
