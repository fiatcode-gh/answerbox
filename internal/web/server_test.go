package web

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite/sqlitetest"
)

func newTestHandler(t *testing.T, db *sql.DB, logOut io.Writer) http.Handler {
	t.Helper()
	return newTestEnvWith(t, db, envConfig{logOut: logOut}).h
}

func do(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	db := sqlitetest.Open(t)
	h := newTestHandler(t, db, nil)

	rec := do(h, "GET", "/healthz", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz = %d %q, want 200 \"ok\\n\"", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	db.Close()
	rec = do(h, "GET", "/healthz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz after close = %d, want 503", rec.Code)
	}
}

func staticURL(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`href="(/static/` + regexp.QuoteMeta(name) + `\?v=([0-9a-f]{8}))"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page does not link %s with ?v=<8 hex>:\n%s", name, body)
	}
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:])[:8]; m[2] != want {
		t.Errorf("%s version = %s, want %s", name, m[2], want)
	}
	return m[1]
}

func TestNotFound_RendersErrorPage(t *testing.T) {
	h := newTestHandler(t, sqlitetest.Open(t), nil)
	rec := do(h, "GET", "/no/such/page", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`<html lang="en" data-theme="light">`, `name="viewport"`, `href="/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page lacks %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestHeaders_CSP(t *testing.T) {
	h := newTestHandler(t, sqlitetest.Open(t), nil)
	notFound := do(h, "GET", "/nope", nil)
	cssURL := staticURL(t, notFound.Body.String(), "app.css")

	want := map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
	}
	cases := []struct {
		name      string
		rec       *httptest.ResponseRecorder
		wantCache string
	}{
		{"healthz", do(h, "GET", "/healthz", nil), "no-store"},
		{"404", notFound, "no-store"},
		{"static", do(h, "GET", cssURL, nil), "public, max-age=31536000, immutable"},
	}
	for _, c := range cases {
		for k, v := range want {
			if got := c.rec.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got, v)
			}
		}
		if got := c.rec.Header().Values("Cache-Control"); len(got) != 1 || got[0] != c.wantCache {
			t.Errorf("%s: Cache-Control = %q, want %q", c.name, got, c.wantCache)
		}
	}
}

func TestStatic_VersionedAndCached(t *testing.T) {
	h := newTestHandler(t, sqlitetest.Open(t), nil)
	page := do(h, "GET", "/nope", nil).Body.String()

	for _, name := range []string{"vendor/pico.min.css", "app.css"} {
		u := staticURL(t, page, name)
		rec := do(h, "GET", u, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", u, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s Cache-Control = %q", u, got)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Errorf("GET %s Content-Type = %q", u, ct)
		}
		want, _ := fs.ReadFile(staticFS, "static/"+name)
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("GET %s body differs from embedded file", u)
		}
	}
	if pico := strings.Index(page, "pico.min.css"); pico < 0 || pico > strings.Index(page, "app.css") {
		t.Error("pico.min.css must be linked before app.css")
	}

	for _, u := range []string{"/static/missing.css", "/static/", "/static/vendor"} {
		if rec := do(h, "GET", u, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", u, rec.Code)
		}
	}
}

func TestAssets_NoExternalReferences(t *testing.T) {
	external := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:src|href|action)\s*=\s*["']?\s*(?:https?:|//)`),
		regexp.MustCompile(`(?i)url\(\s*["']?\s*(?:https?:|//)`),
		regexp.MustCompile(`(?i)@import`),
	}
	checked := 0
	for _, root := range []struct {
		fsys fs.FS
		dir  string
	}{{templateFS, "templates"}, {staticFS, "static"}} {
		err := fs.WalkDir(root.fsys, root.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(root.fsys, path)
			if err != nil {
				return err
			}
			checked++
			for _, re := range external {
				if loc := re.FindIndex(data); loc != nil {
					t.Errorf("%s: external reference %q", path, data[loc[0]:min(len(data), loc[1]+40)])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 4 {
		t.Fatalf("walked only %d files", checked)
	}
}

func TestCrossOrigin_PostRejected(t *testing.T) {
	h := newTestHandler(t, sqlitetest.Open(t), nil)
	rec := do(h, "POST", "/healthz", http.Header{"Sec-Fetch-Site": {"cross-site"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("403 lacks security headers: X-Frame-Options = %q", got)
	}
}

func TestRequestLog(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(t, sqlitetest.Open(t), &buf)
	page := do(h, "GET", "/nope?secret=1", http.Header{"Cookie": {"answerbox_admin=tok"}}).Body.String()
	do(h, "GET", "/healthz", nil)
	do(h, "GET", staticURL(t, page, "app.css"), nil)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one log line (healthz and static skipped), got %d:\n%s", len(lines), buf.String())
	}
	line := lines[0]
	for _, want := range []string{"method=GET", "path=/nope", "status=404", "duration=", "client_ip=192.0.2.1"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line lacks %q: %s", want, line)
		}
	}
	for _, leak := range []string{"secret", "tok"} {
		if strings.Contains(line, leak) {
			t.Errorf("log line leaks %q: %s", leak, line)
		}
	}
}

func TestRecovery_Renders500(t *testing.T) {
	var buf bytes.Buffer
	s, err := newServer(Deps{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := do(h, "GET", "/x", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("500 is not the error page: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("panic not logged: %s", buf.String())
	}
}
