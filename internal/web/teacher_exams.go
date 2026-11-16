package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

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
}

type examListData struct {
	adminPage
	Exams []exam.Exam
	Alert string
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
	s.page(w, r, status, "admin_exams.html", examListData{adminPage: adminPageFor(r), Exams: exams, Alert: alert})
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
}

func (s *server) examDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderExamDetail(w, r, http.StatusOK, id, "")
}

// examID parses the {id} path value; ok is false when it is not a number.
func examID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

const frozenNotice = "This exam has attempts, so its questions are frozen."

func (s *server) renderExamDetail(w http.ResponseWriter, r *http.Request, status int, id int64, alert string) {
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
	s.page(w, r, status, "admin_exam.html", examDetailData{
		adminPage:      adminPageFor(r),
		Exam:           e,
		ShuffleText:    shuffleText[e.Shuffle],
		ShowResultText: showResultText[e.ShowResult],
		Questions:      questions,
		Alert:          alert,
		FrozenNotice:   frozenNotice,
	})
}

func (s *server) replaceExam(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	rerender := func(status int, alert string) { s.renderExamDetail(w, r, status, id, alert) }
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

// otherOpenMessage refuses opening an exam while another teacher's exam is
// open. It names no exam: the other exam is none of the actor's business.
const otherOpenMessage = "Another teacher's exam is open right now. Only one exam can be open at a time, so yours stays closed until theirs is closed."

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
		case errors.Is(err, exam.ErrOtherTeacherOpen):
			s.renderExamList(w, r, http.StatusConflict, otherOpenMessage)
		case err != nil:
			s.serverError(w, r, err)
		default:
			http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
		}
	}
}
