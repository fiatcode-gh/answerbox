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
	"github.com/fiatcode-gh/go-exam/internal/oidcfake"
	"github.com/fiatcode-gh/go-exam/internal/platform/clientip"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

// adminRoutes lists every route under /admin/ except the sign-in routes;
// each must require a session.
var adminRoutes = []struct{ Method, Path string }{
	{"POST", "/admin/logout"},
	{"GET", "/admin/"},
	{"GET", "/admin/exams"},
	{"POST", "/admin/exams"},
	{"GET", "/admin/exams/1"},
	{"POST", "/admin/exams/1/replace"},
	{"POST", "/admin/exams/1/open"},
	{"POST", "/admin/exams/1/close"},
	{"POST", "/admin/exams/1/schedule"},
	{"GET", "/admin/exams/1/attempts"},
	{"GET", "/admin/exams/1/attempts.csv"},
	{"GET", "/admin/exams/1/attempts/1"},
	{"GET", "/admin/exams/1/attempts/1/delete"},
	{"POST", "/admin/exams/1/attempts/1/delete"},
	{"POST", "/admin/exams/1/attempts/1/rename"},
	{"POST", "/admin/exams/1/attempts/1/questions/1/override"},
	{"GET", "/admin/exams/1/revisions"},
	{"GET", "/admin/exams/1/revisions/1"},
	{"GET", "/admin/exams/1/download"},
	{"POST", "/admin/exams/1/copy"},
	{"POST", "/admin/exams/1/archive"},
	{"POST", "/admin/exams/1/unarchive"},
	{"GET", "/admin/exams/1/edit"},
	{"GET", "/admin/exams/1/settings"},
	{"POST", "/admin/exams/1/settings"},
	{"POST", "/admin/exams/1/questions/1/move"},
	{"POST", "/admin/exams/1/questions/1/remove"},
	{"GET", "/admin/exams/1/questions/1"},
	{"POST", "/admin/exams/1/questions/1"},
	{"GET", "/admin/exams/1/questions/new"},
	{"POST", "/admin/exams/1/questions/new"},
	{"GET", "/admin/exams/1/questions/1/section"},
	{"POST", "/admin/exams/1/questions/1/section"},
	{"GET", "/admin/exams/1/sections/1"},
	{"POST", "/admin/exams/1/sections/1"},
	{"POST", "/admin/exams/1/sections/1/remove"},
	{"GET", "/admin/exams/new"},
	{"POST", "/admin/exams/new"},
	{"GET", "/admin/teachers"},
	{"POST", "/admin/teachers"},
	{"POST", "/admin/teachers/1/remove"},
}

const (
	testAdminEmail   = "admin@example.test"
	testTeacherEmail = "teacher@example.test"
	testAdminName    = "Ada Admin"
	testClientSecret = "test-secret"
)

var testStart = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

type envConfig struct {
	logOut      io.Writer
	trusted     []netip.Prefix
	location    *time.Location // time.UTC when nil
	adminEmails []string       // {testAdminEmail} when nil
}

type testEnv struct {
	h        http.Handler
	db       *sql.DB
	admin    *adminauth.Service
	exams    *exam.Service
	attempts *attempt.Service
	fake     *oidcfake.Provider
	oidcSrv  *httptest.Server
	clock    time.Time
	// owner is the first admin as an admin actor, used by tests that call
	// exam.Service directly.
	owner exam.Actor
	// seen collects the codes, states and sign-in cookies of the flows run
	// through env, so log tests can check none of them leaked.
	seen []string
	// deps are the collaborators h was built with.
	deps Deps
	// media is the media folder the exam service writes images into.
	media *os.Root
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
	if cfg.adminEmails == nil {
		cfg.adminEmails = []string{testAdminEmail}
	}
	media, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })

	env := &testEnv{db: db, clock: testStart, media: media}
	now := func() time.Time { return env.clock }
	env.fake = oidcfake.New(oidcfake.Config{
		ClientID:     "test-client",
		ClientSecret: testClientSecret,
		RedirectURL:  "http://app.test/admin/auth/callback",
		Now:          now,
	})
	env.oidcSrv = httptest.NewServer(env.fake)
	t.Cleanup(env.oidcSrv.Close)
	env.fake.SetIssuer(env.oidcSrv.URL)

	env.admin, err = adminauth.NewService(db, cfg.adminEmails, now, rand.NewChaCha8([32]byte{1}))
	if err != nil {
		t.Fatalf("adminauth.NewService: %v", err)
	}
	adminID, err := env.admin.EnsureAdmins(context.Background())
	if err != nil {
		t.Fatalf("EnsureAdmins: %v", err)
	}
	env.owner = exam.Actor{TeacherID: adminID, Admin: true}
	provider := adminauth.NewProvider(adminauth.ProviderConfig{
		Issuer:       env.oidcSrv.URL,
		ClientID:     "test-client",
		ClientSecret: testClientSecret,
		RedirectURL:  "http://app.test/admin/auth/callback",
		HTTPClient:   env.oidcSrv.Client(),
		Now:          now,
		Random:       rand.NewChaCha8([32]byte{4}),
	})
	env.exams = exam.NewService(db, media, now, rand.NewChaCha8([32]byte{2}))
	env.attempts = attempt.NewService(db, now, rand.NewChaCha8([32]byte{3}))
	env.deps = Deps{
		DB:        db,
		Exams:     env.exams,
		Attempts:  env.attempts,
		Admin:     env.admin,
		OIDC:      provider,
		ClientIP:  clientip.New(cfg.trusted),
		Location:  cfg.location,
		Logger:    slog.New(slog.NewTextHandler(cfg.logOut, nil)),
		PublicURL: "http://app.test",
		Now:       now,
	}
	env.h, err = NewHandler(env.deps)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return env
}

// signIn creates a session for the admin directly through the service and
// returns the cookie header for it plus its CSRF token.
func (env *testEnv) signIn(t *testing.T) (cookie http.Header, csrf string, token string) {
	t.Helper()
	return env.signInAs(t, testAdminEmail)
}

// signInAs is signIn for another email; the caller puts it on the list first.
func (env *testEnv) signInAs(t *testing.T, email string) (cookie http.Header, csrf string, token string) {
	t.Helper()
	token, err := env.admin.SignIn(context.Background(), adminauth.Identity{Email: email, EmailVerified: true, Name: testAdminName})
	if err != nil {
		t.Fatalf("SignIn(%s): %v", email, err)
	}
	sess, ok, err := env.admin.Session(context.Background(), token)
	if err != nil || !ok {
		t.Fatalf("Session: %v %v", ok, err)
	}
	return adminCookie(token), sess.CSRFToken, token
}

// startAndApprove runs GET /admin/auth/start and the provider's form for id.
// It returns the goexam_signin cookie value and the callback URL.
func (env *testEnv) startAndApprove(t *testing.T, id oidcfake.Identity) (signin string, callback *url.URL) {
	t.Helper()
	rec := do(env.h, "GET", "/admin/auth/start", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /admin/auth/start = %d:\n%s", rec.Code, rec.Body.String())
	}
	c := setCookie(t, rec, signinCookieName)
	if c == nil {
		t.Fatal("start set no goexam_signin cookie")
	}
	cb, err := oidcfake.Approve(context.Background(), env.oidcSrv.Client(), rec.Header().Get("Location"), id)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	callback, err = url.Parse(cb)
	if err != nil {
		t.Fatal(err)
	}
	q := callback.Query()
	env.seen = append(env.seen, c.Value, q.Get("code"), q.Get("state"))
	return c.Value, callback
}

// callback serves the callback URL through the handler, with the sign-in
// cookie when signin is not empty.
func (env *testEnv) callback(callback *url.URL, signin string) *httptest.ResponseRecorder {
	var header http.Header
	if signin != "" {
		header = http.Header{"Cookie": {signinCookieName + "=" + signin}}
	}
	return do(env.h, "GET", callback.RequestURI(), header)
}

// googleSignIn runs the whole sign-in for id through the handler and the
// fake provider; edit, when not nil, changes the callback URL first.
func (env *testEnv) googleSignIn(t *testing.T, id oidcfake.Identity, edit func(callback *url.URL)) *httptest.ResponseRecorder {
	t.Helper()
	signin, cb := env.startAndApprove(t, id)
	if edit != nil {
		edit(cb)
	}
	return env.callback(cb, signin)
}

func (env *testEnv) sessionValid(t *testing.T, token string) bool {
	t.Helper()
	_, ok, err := env.admin.Session(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (env *testEnv) sessionRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM admin_session`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
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

// setCookieLines returns the raw Set-Cookie lines for the cookie name.
func setCookieLines(t *testing.T, rec *httptest.ResponseRecorder, name string) []string {
	t.Helper()
	var out []string
	for _, line := range rec.Header().Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("bad Set-Cookie %q: %v", line, err)
		}
		if c.Name == name {
			out = append(out, line)
		}
	}
	return out
}

// setCookie returns the cookie the response sets under name, if any.
func setCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	lines := setCookieLines(t, rec, name)
	if len(lines) == 0 {
		return nil
	}
	c, _ := http.ParseSetCookie(lines[0])
	return c
}

// adminSetCookie returns the goexam_admin cookie the response sets, if any.
func adminSetCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	return setCookie(t, rec, "goexam_admin")
}

func requireCookieExpired(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := adminSetCookie(t, rec)
	if c == nil || c.MaxAge >= 0 || c.Path != "/admin" || c.Value != "" {
		t.Fatalf("want an expired goexam_admin cookie with Path=/admin, got %+v", c)
	}
}

func requireSigninCookieExpired(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := setCookie(t, rec, "goexam_signin")
	if c == nil || c.MaxAge >= 0 || c.Path != "/admin/auth" || c.Value != "" {
		t.Fatalf("want an expired goexam_signin cookie with Path=/admin/auth, got %+v", c)
	}
}

// requireCookieAttrs checks the raw Set-Cookie line carries every attribute.
func requireCookieAttrs(t *testing.T, line string, attrs ...string) {
	t.Helper()
	for _, attr := range attrs {
		if !strings.Contains(line, attr) {
			t.Errorf("Set-Cookie %q lacks %s", line, attr)
		}
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

func TestSignIn_AdminAndListedTeacher(t *testing.T) {
	for _, email := range []string{testAdminEmail, testTeacherEmail} {
		t.Run(email, func(t *testing.T) {
			env := newTestEnv(t)
			sqlitetest.Teacher(t, env.db, testTeacherEmail)

			rec := env.googleSignIn(t, oidcfake.Identity{Email: email, Name: testAdminName}, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("callback = %d, want 200:\n%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range []string{`<meta http-equiv="refresh" content="0;url=/admin/exams">`, `href="/admin/exams"`} {
				if !strings.Contains(body, want) {
					t.Errorf("signed-in page lacks %q:\n%s", want, body)
				}
			}

			lines := setCookieLines(t, rec, "goexam_admin")
			if len(lines) != 1 {
				t.Fatalf("goexam_admin Set-Cookie = %q, want one", lines)
			}
			requireCookieAttrs(t, lines[0], "Path=/admin", "Max-Age=43200", "HttpOnly", "Secure", "SameSite=Strict")
			c, _ := http.ParseSetCookie(lines[0])
			if c.Path != "/admin" || c.MaxAge != 43200 || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("goexam_admin cookie = %+v", c)
			}
			requireSigninCookieExpired(t, rec)

			sess, ok, err := env.admin.Session(context.Background(), c.Value)
			if err != nil || !ok || sess.Teacher.Email != email {
				t.Fatalf("cookie session: ok=%v err=%v teacher=%+v, want %s", ok, err, sess.Teacher, email)
			}
			if rec := do(env.h, "GET", "/admin/exams", adminCookie(c.Value)); rec.Code != http.StatusOK {
				t.Fatalf("GET /admin/exams with the new cookie = %d", rec.Code)
			}
			requireRedirect(t, do(env.h, "GET", "/admin/login", adminCookie(c.Value)), "/admin/exams")
		})
	}
}

type refusalCase struct {
	name string
	want string // redirect target
	// run returns the callback response; sessionsAfter is how many sessions
	// it leaves (only the replay case signs in once first).
	run           func(t *testing.T, env *testEnv) *httptest.ResponseRecorder
	sessionsAfter int
}

func refusalCases() []refusalCase {
	const notAllowed, failed = "/admin/login?error=not_allowed", "/admin/login?error=failed"
	admin := oidcfake.Identity{Email: testAdminEmail, Name: testAdminName}
	tampered := func(tamper oidcfake.Tamper) func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
		return func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			env.fake.SetTamper(tamper)
			return env.googleSignIn(t, admin, nil)
		}
	}
	return []refusalCase{
		{name: "unlisted email", want: notAllowed, run: func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			return env.googleSignIn(t, oidcfake.Identity{Email: testTeacherEmail, Name: "Tess Teacher"}, nil)
		}},
		{name: "unverified email", want: notAllowed, run: func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			return env.googleSignIn(t, oidcfake.Identity{Email: testAdminEmail, Name: testAdminName, Unverified: true}, nil)
		}},
		{name: "wrong state", want: failed, run: func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			return env.googleSignIn(t, admin, func(cb *url.URL) {
				q := cb.Query()
				q.Set("state", "someone-elses-state")
				cb.RawQuery = q.Encode()
			})
		}},
		{name: "wrong nonce", want: failed, run: tampered(oidcfake.Tamper{Nonce: "other"})},
		{name: "bad signature", want: failed, run: tampered(oidcfake.Tamper{WrongKey: true})},
		{name: "wrong audience", want: failed, run: tampered(oidcfake.Tamper{Audience: "someone-else"})},
		{name: "no sign-in cookie", want: failed, run: func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			_, cb := env.startAndApprove(t, admin)
			return env.callback(cb, "")
		}},
		{name: "provider error", want: failed, run: func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			signin, cb := env.startAndApprove(t, admin)
			cb.RawQuery = url.Values{"error": {"access_denied"}, "state": {cb.Query().Get("state")}}.Encode()
			return env.callback(cb, signin)
		}},
		{name: "replayed callback", want: failed, sessionsAfter: 1, run: func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			signin, cb := env.startAndApprove(t, admin)
			if rec := env.callback(cb, signin); rec.Code != http.StatusOK {
				t.Fatalf("first callback = %d, want 200", rec.Code)
			}
			return env.callback(cb, signin)
		}},
	}
}

func TestSignIn_Refusals(t *testing.T) {
	for _, c := range refusalCases() {
		t.Run(c.name, func(t *testing.T) {
			env := newTestEnv(t)
			rec := c.run(t, env)
			requireRedirect(t, rec, c.want)
			if adminSetCookie(t, rec) != nil {
				t.Error("refusal sets goexam_admin")
			}
			requireSigninCookieExpired(t, rec)
			if n := env.sessionRows(t); n != c.sessionsAfter {
				t.Errorf("admin_session rows = %d, want %d", n, c.sessionsAfter)
			}
		})
	}
}

func TestSignIn_StartSetsCookieAndRedirects(t *testing.T) {
	env := newTestEnv(t)
	rec := do(env.h, "GET", "/admin/auth/start", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, env.oidcSrv.URL+"/auth?") {
		t.Fatalf("Location = %q, want the provider's /auth", loc)
	}
	lines := setCookieLines(t, rec, "goexam_signin")
	if len(lines) != 1 {
		t.Fatalf("goexam_signin Set-Cookie = %q, want one", lines)
	}
	requireCookieAttrs(t, lines[0], "Path=/admin/auth", "Max-Age=600", "HttpOnly", "Secure", "SameSite=Lax")
	c, _ := http.ParseSetCookie(lines[0])
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := strings.Cut(c.Value, ".")
	if state == "" || u.Query().Get("state") != state {
		t.Errorf("state %q is not the cookie's first part (%q)", u.Query().Get("state"), c.Value)
	}
	if strings.Count(c.Value, ".") != 2 {
		t.Errorf("cookie value %q does not have three parts", c.Value)
	}
	if strings.Contains(loc, testClientSecret) || strings.Contains(rec.Body.String(), testClientSecret) {
		t.Error("start leaks the client secret")
	}
}

func TestSignIn_ProviderDown(t *testing.T) {
	env := newTestEnv(t)
	env.oidcSrv.Close()
	requireRedirect(t, do(env.h, "GET", "/admin/auth/start", nil), "/admin/login?error=failed")
	if rec := do(env.h, "GET", "/", nil); rec.Code != http.StatusOK {
		t.Fatalf("student page with the provider down = %d, want 200", rec.Code)
	}
}

func TestLogin_Page(t *testing.T) {
	env := newTestEnv(t)
	const (
		notAllowedMsg = "This Google account is not allowed. Ask your go-exam admin to add it."
		failedMsg     = "Sign-in did not complete. Try again."
	)
	for _, c := range []struct{ query, alert string }{
		{"", ""},
		{"?error=whatever", ""},
		{"?error=" + url.QueryEscape("<b>x</b>"), ""},
		{"?error=not_allowed", notAllowedMsg},
		{"?error=failed", failedMsg},
	} {
		rec := do(env.h, "GET", "/admin/login"+c.query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /admin/login%s = %d", c.query, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{`id="google-sign-in"`, `href="/admin/auth/start"`, "Sign in with Google"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: login page lacks %q:\n%s", c.query, want, body)
			}
		}
		for _, absent := range []string{"<form", `name="password"`, testClientSecret, "<b>x</b>", "&lt;b&gt;"} {
			if strings.Contains(body, absent) {
				t.Errorf("%s: login page contains %q", c.query, absent)
			}
		}
		if c.alert == "" {
			if strings.Contains(body, `role="alert"`) {
				t.Errorf("%s: login page shows an alert", c.query)
			}
			continue
		}
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, c.alert) {
			t.Errorf("%s: login page lacks alert %q:\n%s", c.query, c.alert, body)
		}
	}
}

func TestLogin_PasswordRouteGone(t *testing.T) {
	env := newTestEnv(t)
	rec := postForm(env.h, "/admin/login", url.Values{"password": {"correct-horse-battery"}}, nil)
	requireRedirect(t, rec, "/admin/login")
	if len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Errorf("POST /admin/login sets %q", rec.Header().Values("Set-Cookie"))
	}
	if n := env.sessionRows(t); n != 0 {
		t.Errorf("admin_session rows = %d, want 0", n)
	}
}

func TestSignIn_LogsNothingSecret(t *testing.T) {
	var logs bytes.Buffer
	db := sqlitetest.Open(t)
	var secrets []string

	env := newTestEnvWith(t, db, envConfig{logOut: &logs})
	rec := env.googleSignIn(t, oidcfake.Identity{Email: testAdminEmail, Name: testAdminName}, nil)
	c := adminSetCookie(t, rec)
	if rec.Code != http.StatusOK || c == nil {
		t.Fatalf("successful sign-in = %d, cookie %v", rec.Code, c)
	}
	secrets = append(secrets, c.Value)
	secrets = append(secrets, env.seen...)

	for _, rc := range refusalCases() {
		before := logs.Len()
		env := newTestEnvWith(t, sqlitetest.Open(t), envConfig{logOut: &logs})
		rec := rc.run(t, env)
		requireRedirect(t, rec, rc.want)
		if c := adminSetCookie(t, rec); c != nil {
			secrets = append(secrets, c.Value)
		}
		secrets = append(secrets, env.seen...)
		if added := logs.String()[before:]; !strings.Contains(added, "sign-in refused") || !strings.Contains(added, "reason=") {
			t.Errorf("%s: log lacks a refusal line with a reason:\n%s", rc.name, added)
		}
	}

	out := logs.String()
	for _, s := range append(secrets, testAdminEmail, testTeacherEmail, testAdminName, "Tess Teacher", testClientSecret, "eyJ") {
		if s != "" && strings.Contains(out, s) {
			t.Errorf("log contains %q:\n%s", s, out)
		}
	}
}

func TestAdminHeader_ShowsSignedInEmail(t *testing.T) {
	env := newTestEnv(t)
	sqlitetest.Teacher(t, env.db, testTeacherEmail)
	for _, email := range []string{testAdminEmail, testTeacherEmail} {
		cookie, _, _ := env.signInAs(t, email)
		body := do(env.h, "GET", "/admin/exams", cookie).Body.String()
		if !strings.Contains(body, `id="signed-in-as"`) || !strings.Contains(body, ">"+email+"<") {
			t.Errorf("%s: exams page does not show the signed-in email:\n%s", email, body)
		}
	}
}
