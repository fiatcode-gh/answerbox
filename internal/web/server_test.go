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
	"path"
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

// servedStatic is what /static/name must answer: the embedded file, and for a
// stylesheet the file with its url() references versioned.
func servedStatic(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if path.Ext(name) != ".css" {
		return data
	}
	a, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	data, err = versionCSS(data, a)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func staticURL(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`href="(/static/` + regexp.QuoteMeta(name) + `\?v=([0-9a-f]{8}))"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page does not link %s with ?v=<8 hex>:\n%s", name, body)
	}
	sum := sha256.Sum256(servedStatic(t, name))
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
	for _, want := range []string{`<html lang="en">`, `<meta name="color-scheme" content="light dark">`, `name="viewport"`, `href="/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "data-theme") {
		t.Errorf("404 page sets a data-theme:\n%s", body)
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
		"Content-Security-Policy": "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
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

	for _, a := range []struct{ name, contentType string }{{"app.css", "text/css"}, {"mark.svg", "image/svg+xml"}} {
		u := staticURL(t, page, a.name)
		rec := do(h, "GET", u, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", u, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s Cache-Control = %q", u, got)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, a.contentType) {
			t.Errorf("GET %s Content-Type = %q, want %s", u, ct, a.contentType)
		}
		if !bytes.Equal(rec.Body.Bytes(), servedStatic(t, a.name)) {
			t.Errorf("GET %s body differs from the embedded file", u)
		}
	}
	for _, rel := range []string{`rel="stylesheet"`, `rel="icon"`} {
		if n := strings.Count(page, rel); n != 1 {
			t.Errorf("page has %d %s, want 1", n, rel)
		}
	}

	for _, u := range []string{"/static/missing.css", "/static/", "/static/vendor", "/static/vendor/pico.min.css"} {
		if rec := do(h, "GET", u, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", u, rec.Code)
		}
	}
}

const (
	fontName   = "fonts/AtkinsonHyperlegibleNext-latin.woff2"
	fontSHA256 = "18b2a1a39a2fa298b0ba5390aca68462669826c90925656f1c1f6796e0e1bbaf"
)

var fontPreload = regexp.MustCompile(`<link rel="preload" href="(/static/` + regexp.QuoteMeta(fontName) + `\?v=[0-9a-f]{8})" as="font" type="font/woff2" crossorigin>`)

func TestStatic_Font(t *testing.T) {
	h := newTestHandler(t, sqlitetest.Open(t), nil)
	landing := do(h, "GET", "/", nil).Body.String()
	links := fontPreload.FindAllStringSubmatch(landing, -1)
	if len(links) != 1 || strings.Count(landing, `rel="preload"`) != 1 {
		t.Fatalf("GET / has %d font preloads (%d rel=preload), want exactly 1:\n%s", len(links), strings.Count(landing, `rel="preload"`), landing)
	}
	href := links[0][1]

	rec := do(h, "GET", href, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", href, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
		t.Errorf("GET %s Content-Type = %q, want font/woff2", href, ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("GET %s Cache-Control = %q", href, cc)
	}
	if sum := sha256.Sum256(rec.Body.Bytes()); hex.EncodeToString(sum[:]) != fontSHA256 {
		t.Errorf("GET %s body sha256 = %x, want %s", href, sum, fontSHA256)
	}

	css := do(h, "GET", staticURL(t, landing, "app.css"), nil).Body.String()
	if n := strings.Count(css, `url("`+href+`")`); n != 1 {
		t.Errorf("app.css holds url(%q) %d times, want 1", href, n)
	}
	if strings.Contains(css, `url("fonts/`) {
		t.Error(`served app.css still holds an unversioned url("fonts/`)
	}

	ofl := do(h, "GET", "/static/fonts/OFL.txt", nil)
	if ofl.Code != http.StatusOK || !strings.HasPrefix(ofl.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(ofl.Body.String(), "Open Font License") {
		t.Errorf("GET /static/fonts/OFL.txt = %d %q, want 200 text/plain with the licence", ofl.Code, ofl.Header().Get("Content-Type"))
	}

	if login := do(h, "GET", "/admin/login", nil).Body.String(); strings.Contains(login, `rel="preload"`) {
		t.Errorf("GET /admin/login preloads:\n%s", login)
	}
}

func TestStatic_VersionCSS(t *testing.T) {
	a := assets{
		"fonts/x.woff2": {version: "aaaaaaaa"},
		"img/y.svg":     {version: "bbbbbbbb"},
	}
	for _, c := range []struct{ name, in, want string }{
		{"known path", `src: url("fonts/x.woff2") format("woff2")`, `src: url("/static/fonts/x.woff2?v=aaaaaaaa") format("woff2")`},
		{"two references", `a{b:url("fonts/x.woff2")}c{d:url("img/y.svg")}`, `a{b:url("/static/fonts/x.woff2?v=aaaaaaaa")}c{d:url("/static/img/y.svg?v=bbbbbbbb")}`},
		{"data url", `--i: url("data:image/svg+xml,%3Csvg%3E%3C/svg%3E");`, `--i: url("data:image/svg+xml,%3Csvg%3E%3C/svg%3E");`},
		{"unquoted", `src: url(fonts/x.woff2)`, `src: url(fonts/x.woff2)`},
		{"single quotes", `src: url('fonts/x.woff2')`, `src: url('fonts/x.woff2')`},
	} {
		got, err := versionCSS([]byte(c.in), a)
		if err != nil || string(got) != c.want {
			t.Errorf("%s: versionCSS(%q) = %q, %v; want %q", c.name, c.in, got, err, c.want)
		}
	}
	if _, err := versionCSS([]byte(`src: url("fonts/missing.woff2")`), a); err == nil || !strings.Contains(err.Error(), "fonts/missing.woff2") {
		t.Errorf("unknown path: err = %v, want an error naming fonts/missing.woff2", err)
	}
}

func TestStatic_StylesheetVersionFollowsFont(t *testing.T) {
	css := []byte(`@font-face { src: url("fonts/x.woff2") format("woff2"); }`)
	one, err := versionCSS(css, assets{"fonts/x.woff2": {version: "11111111"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := versionCSS(css, assets{"fonts/x.woff2": {version: "22222222"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(one, two) {
		t.Errorf("versionCSS gives the same bytes for two font versions: %q", one)
	}
}

// sourceLink is the one external link the templates may hold: the landing
// page's plain navigation link to the source and self-hosting guide.
const sourceLink = `href="https://github.com/fiatcode-gh/answerbox"`

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
			if path == "templates/landing.html" {
				if n := bytes.Count(data, []byte(sourceLink)); n != 1 {
					t.Errorf("%s: %d occurrences of %s, want exactly 1", path, n, sourceLink)
				}
				data = bytes.Replace(data, []byte(sourceLink), nil, 1)
			}
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
