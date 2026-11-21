package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPreferIndonesian(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"", false},
		{"id", true},
		{"id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7", true},
		{"en-US,en;q=0.9,id;q=0.8", false},
		{"en", false},
		{"fr-FR,fr;q=0.9", false},
		{"fr, id;q=0.5", true},
		{"id;q=0, en", false},
		{"en;q=0.5, id;q=0.5", false},
		{"id;q=0.5, en;q=0.5", true},
		{"*", false},
		{"in-ID", true},
		{"ID-id", true},
		{"id;q=abc", false},
		{"id;q=1.5", false},
		{"ms", false},
		{" id ; q=0.8 , en ; q=0.9 ", false},
		{" id ; Q = 0.9 , en ; q=0.8 ", true},
		{"idx", false},
		{"id_ID", false},
		{"en;q=0, id;q=0.1", true},
		{"id;q=nan", false},
		{",,id", true},
		{"id;q=0x1p-1", false},
		{"id;q=5e-1", false},
		{"id;q=0.5000", false},
		{"id;q=1.001", false},
		{"id;q=.5", false},
		{"id;q=+0.5", false},
		{"id;q=1.000, en;q=0.999", true},
		{"id;q=1., en;q=0.", true},
		{"en;q=0.001, id;q=0.002", true},
	} {
		if got := preferIndonesian(tc.header); got != tc.want {
			t.Errorf("preferIndonesian(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestRequestLang(t *testing.T) {
	for _, tc := range []struct {
		name, cookie, header string
		want                 lang
	}{
		{"nothing", "", "", langEN},
		{"header id", "", "id", langID},
		{"cookie en over header id", "en", "id", langEN},
		{"cookie id over header en", "id", "en", langID},
		{"invalid cookie falls back to the header", "xx", "id", langID},
		{"cookie values are exact", "ID", "", langEN},
		{"upper-case cookie falls back to the header", "ID", "id", langID},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.cookie != "" {
			req.Header.Set("Cookie", langCookieName+"="+tc.cookie)
		}
		if tc.header != "" {
			req.Header.Set("Accept-Language", tc.header)
		}
		if got := requestLang(req); got != tc.want {
			t.Errorf("%s: requestLang = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseLang(t *testing.T) {
	for _, v := range []string{"en", "id"} {
		if l, ok := parseLang(v); !ok || string(l) != v {
			t.Errorf("parseLang(%q) = %q, %v", v, l, ok)
		}
	}
	for _, v := range []string{"", "EN", "Id", "in", "id-ID", " id", "fr"} {
		if _, ok := parseLang(v); ok {
			t.Errorf("parseLang(%q) accepted", v)
		}
	}
}

func TestPages_HTMLLang(t *testing.T) {
	env, cases := pageCases(t)
	checked := 0
	for _, c := range cases {
		for _, l := range []lang{langID, langEN} {
			body := env.fetch(t, c, l)
			if !strings.HasPrefix(body, "<!doctype html>") {
				continue // the CSV download
			}
			if want := `<html lang="` + string(l) + `">`; !strings.Contains(body, want) {
				t.Errorf("%s in %s: no %s", c.name, l, want)
			}
			checked++
		}
	}
	if checked < 2*60 {
		t.Errorf("checked %d pages, want the whole fixture", checked)
	}
}

func idText(t *testing.T, key string) string {
	t.Helper()
	text, ok := lookup(langID, key)
	if !ok {
		t.Fatalf("no Indonesian text for %q", key)
	}
	return text
}

func TestErrorPage_Indonesian(t *testing.T) {
	env := newTestEnv(t)
	indonesian := http.Header{"Accept-Language": {"id"}}
	requireTexts := func(label, body string, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if want := idText(t, k); !strings.Contains(body, want) {
				t.Errorf("%s: missing %s %q:\n%s", label, k, want, body)
			}
		}
	}

	rec := do(env.h, "GET", "/tidak-ada", indonesian)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /tidak-ada = %d", rec.Code)
	}
	requireTexts("404", rec.Body.String(), "error.404.title", "error.404.message", "error.start_page")
	if strings.Contains(rec.Body.String(), idText(t, "error.teacher_pages")) {
		t.Error("404 outside /admin offers the teacher pages")
	}

	cookie, _, _ := env.signIn(t)
	header := http.Header{"Accept-Language": {"id"}, "Cookie": cookie["Cookie"]}
	rec = do(env.h, "GET", "/admin/tidak-ada", header)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /admin/tidak-ada = %d", rec.Code)
	}
	requireTexts("admin 404", rec.Body.String(), "error.404.title", "error.start_page", "error.teacher_pages")

	rec = postForm(env.h, "/admin/exams/1/open", url.Values{"csrf_token": {"salah"}}, header)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong CSRF token = %d", rec.Code)
	}
	requireTexts("csrf 403", rec.Body.String(), "error.403.title", "error.csrf_expired")
	if strings.Contains(rec.Body.String(), idText(t, "error.403.message")) {
		t.Error("csrf 403 shows the standard message instead of the expired form")
	}
}

// TestErrorPage_Texts is the table test of errorPageMessage's computed keys:
// every status the app answers with the error page shows its title and
// message in both languages, English as the page said before, and any other
// status shows the generic texts: in English its standard title with the
// 400 message as before, in Indonesian no English at all.
func TestErrorPage_Texts(t *testing.T) {
	env := newTestEnv(t)
	s, err := newServer(env.deps)
	if err != nil {
		t.Fatal(err)
	}
	render := func(status int, l lang) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Cookie", langCookieName+"="+string(l))
		rec := httptest.NewRecorder()
		s.errorPageMessage(rec, req, status, "")
		if rec.Code != status {
			t.Fatalf("status %d answered %d", status, rec.Code)
		}
		return rec.Body.String()
	}
	shows := func(body, title, message string) bool {
		return strings.Contains(body, "<h1>"+title+"</h1>") && strings.Contains(body, "<p>"+message+"</p>")
	}
	english := map[int][2]string{
		http.StatusBadRequest:            {"Bad Request", "The request could not be completed."},
		http.StatusForbidden:             {"Not allowed", "This request was refused."},
		http.StatusNotFound:              {"Page not found", "There is nothing at this address."},
		http.StatusRequestEntityTooLarge: {"Upload too large", "Upload is larger than 10 MB."},
		http.StatusInternalServerError:   {"Something went wrong", "The server could not finish this request. Please try again."},
	}
	for status, en := range english {
		if body := render(status, langEN); !shows(body, en[0], en[1]) {
			t.Errorf("en %d: want %q / %q:\n%s", status, en[0], en[1], body)
		}
		key := "error." + strconv.Itoa(status)
		if body := render(status, langID); !shows(body, idText(t, key+".title"), idText(t, key+".message")) {
			t.Errorf("id %d: want the texts of %s:\n%s", status, key, body)
		}
	}
	for _, status := range []int{http.StatusConflict, http.StatusServiceUnavailable} {
		if body := render(status, langEN); !shows(body, http.StatusText(status), "The request could not be completed.") {
			t.Errorf("en other status %d:\n%s", status, body)
		}
		body := render(status, langID)
		if english := http.StatusText(status); strings.Contains(body, english) {
			t.Errorf("id other status %d shows %q:\n%s", status, english, body)
		}
		if !shows(body, idText(t, "error.other.title"), idText(t, "error.other.message")) {
			t.Errorf("id other status %d: want the texts of error.other:\n%s", status, body)
		}
	}
}

func TestDates(t *testing.T) {
	en := localizer{langEN}
	start := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 800 {
		d := start.AddDate(0, 0, i)
		if got, want := en.Date(d), d.Format("Mon, 2 Jan 2006"); got != want {
			t.Fatalf("en Date(%s) = %q, want %q", want, got, want)
		}
		if got, want := en.Day(d), d.Format("2 Jan"); got != want {
			t.Fatalf("en Day(%s) = %q, want %q", d, got, want)
		}
	}
	id := localizer{langID}
	for _, tc := range []struct {
		got, want string
	}{
		{id.Date(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)), "Sen, 2 Nov 2026"},
		{id.Date(time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)), "Min, 9 Agu 2026"},
		{id.Date(time.Date(2026, 12, 19, 0, 0, 0, 0, time.UTC)), "Sab, 19 Des 2026"},
		{id.Day(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)), "2 Mei"},
		{id.Day(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)), "31 Okt"},
	} {
		if tc.got != tc.want {
			t.Errorf("id = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestDates_ShownInIndonesian(t *testing.T) {
	env, cases := pageCases(t)
	for _, tc := range []struct{ name, want string }{
		{"landing-code-scheduled", "Sen, 16 Nov 2026"},
		{"exam-archived", "Sab, 14 Nov 2026"},
		{"attempts", "Sab, 14 Nov 2026"},
	} {
		for _, c := range cases {
			if c.name == tc.name {
				if body := env.fetch(t, c, langID); !strings.Contains(body, tc.want) {
					t.Errorf("%s in id: no %q", c.name, tc.want)
				}
			}
		}
	}
}
