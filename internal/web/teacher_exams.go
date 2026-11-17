package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// mountTeacherExams registers the exam pages on the admin sub-mux.
func (s *server) mountTeacherExams(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
	})
	admin.HandleFunc("GET /admin/exams", s.examList)
	admin.HandleFunc("POST /admin/exams", s.importExam)
	admin.HandleFunc("GET /admin/exams/{id}", s.examDetail)
	admin.HandleFunc("POST /admin/exams/{id}/replace", s.replaceExam)
	admin.HandleFunc("POST /admin/exams/{id}/open", s.setExamOpen(s.deps.Exams.Open))
	admin.HandleFunc("POST /admin/exams/{id}/close", s.setExamOpen(s.deps.Exams.Close))
	admin.HandleFunc("POST /admin/exams/{id}/schedule", s.setExamSchedule)
	admin.HandleFunc("GET /admin/exams/{id}/download", s.downloadExam)
	admin.HandleFunc("POST /admin/exams/{id}/copy", s.copyExam)
}

type examListData struct {
	adminPage
	Exams []examRow
	Alert string
	Zone  string
	// Scheduled is whether a listed exam has a schedule.
	Scheduled bool
}

// examRow is an exam of the list with its schedule times as text.
type examRow struct {
	exam.Exam
	Opens, Closes string
}

func (s *server) examList(w http.ResponseWriter, r *http.Request) {
	s.renderExamList(w, r, http.StatusOK, "")
}

func (s *server) renderExamList(w http.ResponseWriter, r *http.Request, status int, alert string) {
	exams, err := s.deps.Exams.List(r.Context(), actorFrom(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := examListData{adminPage: adminPageFor(r), Exams: make([]examRow, len(exams)), Alert: alert, Zone: s.deps.Location.String()}
	for i, e := range exams {
		data.Exams[i] = examRow{Exam: e, Opens: s.scheduleTime(e.Schedule.OpensAt), Closes: s.scheduleTime(e.Schedule.ClosesAt)}
		data.Scheduled = data.Scheduled || e.Schedule.Set()
	}
	s.page(w, r, status, "admin_exams.html", data)
}

// scheduleTime is a schedule time as text, empty when not set.
func (s *server) scheduleTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return s.fullTime(t)
}

const missingFileMessage = "Choose an exam file (.md, .txt or .zip) to upload."

// rerenderFunc renders the page an upload form lives on with an alert.
type rerenderFunc func(status int, alert string)

// readUpload reads the "file" part of the multipart form requireTeacher has
// already parsed. On failure it has responded — 422 through rerender, 413 or
// 500 — and returns ok false.
func (s *server) readUpload(w http.ResponseWriter, r *http.Request, rerender rerenderFunc) (data []byte, ok bool) {
	f, _, err := r.FormFile("file")
	if err != nil {
		rerender(http.StatusUnprocessableEntity, missingFileMessage)
		return nil, false
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, examfile.MaxUploadBytes+1))
	switch {
	case err != nil:
		s.serverError(w, r, err)
	case len(data) > examfile.MaxUploadBytes:
		s.errorPage(w, r, http.StatusRequestEntityTooLarge)
	case len(data) == 0:
		rerender(http.StatusUnprocessableEntity, missingFileMessage)
	default:
		return data, true
	}
	return nil, false
}

func (s *server) importExam(w http.ResponseWriter, r *http.Request) {
	rerender := func(status int, alert string) { s.renderExamList(w, r, status, alert) }
	data, ok := s.readUpload(w, r, rerender)
	if !ok {
		return
	}
	id, err := s.deps.Exams.Import(r.Context(), actorFrom(r), data)
	var invalid *examfile.Error
	switch {
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, "Import failed: "+invalid.Msg)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, examPath(id), http.StatusSeeOther)
	}
}

func examPath(id int64) string {
	return "/admin/exams/" + strconv.FormatInt(id, 10)
}

var shuffleText = map[examfile.Shuffle]string{
	examfile.ShuffleNone:             "No shuffling",
	examfile.ShuffleQuestions:        "Questions shuffled",
	examfile.ShuffleQuestionsOptions: "Questions and options shuffled",
}

var showResultText = map[examfile.ShowResult]string{
	examfile.ShowNone:   "Nothing",
	examfile.ShowScore:  "Score",
	examfile.ShowReview: "Score and review",
}

type examDetailData struct {
	adminPage
	Exam           exam.Exam
	ShuffleText    string
	ShowResultText string
	Questions      []exam.Question
	Alert          string
	FrozenNotice   string
	// NoRevisionNotice is shown while the exam has no revision.
	NoRevisionNotice string
	// StudentLink is the address students open to enter this exam.
	StudentLink string
	// Opens and Closes are the schedule times as text, empty when not set.
	Opens, Closes string
	// Schedule holds the values of the schedule form's inputs.
	Schedule scheduleInput
	Zone     string
}

// scheduleInput is the schedule form's two datetime-local values.
type scheduleInput struct{ Opens, Closes string }

func (s *server) examDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderExamDetail(w, r, http.StatusOK, id, "", nil)
}

// examID parses the {id} path value; ok is false when it is not a number.
func examID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

const frozenNotice = "This exam has attempts, so its questions are frozen."

const noRevisionNotice = "This exam has no revision yet: its stored text could not be turned back into exam text. It works as before, but it has no history and cannot be downloaded or copied."

// renderExamDetail renders the exam page. typed holds the schedule form
// values as the teacher entered them; nil fills the form from the exam.
func (s *server) renderExamDetail(w http.ResponseWriter, r *http.Request, status int, id int64, alert string, typed *scheduleInput) {
	e, err := s.deps.Exams.Get(r.Context(), actorFrom(r), id)
	if errors.Is(err, exam.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	questions, err := s.deps.Exams.Questions(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	input := scheduleInput{Opens: s.scheduleInputValue(e.Schedule.OpensAt), Closes: s.scheduleInputValue(e.Schedule.ClosesAt)}
	if typed != nil {
		input = *typed
	}
	s.page(w, r, status, "admin_exam.html", examDetailData{
		adminPage:        adminPageFor(r),
		Exam:             e,
		ShuffleText:      shuffleText[e.Shuffle],
		ShowResultText:   showResultText[e.ShowResult],
		Questions:        questions,
		Alert:            alert,
		FrozenNotice:     frozenNotice,
		NoRevisionNotice: noRevisionNotice,
		StudentLink:      s.deps.PublicURL + "/?code=" + e.Code,
		Opens:            s.scheduleTime(e.Schedule.OpensAt),
		Closes:           s.scheduleTime(e.Schedule.ClosesAt),
		Schedule:         input,
		Zone:             s.deps.Location.String(),
	})
}

// scheduleInputLayout is the value format of a datetime-local input.
const scheduleInputLayout = "2006-01-02T15:04"

// scheduleInputValue is t as a datetime-local value in the configured zone,
// empty when not set.
func (s *server) scheduleInputValue(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.deps.Location).Format(scheduleInputLayout)
}

// parseScheduleInput reads a datetime-local value in the configured zone.
// Empty is the zero time; ok is false when v is not a valid value.
func (s *server) parseScheduleInput(v string) (t time.Time, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, true
	}
	t, err := time.ParseInLocation(scheduleInputLayout, v, s.deps.Location)
	return t, err == nil
}

const invalidScheduleTimeMessage = "Enter a valid date and time."

var scheduleMessages = []struct {
	err error
	msg string
}{
	{exam.ErrScheduleEmpty, "Enter an open time, a close time or both. To remove the schedule, press Open or Close."},
	{exam.ErrScheduleOrder, "The close time must be after the open time."},
	{exam.ErrSchedulePast, "The close time must be in the future."},
}

// setExamSchedule gives an exam the open and close times of the schedule
// form. The rules live in exam.Service; this only reads the two inputs.
func (s *server) setExamSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	typed := scheduleInput{Opens: r.PostFormValue("opens_at"), Closes: r.PostFormValue("closes_at")}
	rerender := func(alert string) {
		s.renderExamDetail(w, r, http.StatusUnprocessableEntity, id, alert, &typed)
	}
	opens, okOpens := s.parseScheduleInput(typed.Opens)
	closes, okCloses := s.parseScheduleInput(typed.Closes)
	if !okOpens || !okCloses {
		rerender(invalidScheduleTimeMessage)
		return
	}
	err := s.deps.Exams.SetSchedule(r.Context(), actorFrom(r), id, exam.Schedule{OpensAt: opens, ClosesAt: closes})
	for _, m := range scheduleMessages {
		if errors.Is(err, m.err) {
			rerender(m.msg)
			return
		}
	}
	switch {
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, examPath(id), http.StatusSeeOther)
	}
}

func (s *server) replaceExam(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	rerender := func(status int, alert string) { s.renderExamDetail(w, r, status, id, alert, nil) }
	data, ok := s.readUpload(w, r, rerender)
	if !ok {
		return
	}
	err := s.deps.Exams.Replace(r.Context(), actorFrom(r), id, data)
	var invalid *examfile.Error
	switch {
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, "Replace failed: "+invalid.Msg)
	case errors.Is(err, exam.ErrFrozen):
		rerender(http.StatusConflict, frozenNotice)
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, examPath(id), http.StatusSeeOther)
	}
}

// downloadExam sends the exam's current revision as a file to import again.
func (s *server) downloadExam(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	f, err := s.deps.Exams.Download(r.Context(), actorFrom(r), id)
	switch {
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, exam.ErrNoRevision):
		s.renderExamDetail(w, r, http.StatusConflict, id, noRevisionNotice, nil)
	case err != nil:
		s.serverError(w, r, err)
	default:
		w.Header().Set("Content-Type", f.ContentType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
		w.Write(f.Data)
	}
}

// copyExam makes a new exam from the current revision and opens its page.
func (s *server) copyExam(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	newID, err := s.deps.Exams.Copy(r.Context(), actorFrom(r), id)
	switch {
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, exam.ErrNoRevision):
		s.renderExamDetail(w, r, http.StatusConflict, id, noRevisionNotice, nil)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, examPath(newID), http.StatusSeeOther)
	}
}

// setExamOpen returns the handler for the open or close action: it applies
// change to the exam for the signed-in teacher and returns to the list.
func (s *server) setExamOpen(change func(context.Context, exam.Actor, int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := examID(r)
		if !ok {
			s.notFound(w, r)
			return
		}
		err := change(r.Context(), actorFrom(r), id)
		switch {
		case errors.Is(err, exam.ErrNotFound):
			s.notFound(w, r)
		case err != nil:
			s.serverError(w, r, err)
		default:
			http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
		}
	}
}
