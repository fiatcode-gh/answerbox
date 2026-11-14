package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
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
	want := []string{"Algebra &amp; Co", "Closed", "2", "0"}
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
