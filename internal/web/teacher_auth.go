package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
	"github.com/fiatcode-gh/answerbox/internal/exam"
)

const (
	adminCookieName = "answerbox_admin"
	adminCookiePath = "/admin"

	// The sign-in cookie carries one flow's secrets from the start route to
	// the callback. It is SameSite=Lax because the callback is a cross-site
	// redirect from the provider.
	signinCookieName   = "answerbox_signin"
	signinCookiePath   = "/admin/auth"
	signinCookieMaxAge = 600

	// adminBodyLimit caps teacher-area POSTs so a 10 MiB upload plus form
	// overhead fits.
	adminBodyLimit = 11 << 20
)

type adminSessionKey struct{}

// adminPage is embedded in the data of every signed-in teacher page; the
// header reads the CSRF token for its logout form, the signed-in teacher and
// the area the page belongs to ("exams" or "teachers") from it.
type adminPage struct {
	CSRFToken string
	Me        adminauth.Teacher
	Nav       string
}

func sessionFrom(ctx context.Context) adminauth.Session {
	sess, _ := ctx.Value(adminSessionKey{}).(adminauth.Session)
	return sess
}

func adminPageFor(r *http.Request) adminPage {
	sess := sessionFrom(r.Context())
	nav := "exams"
	if strings.HasPrefix(r.URL.Path, "/admin/teachers") {
		nav = "teachers"
	}
	return adminPage{CSRFToken: sess.CSRFToken, Me: sess.Teacher, Nav: nav}
}

// actorFrom returns the signed-in teacher as the actor of exam catalog calls.
func actorFrom(r *http.Request) exam.Actor {
	sess := sessionFrom(r.Context())
	return exam.Actor{TeacherID: sess.Teacher.ID, Admin: sess.Teacher.Admin}
}

// mountAdmin registers the sign-in routes and mounts every other /admin/
// route behind requireTeacher, so no teacher route can be registered
// unprotected.
func (s *server) mountAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", s.loginPage)
	mux.HandleFunc("GET /admin/auth/start", s.signInStart)
	mux.HandleFunc("GET /admin/auth/callback", s.signInCallback)

	admin := http.NewServeMux()
	admin.HandleFunc("POST /admin/logout", s.logout)
	s.mountTeacherExams(admin)
	s.mountTeacherResults(admin)
	s.mountTeacherRevisions(admin)
	s.mountTeacherEditor(admin)
	s.mountTeacherAccounts(admin)
	admin.HandleFunc("/admin/", s.notFound)
	mux.Handle("/admin/", s.requireTeacher(admin))
}

func newAdminCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     adminCookiePath,
		MaxAge:   int(adminauth.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

func expireAdminCookie(w http.ResponseWriter) {
	c := newAdminCookie("")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

func newSigninCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     signinCookieName,
		Value:    value,
		Path:     signinCookiePath,
		MaxAge:   signinCookieMaxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func expireSigninCookie(w http.ResponseWriter) {
	c := newSigninCookie("")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// signinFlow reads the flow secrets from the request's sign-in cookie.
func signinFlow(r *http.Request) (adminauth.Flow, bool) {
	c, err := r.Cookie(signinCookieName)
	if err != nil {
		return adminauth.Flow{}, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return adminauth.Flow{}, false
	}
	for _, p := range parts {
		if !validToken(p) {
			return adminauth.Flow{}, false
		}
	}
	return adminauth.Flow{State: parts[0], Nonce: parts[1], Verifier: parts[2]}, true
}

// adminSession returns the session named by the request's admin cookie.
// sent reports whether the request carried the cookie at all.
func (s *server) adminSession(r *http.Request) (sess adminauth.Session, ok, sent bool, err error) {
	c, err := r.Cookie(adminCookieName)
	if err != nil {
		return adminauth.Session{}, false, false, nil
	}
	sess, ok, err = s.deps.Admin.Session(r.Context(), c.Value)
	return sess, ok, true, err
}

// requireTeacher admits only requests with a valid session of a signed-in
// teacher; state-changing requests must also carry the session's CSRF token
// in a body of at most adminBodyLimit bytes. Once that body is read, the
// request gets a fresh requestTimeout budget: a slow upload is bounded by the
// server's read timeout instead, and must not use up the time left for the
// database work.
func (s *server) requireTeacher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok, sent, err := s.adminSession(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			if sent {
				expireAdminCookie(w)
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		ctx := r.Context()
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, adminBodyLimit)
			if status := parseAdminForm(r); status != 0 {
				s.errorPage(w, r, status)
				return
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
			defer cancel()
			defer func() {
				if r.MultipartForm != nil {
					r.MultipartForm.RemoveAll()
				}
			}()
			given := r.PostFormValue("csrf_token")
			if subtle.ConstantTimeCompare([]byte(given), []byte(sess.CSRFToken)) != 1 {
				s.errorPageMessage(w, r, http.StatusForbidden, loc(r).T("error.csrf_expired"))
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, adminSessionKey{}, sess)))
	})
}

// parseAdminForm parses a multipart or urlencoded body; it returns 413 when
// the body exceeds the limit, 400 when it cannot be parsed, else 0.
func parseAdminForm(r *http.Request) int {
	var err error
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "multipart/form-data" {
		err = r.ParseMultipartForm(adminBodyLimit)
	} else {
		err = r.ParseForm()
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge
	case err != nil:
		return http.StatusBadRequest
	}
	return 0
}

// Sign-in outcomes shown on the login page, keyed by the ?error= word.
const (
	signInNotAllowed = "not_allowed"
	signInFailed     = "failed"
)

// signInAlert is the login page's alert for the ?error= word; an unknown
// word shows none.
func signInAlert(l localizer, word string) string {
	switch word {
	case signInNotAllowed:
		return l.T("login.not_allowed")
	case signInFailed:
		return l.T("login.failed")
	}
	return ""
}

type loginData struct {
	Alert string
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	_, ok, _, err := s.adminSession(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if ok {
		http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
		return
	}
	s.page(w, r, http.StatusOK, "admin_login.html", loginData{Alert: signInAlert(loc(r), r.URL.Query().Get("error"))})
}

// signInStart sends the browser to the provider. It is a link target, not a
// form action: the page policy's form-action 'self' would block a form
// submission that redirects to another origin.
func (s *server) signInStart(w http.ResponseWriter, r *http.Request) {
	flow, authURL, err := s.deps.OIDC.Begin(r.Context())
	if err != nil {
		reason, detail := "start", ""
		var fe *adminauth.FlowError
		if errors.As(err, &fe) {
			reason, detail = fe.Reason, fe.Detail
		}
		s.refuseSignIn(w, r, signInFailed, reason, detail)
		return
	}
	http.SetCookie(w, newSigninCookie(flow.State+"."+flow.Nonce+"."+flow.Verifier))
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// signInCallback finishes a sign-in. Success answers 200 with a page that
// moves on to /admin/exams by itself: the session cookie is SameSite=Strict,
// and a browser withholds it on a redirect chain that started at the
// provider's site.
func (s *server) signInCallback(w http.ResponseWriter, r *http.Request) {
	flow, ok := signinFlow(r)
	expireSigninCookie(w)
	if !ok {
		s.refuseSignIn(w, r, signInFailed, "no_flow", "")
		return
	}
	q := r.URL.Query()
	if q.Get("error") != "" {
		s.refuseSignIn(w, r, signInFailed, "provider_error", "")
		return
	}
	id, err := s.deps.OIDC.Complete(r.Context(), flow, q.Get("state"), q.Get("code"))
	var fe *adminauth.FlowError
	switch {
	case errors.As(err, &fe):
		s.refuseSignIn(w, r, signInFailed, fe.Reason, fe.Detail)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	token, err := s.deps.Admin.SignIn(r.Context(), id)
	switch {
	case errors.Is(err, adminauth.ErrNotAllowed):
		s.refuseSignIn(w, r, signInNotAllowed, "not_allowed", "")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	http.SetCookie(w, newAdminCookie(token))
	s.page(w, r, http.StatusOK, "admin_signed_in.html", nil)
}

// refuseSignIn logs the fixed reason (never an email, code, state, token or
// error text) and sends the browser back to the login page with outcome.
func (s *server) refuseSignIn(w http.ResponseWriter, r *http.Request, outcome, reason, detail string) {
	attrs := []any{"reason", reason}
	if detail != "" {
		attrs = append(attrs, "detail", detail)
	}
	s.log.Warn("sign-in refused", attrs...)
	http.Redirect(w, r, "/admin/login?error="+outcome, http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(adminCookieName)
	if err == nil {
		if err := s.deps.Admin.Logout(r.Context(), c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	expireAdminCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// serverError logs err and renders the 500 page.
func (s *server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	s.errorPage(w, r, http.StatusInternalServerError)
}
