// Package web is the HTTP delivery layer: routes, handlers, templates and
// static assets.
package web

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/platform/clientip"
)

// Deps are the collaborators of the HTTP layer.
type Deps struct {
	DB       *sql.DB
	Exams    *exam.Service
	Attempts *attempt.Service
	Admin    *adminauth.Service
	OIDC     *adminauth.Provider
	ClientIP clientip.Resolver
	Location *time.Location
	Logger   *slog.Logger
	// PublicURL is the app's public address, scheme and host without a
	// trailing slash, used for the student link.
	PublicURL string
	// Now is the clock the pages read "today" from.
	Now func() time.Time
}

type server struct {
	deps   Deps
	log    *slog.Logger
	assets assets
	pages  *renderer
}

// NewHandler parses templates, indexes static assets and returns the
// complete application handler with its middleware.
func NewHandler(d Deps) (http.Handler, error) {
	if d.DB == nil || d.Exams == nil || d.Attempts == nil || d.Admin == nil || d.OIDC == nil || d.Location == nil || d.Logger == nil || d.PublicURL == "" || d.Now == nil {
		return nil, errors.New("web: Deps.DB, Exams, Attempts, Admin, OIDC, Location, Logger, PublicURL and Now are required")
	}
	s, err := newServer(d)
	if err != nil {
		return nil, err
	}
	return s.handler(), nil
}

func newServer(d Deps) (*server, error) {
	a, err := loadAssets()
	if err != nil {
		return nil, err
	}
	pages, err := newRenderer(a)
	if err != nil {
		return nil, err
	}
	return &server{deps: d, log: d.Logger, assets: a, pages: pages}, nil
}

// handler wires the routes inside the middleware chain, outermost first:
// recovery, request log, response headers, deadline, cross-origin protection.
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /static/{path...}", s.serveStatic)
	mux.HandleFunc("GET /media/{key}/{name}", s.serveMedia)
	s.mountAdmin(mux)
	s.mountStudent(mux)
	mux.HandleFunc("POST /lang", s.setLanguage)
	mux.HandleFunc("/", s.notFound)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.errorPage(w, r, http.StatusForbidden)
	}))

	var h http.Handler = mux
	h = cop.Handler(h)
	h = withDeadline(h)
	h = securityHeaders(h)
	h = s.logRequests(h)
	h = s.recoverer(h)
	return h
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	var one int
	if err := s.deps.DB.QueryRowContext(r.Context(), "SELECT 1").Scan(&one); err != nil {
		s.log.Error("healthz: database unavailable", "err", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	s.errorPage(w, r, http.StatusNotFound)
}
