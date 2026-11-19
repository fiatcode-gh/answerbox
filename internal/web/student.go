package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

const (
	attemptCookieName = "answerbox_attempt"

	// attemptCookieMaxAge is 7 days, long enough to resume an exam on the
	// same device after any interruption.
	attemptCookieMaxAge = 7 * 24 * 60 * 60

	// tokenLength is the base64url length of a 32-byte token without padding.
	tokenLength = 43

	studentBodyLimit = 4 << 10

	invalidNameAlert = "Enter your name (1 to 100 characters)."
	noOpenExamAlert  = "No open exam has that code. Check it with your teacher."
	badCodeAlert     = "That code is not valid."
)

// mountStudent registers the student routes. Identity is the attempt cookie
// only; no student route or form carries an attempt id.
func (s *server) mountStudent(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.landing)
	mux.HandleFunc("POST /start", s.start)
	mux.HandleFunc("POST /resume", s.resume)
	mux.HandleFunc("POST /exam/submit", s.submit)
	mux.HandleFunc("GET /exam", s.examPage)
	mux.HandleFunc("POST /exam/answer", s.answer)
	mux.HandleFunc("GET /exam/status", s.status)
	mux.HandleFunc("POST /exam/event", s.event)
}

// attemptToken returns the request's attempt token. A cookie that is not 43
// base64url characters counts as absent; sent reports whether the cookie was
// present at all, so a malformed one can be cleared.
func attemptToken(r *http.Request) (token string, ok, sent bool) {
	c, err := r.Cookie(attemptCookieName)
	if err != nil {
		return "", false, false
	}
	if !validToken(c.Value) {
		return "", false, true
	}
	return c.Value, true, true
}

func validToken(v string) bool {
	if len(v) != tokenLength {
		return false
	}
	for i := range len(v) {
		c := v[i]
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func newAttemptCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     attemptCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   attemptCookieMaxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearAttemptCookie(w http.ResponseWriter) {
	c := newAttemptCookie("")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// cookieState is what the request's attempt cookie names.
type cookieState int

const (
	cookieNone cookieState = iota
	cookieInProgress
	cookieFinished
)

// cookieAttempt resolves the request's attempt cookie; a malformed or unknown
// cookie is cleared and reported as none.
func (s *server) cookieAttempt(w http.ResponseWriter, r *http.Request) (cookieState, error) {
	token, ok, sent := attemptToken(r)
	if !ok {
		if sent {
			clearAttemptCookie(w)
		}
		return cookieNone, nil
	}
	v, err := s.deps.Attempts.View(r.Context(), token)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		clearAttemptCookie(w)
		return cookieNone, nil
	case err != nil:
		return cookieNone, err
	case v.Status == attempt.InProgress:
		return cookieInProgress, nil
	default:
		return cookieFinished, nil
	}
}

type landingData struct {
	ExamTitle  string
	Code       string
	Name       string
	Alert      string
	ResumeOpen bool
	LastResult bool
	Minutes    int // the exam's length; 0 when the page shows no exam
}

func (s *server) landing(w http.ResponseWriter, r *http.Request) {
	state, err := s.cookieAttempt(w, r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if state == cookieInProgress {
		http.Redirect(w, r, "/exam", http.StatusSeeOther)
		return
	}
	d := landingData{LastResult: state == cookieFinished}
	if raw := r.URL.Query().Get("code"); raw != "" {
		e, found, err := s.deps.Exams.EntryByCode(r.Context(), raw)
		switch {
		case err != nil:
			s.serverError(w, r, err)
			return
		case found:
			d.ExamTitle, d.Code, d.Minutes = e.Title, e.Code, e.DurationMinutes
			if e.Availability == exam.NotYetOpen {
				d.Alert = s.opensAtAlert(e.OpensAt)
			}
		default:
			d.Alert = noOpenExamAlert
		}
	}
	s.page(w, r, http.StatusOK, "landing.html", d)
}

// parseStudentForm parses a urlencoded body of at most studentBodyLimit
// bytes; it reports false after rendering the error page.
func (s *server) parseStudentForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, studentBodyLimit)
	err := r.ParseForm()
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		s.errorPage(w, r, http.StatusRequestEntityTooLarge)
		return false
	case err != nil:
		s.errorPage(w, r, http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	if !s.parseStudentForm(w, r) {
		return
	}
	state, err := s.cookieAttempt(w, r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if state == cookieInProgress {
		http.Redirect(w, r, "/exam", http.StatusSeeOther)
		return
	}
	code, name := r.PostFormValue("code"), r.PostFormValue("name")
	token, err := s.deps.Attempts.Start(r.Context(), code, name)
	refused := func(d landingData) {
		d.Code, d.Name, d.LastResult = code, name, state == cookieFinished
		s.page(w, r, http.StatusUnprocessableEntity, "landing.html", d)
	}
	var early *attempt.NotYetOpenError
	switch {
	case err == nil:
		http.SetCookie(w, newAttemptCookie(token))
		http.Redirect(w, r, "/exam", http.StatusSeeOther)
	case errors.Is(err, attempt.ErrInvalidName):
		refused(landingData{Alert: invalidNameAlert})
	case errors.Is(err, attempt.ErrNoOpenExam):
		refused(landingData{Alert: noOpenExamAlert})
	case errors.As(err, &early):
		refused(landingData{
			ExamTitle: early.Entry.Title,
			Minutes:   early.Entry.DurationMinutes,
			Alert:     s.opensAtAlert(early.Entry.OpensAt),
		})
	default:
		s.serverError(w, r, err)
	}
}

// opensAtAlert tells a student when an exam opens, in Deps.Location: the
// clock alone when that is today there, else the clock and the date.
func (s *server) opensAtAlert(opensAt time.Time) string {
	local := opensAt.In(s.deps.Location)
	if sameDay(opensAt, s.deps.Now(), s.deps.Location) {
		return "This exam opens at " + local.Format(clockLayout) + "."
	}
	return "This exam opens at " + local.Format(clockLayout) + " on " + local.Format(dateLayout) + "."
}

func (s *server) resume(w http.ResponseWriter, r *http.Request) {
	if !s.parseStudentForm(w, r) {
		return
	}
	token, err := s.deps.Attempts.Redeem(r.Context(), r.PostFormValue("code"))
	switch {
	case err == nil:
		http.SetCookie(w, newAttemptCookie(token))
		http.Redirect(w, r, "/exam", http.StatusSeeOther)
	case errors.Is(err, attempt.ErrBadCode):
		s.page(w, r, http.StatusUnprocessableEntity, "landing.html", landingData{Alert: badCodeAlert, ResumeOpen: true})
	default:
		s.serverError(w, r, err)
	}
}

// submit finishes the cookie's attempt. The request body is ignored: answers
// are written only by POST /exam/answer, so a submit cannot carry stale
// answers.
func (s *server) submit(w http.ResponseWriter, r *http.Request) {
	token, ok, sent := attemptToken(r)
	if !ok {
		if sent {
			clearAttemptCookie(w)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	err := s.deps.Attempts.Submit(r.Context(), token)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		clearAttemptCookie(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/exam", http.StatusSeeOther)
	}
}

// examQuestion is a question of the exam page with its saved state.
type examQuestion struct {
	attempt.ViewQuestion
	Answered bool
}

// examPageData is derived only from attempt.View, whose types carry no
// answer key, so the page cannot leak which option is correct.
type examPageData struct {
	ExamTitle, StudentName string
	RemainingMS            int64
	Timer                  string // MM:SS, seconds rounded up
	Level                  string // normal | warn | danger
	Questions              []examQuestion
	Answered, Total        int
	Monitor                bool
}

// Timer levels: the countdown shows "warn" from 5 minutes left and "danger"
// from 1 minute left.
const (
	warnAt   = 5 * time.Minute
	dangerAt = time.Minute
)

func newExamPageData(v attempt.View) examPageData {
	secs := (v.Remaining + time.Second - 1) / time.Second
	d := examPageData{
		ExamTitle:   v.ExamTitle,
		StudentName: v.StudentName,
		Monitor:     v.Monitor,
		RemainingMS: v.Remaining.Milliseconds(),
		Timer:       fmt.Sprintf("%02d:%02d", secs/60, secs%60),
		Level:       "normal",
		Total:       len(v.Questions),
	}
	switch {
	case v.Remaining <= dangerAt:
		d.Level = "danger"
	case v.Remaining <= warnAt:
		d.Level = "warn"
	}
	for _, q := range v.Questions {
		eq := examQuestion{ViewQuestion: q, Answered: q.Typed != ""}
		for _, o := range q.Options {
			eq.Answered = eq.Answered || o.Picked
		}
		if eq.Answered {
			d.Answered++
		}
		d.Questions = append(d.Questions, eq)
	}
	return d
}

// resultPageData is derived only from attempt.View and its Result, which hold
// the answer key only when the exam shows a review.
type resultPageData struct {
	ExamTitle, StudentName string
	Status                 attempt.Status
	TimedOut               bool
	Show                   examfile.ShowResult
	ShowScore              bool
	Score, Total           int
	Review                 []attempt.ReviewQuestion
}

func newResultPageData(v attempt.View) resultPageData {
	return resultPageData{
		ExamTitle:   v.ExamTitle,
		StudentName: v.StudentName,
		Status:      v.Status,
		TimedOut:    v.Status == attempt.TimedOut,
		Show:        v.Result.ShowResult,
		ShowScore:   v.Result.ShowResult != examfile.ShowNone,
		Score:       v.Result.Score,
		Total:       v.Result.Total,
		Review:      v.Result.Review,
	}
}

// examPage shows the attempt while in progress and its result once finished.
func (s *server) examPage(w http.ResponseWriter, r *http.Request) {
	token, ok, sent := attemptToken(r)
	if !ok {
		if sent {
			clearAttemptCookie(w)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	v, err := s.deps.Attempts.View(r.Context(), token)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		clearAttemptCookie(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case err != nil:
		s.serverError(w, r, err)
	case v.Status == attempt.InProgress:
		s.page(w, r, http.StatusOK, "exam.html", newExamPageData(v))
	default:
		s.page(w, r, http.StatusOK, "result.html", newResultPageData(v))
	}
}

type answerResponse struct {
	Saved       bool  `json:"saved"`
	RemainingMS int64 `json:"remaining_ms"`
}

type statusResponse struct {
	Status      string `json:"status"`
	RemainingMS int64  `json:"remaining_ms"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (s *server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

// studentJSONForm returns the request's attempt token after parsing its
// urlencoded body of at most studentBodyLimit bytes. On failure it has
// written the JSON error (401 no_attempt, 422 invalid) and reports false.
func (s *server) studentJSONForm(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, ok, _ := attemptToken(r)
	if !ok {
		s.writeJSON(w, r, http.StatusUnauthorized, errorResponse{"no_attempt"})
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, studentBodyLimit)
	if err := r.ParseForm(); err != nil {
		s.writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{"invalid"})
		return "", false
	}
	return token, true
}

// answer saves one answer: question plus exactly one of option (multiple
// choice) or text (fill-in).
func (s *server) answer(w http.ResponseWriter, r *http.Request) {
	token, ok := s.studentJSONForm(w, r)
	if !ok {
		return
	}
	questionID, qErr := strconv.ParseInt(r.PostFormValue("question"), 10, 64)
	hasOption, hasText := r.PostForm.Has("option"), r.PostForm.Has("text")
	if qErr != nil || hasOption == hasText {
		s.writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{"invalid"})
		return
	}
	var remaining time.Duration
	var err error
	if hasText {
		remaining, err = s.deps.Attempts.SaveText(r.Context(), token, questionID, r.PostFormValue("text"))
	} else {
		optionID, oErr := strconv.ParseInt(r.PostFormValue("option"), 10, 64)
		if oErr != nil {
			s.writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{"invalid"})
			return
		}
		remaining, err = s.deps.Attempts.SaveAnswer(r.Context(), token, questionID, optionID)
	}
	switch {
	case err == nil:
		s.writeJSON(w, r, http.StatusOK, answerResponse{Saved: true, RemainingMS: remaining.Milliseconds()})
	case errors.Is(err, attempt.ErrNotFound):
		s.writeJSON(w, r, http.StatusUnauthorized, errorResponse{"no_attempt"})
	case errors.Is(err, attempt.ErrFinished):
		s.writeJSON(w, r, http.StatusConflict, errorResponse{"finished"})
	case errors.Is(err, attempt.ErrInvalidChoice), errors.Is(err, attempt.ErrInvalidAnswer):
		s.writeJSON(w, r, http.StatusUnprocessableEntity, errorResponse{"invalid"})
	default:
		s.serverError(w, r, err)
	}
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	token, ok, _ := attemptToken(r)
	if !ok {
		s.writeJSON(w, r, http.StatusUnauthorized, errorResponse{"no_attempt"})
		return
	}
	v, err := s.deps.Attempts.View(r.Context(), token)
	switch {
	case errors.Is(err, attempt.ErrNotFound):
		s.writeJSON(w, r, http.StatusUnauthorized, errorResponse{"no_attempt"})
	case err != nil:
		s.serverError(w, r, err)
	case v.Status == attempt.InProgress:
		s.writeJSON(w, r, http.StatusOK, statusResponse{Status: "in_progress", RemainingMS: v.Remaining.Milliseconds()})
	default:
		s.writeJSON(w, r, http.StatusOK, statusResponse{Status: "finished"})
	}
}
