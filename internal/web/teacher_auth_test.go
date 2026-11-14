package web

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/platform/clientip"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

// adminRoutes lists every route under /admin/ except login; each must
// require a session.
var adminRoutes = []struct{ Method, Path string }{
	{"POST", "/admin/logout"},
	{"GET", "/admin/"},
	{"GET", "/admin/exams"},
	{"POST", "/admin/exams"},
	{"GET", "/admin/exams/1"},
	{"POST", "/admin/exams/1/replace"},
	{"POST", "/admin/exams/1/open"},
	{"POST", "/admin/exams/1/close"},
	{"GET", "/admin/exams/1/attempts"},
	{"GET", "/admin/exams/1/attempts.csv"},
	{"GET", "/admin/exams/1/attempts/1"},
	{"GET", "/admin/exams/1/attempts/1/delete"},
	{"POST", "/admin/exams/1/attempts/1/delete"},
}

const testPassword = "correct-horse-battery"

var testStart = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

type envConfig struct {
	logOut   io.Writer
	trusted  []netip.Prefix
	location *time.Location // time.UTC when nil
}

type testEnv struct {
	h        http.Handler
	db       *sql.DB
	admin    *adminauth.Service
	exams    *exam.Service
	attempts *attempt.Service
	clock    time.Time
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, sqlitetest.Open(t), envConfig{})
}

func newTestEnvWith(t *testing.T, db *sql.DB, cfg envConfig) *testEnv {
	t.Helper()
	if cfg.logOut == nil {
		cfg.logOut = io.Discard
	}
	if cfg.location == nil {
		cfg.location = time.UTC
	}
	media, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })

	env := &testEnv{db: db, clock: testStart}
	now := func() time.Time { return env.clock }
	env.admin = adminauth.NewService(db, testPassword, now, rand.NewChaCha8([32]byte{1}))
	env.exams = exam.NewService(db, media, now, rand.NewChaCha8([32]byte{2}))
	env.attempts = attempt.NewService(db, now, rand.NewChaCha8([32]byte{3}))
	env.h, err = NewHandler(Deps{
		DB:       db,
		Exams:    env.exams,
		Attempts: env.attempts,
		Admin:    env.admin,
		ClientIP: clientip.New(cfg.trusted),
		Location: cfg.location,
		Now:      now,
		Logger:   slog.New(slog.NewTextHandler(cfg.logOut, nil)),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return env
}

// signIn creates a session directly through the service and returns the
// cookie header for it plus its CSRF token.
func (env *testEnv) signIn(t *testing.T) (cookie http.Header, csrf string, token string) {
	t.Helper()
	token, err := env.admin.Login(context.Background(), testPassword, netip.MustParseAddr("192.0.2.200"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	sess, ok, err := env.admin.Session(context.Background(), token)
	if err != nil || !ok {
		t.Fatalf("Session: %v %v", ok, err)
	}
	return adminCookie(token), sess.CSRFToken, token
}

func (env *testEnv) sessionValid(t *testing.T, token string) bool {
	t.Helper()
	_, ok, err := env.admin.Session(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func adminCookie(token string) http.Header {
	return http.Header{"Cookie": {"goexam_admin=" + token}}
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func formRequest(method, target string, form url.Values, header http.Header) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req
}

func postForm(h http.Handler, target string, form url.Values, header http.Header) *httptest.ResponseRecorder {
	return serve(h, formRequest("POST", target, form, header))
}

func requireRedirect(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("got %d Location %q, want 303 %q; body:\n%s", rec.Code, rec.Header().Get("Location"), want, rec.Body.String())
	}
}

// adminSetCookie returns the goexam_admin cookie the response sets, if any.
func adminSetCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, line := range rec.Header().Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("bad Set-Cookie %q: %v", line, err)
		}
		if c.Name == "goexam_admin" {
			return c
		}
	}
	return nil
}

func requireCookieExpired(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := adminSetCookie(t, rec)
	if c == nil || c.MaxAge >= 0 || c.Path != "/admin" || c.Value != "" {
		t.Fatalf("want an expired goexam_admin cookie with Path=/admin, got %+v", c)
	}
}

func TestAdmin_RequiresSession(t *testing.T) {
	env := newTestEnv(t)
	_, _, expired := env.signIn(t)
	env.clock = env.clock.Add(adminauth.SessionTTL)

	routes := append(adminRoutes[:len(adminRoutes):len(adminRoutes)],
		struct{ Method, Path string }{"GET", "/admin/unknown"},
		struct{ Method, Path string }{"POST", "/admin/unknown"},
	)
	cases := []struct {
		name   string
		header http.Header
	}{
		{"no cookie", nil},
		{"garbage cookie", adminCookie("garbage")},
		{"expired session", adminCookie(expired)},
	}
	for _, c := range cases {
		for _, r := range routes {
			t.Run(c.name+" "+r.Method+" "+r.Path, func(t *testing.T) {
				rec := do(env.h, r.Method, r.Path, c.header)
				requireRedirect(t, rec, "/admin/login")
				body := rec.Body.String()
				for _, leak := range []string{"<main", "<table", "csrf_token", "Page not found"} {
					if strings.Contains(body, leak) {
						t.Errorf("redirect body leaks %q:\n%s", leak, body)
					}
				}
				if c.header != nil {
					requireCookieExpired(t, rec)
				} else if adminSetCookie(t, rec) != nil {
					t.Error("cookie set although none was sent")
				}
			})
		}
	}
}

func TestAdmin_CSRF(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, token := env.signIn(t)
	const expiredForm = "This form has expired. Go back, reload the page and try again."

	for name, form := range map[string]url.Values{
		"missing": {},
		"wrong":   {"csrf_token": {csrf + "x"}},
	} {
		rec := postForm(env.h, "/admin/logout", form, cookie)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), expiredForm) {
			t.Fatalf("%s token: %d, want 403 with %q:\n%s", name, rec.Code, expiredForm, rec.Body.String())
		}
		if !env.sessionValid(t, token) {
			t.Fatalf("%s token: session ended", name)
		}
	}

	rec := postForm(env.h, "/admin/logout", url.Values{"csrf_token": {csrf}}, cookie)
	requireRedirect(t, rec, "/admin/login")
	requireCookieExpired(t, rec)
	if env.sessionValid(t, token) {
		t.Fatal("session survives logout")
	}
}

func multipartRequest(t *testing.T, target string, fields map[string]string, fileSize int, header http.Header) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileSize > 0 {
		fw, err := mw.CreateFormFile("file", "exam.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(bytes.Repeat([]byte{'x'}, fileSize)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, vs := range header {
		req.Header[k] = vs
	}
	return req
}

func TestAdmin_MultipartCSRFAndBodyLimit(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, token := env.signIn(t)

	rec := serve(env.h, multipartRequest(t, "/admin/logout", map[string]string{"csrf_token": csrf}, 11<<20+1, cookie))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "Upload is larger than 10 MB.") {
		t.Fatalf("oversized body: %d, want 413 with message:\n%s", rec.Code, rec.Body.String())
	}
	if !env.sessionValid(t, token) {
		t.Fatal("oversized request reached the handler")
	}

	rec = serve(env.h, multipartRequest(t, "/admin/logout", map[string]string{"csrf_token": csrf}, 10<<20, cookie))
	requireRedirect(t, rec, "/admin/login")
	if env.sessionValid(t, token) {
		t.Fatal("multipart csrf_token with a 10 MiB file was not accepted")
	}
}

func TestAdmin_CrossOriginPostRejected(t *testing.T) {
	env := newTestEnv(t)
	cookie, csrf, token := env.signIn(t)
	cookie.Set("Sec-Fetch-Site", "cross-site")

	rec := postForm(env.h, "/admin/logout", url.Values{"csrf_token": {csrf}}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout = %d, want 403", rec.Code)
	}
	if !env.sessionValid(t, token) {
		t.Fatal("cross-site request ended the session")
	}
}

func TestLogin_CookieAttributes(t *testing.T) {
	env := newTestEnv(t)
	rec := postForm(env.h, "/admin/login", url.Values{"password": {testPassword}}, nil)
	requireRedirect(t, rec, "/admin/exams")

	lines := rec.Header().Values("Set-Cookie")
	if len(lines) != 1 {
		t.Fatalf("Set-Cookie = %q, want one cookie", lines)
	}
	c, err := http.ParseSetCookie(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "goexam_admin" || c.Path != "/admin" || c.MaxAge != 43200 || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("Set-Cookie = %q", lines[0])
	}
	for _, attr := range []string{"Path=/admin", "Max-Age=43200", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(lines[0], attr) {
			t.Errorf("Set-Cookie %q lacks %s", lines[0], attr)
		}
	}
	if !env.sessionValid(t, c.Value) {
		t.Error("cookie value is not a valid session")
	}

	requireRedirect(t, do(env.h, "GET", "/admin/login", adminCookie(c.Value)), "/admin/exams")
	rec = do(env.h, "GET", "/admin/exams", adminCookie(c.Value))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/exams with the login cookie = %d", rec.Code)
	}
}

func TestLogin_Page(t *testing.T) {
	env := newTestEnv(t)
	rec := do(env.h, "GET", "/admin/login", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/login = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="login-form"`, `method="post"`, `action="/admin/login"`, `name="password"`, `type="password"`, "Log in"} {
		if !strings.Contains(body, want) {
			t.Errorf("login page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `role="alert"`) {
		t.Error("login page shows an alert without a failed attempt")
	}
}

func TestLogin_WrongAndThrottled(t *testing.T) {
	env := newTestEnv(t)
	rec := postForm(env.h, "/admin/login", url.Values{"password": {"wrong"}}, nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `role="alert"`) ||
		!strings.Contains(rec.Body.String(), "Wrong password.") {
		t.Fatalf("wrong password: %d, want 401 with alert:\n%s", rec.Code, rec.Body.String())
	}
	if adminSetCookie(t, rec) != nil {
		t.Error("wrong password sets a cookie")
	}
	for range 4 {
		postForm(env.h, "/admin/login", url.Values{"password": {"wrong"}}, nil)
	}
	env.clock = env.clock.Add(500 * time.Millisecond)

	rec = postForm(env.h, "/admin/login", url.Values{"password": {testPassword}}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30 (29.5 s rounded up)", got)
	}
	if !strings.Contains(rec.Body.String(), "Too many attempts. Try again in 30 seconds.") {
		t.Errorf("429 lacks the alert:\n%s", rec.Body.String())
	}
	if adminSetCookie(t, rec) != nil {
		t.Error("throttled login sets a cookie")
	}
}

func TestLogin_ThrottleUsesForwardedIPOnlyFromTrustedProxy(t *testing.T) {
	env := newTestEnvWith(t, sqlitetest.Open(t), envConfig{trusted: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	attempt := func(remote, forwarded, password string) int {
		req := formRequest("POST", "/admin/login", url.Values{"password": {password}}, nil)
		req.RemoteAddr = remote + ":5555"
		req.Header.Set("X-Forwarded-For", forwarded)
		return serve(env.h, req).Code
	}

	for i := range 5 {
		attempt("203.0.113.7", "198.51.100."+string(rune('1'+i)), "wrong")
	}
	if code := attempt("203.0.113.7", "198.51.100.99", testPassword); code != http.StatusTooManyRequests {
		t.Fatalf("untrusted remote with a new X-Forwarded-For = %d, want 429", code)
	}

	for range 5 {
		attempt("10.0.0.1", "198.51.100.1", "wrong")
	}
	if code := attempt("10.0.0.1", "198.51.100.1", testPassword); code != http.StatusTooManyRequests {
		t.Fatalf("forwarded client after 5 failures = %d, want 429", code)
	}
	if code := attempt("10.0.0.1", "198.51.100.2", testPassword); code != http.StatusSeeOther {
		t.Fatalf("other forwarded client behind the same proxy = %d, want 303", code)
	}
}
