package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"mime"
	"net/http"
	"strconv"

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
)

const (
	adminCookieName = "goexam_admin"
	adminCookiePath = "/admin"

	// loginBodyLimit caps POST /admin/login; adminBodyLimit caps other admin
	// POSTs so a 10 MiB upload plus form overhead fits (D10).
	loginBodyLimit = 4 << 10
	adminBodyLimit = 11 << 20

	csrfExpiredMessage = "This form has expired. Go back, reload the page and try again."
)

type adminSessionKey struct{}

// adminPage is embedded in the data of every signed-in admin page; the admin
// header reads the CSRF token for its logout form from it.
type adminPage struct {
	CSRFToken string
}

func sessionFrom(ctx context.Context) adminauth.Session {
	sess, _ := ctx.Value(adminSessionKey{}).(adminauth.Session)
	return sess
}

func adminPageFor(r *http.Request) adminPage {
	return adminPage{CSRFToken: sessionFrom(r.Context()).CSRFToken}
}

// mountAdmin registers the login routes and mounts every other /admin/ route
// behind requireAdmin (D14).
func (s *server) mountAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", s.loginPage)
	mux.HandleFunc("POST /admin/login", s.login)

	admin := http.NewServeMux()
	admin.HandleFunc("POST /admin/logout", s.logout)
	s.mountTeacherExams(admin)
	s.mountTeacherResults(admin)
	admin.HandleFunc("/admin/", s.notFound)
	mux.Handle("/admin/", s.requireAdmin(admin))
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

// requireAdmin admits only requests with a valid admin session; state-changing
// requests must also carry the session's CSRF token in a body of at most
// adminBodyLimit bytes.
func (s *server) requireAdmin(next http.Handler) http.Handler {
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

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, adminBodyLimit)
			if status := parseAdminForm(r); status != 0 {
				s.errorPage(w, r, status)
				return
			}
			defer func() {
				if r.MultipartForm != nil {
					r.MultipartForm.RemoveAll()
				}
			}()
			given := r.PostFormValue("csrf_token")
			if subtle.ConstantTimeCompare([]byte(given), []byte(sess.CSRFToken)) != 1 {
				s.errorPageMessage(w, r, http.StatusForbidden, csrfExpiredMessage)
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminSessionKey{}, sess)))
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
	s.page(w, r, http.StatusOK, "admin_login.html", loginData{})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, loginBodyLimit)
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest)
		return
	}
	token, err := s.deps.Admin.Login(r.Context(), r.PostFormValue("password"), s.deps.ClientIP.ClientIP(r))
	var throttled *adminauth.ThrottledError
	switch {
	case err == nil:
		http.SetCookie(w, newAdminCookie(token))
		http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
	case errors.Is(err, adminauth.ErrWrongPassword):
		s.page(w, r, http.StatusUnauthorized, "admin_login.html", loginData{Alert: "Wrong password."})
	case errors.As(err, &throttled):
		secs := strconv.Itoa(int(math.Ceil(throttled.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", secs)
		s.page(w, r, http.StatusTooManyRequests, "admin_login.html",
			loginData{Alert: "Too many attempts. Try again in " + secs + " seconds."})
	default:
		s.serverError(w, r, err)
	}
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
