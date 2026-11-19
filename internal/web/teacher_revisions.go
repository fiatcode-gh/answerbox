package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/fiatcode-gh/answerbox/internal/exam"
)

// mountTeacherRevisions registers the history pages on the admin sub-mux.
func (s *server) mountTeacherRevisions(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/exams/{id}/revisions", s.revisionList)
	admin.HandleFunc("GET /admin/exams/{id}/revisions/{n}", s.revisionDetail)
}

type revisionRow struct {
	Number                  int
	Saved, Teacher, Summary string
}

type revisionListData struct {
	adminPage
	Exam             exam.Exam
	Rows             []revisionRow
	Zone             string
	NoRevisionNotice string
}

func (s *server) revisionList(w http.ResponseWriter, r *http.Request) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return
	}
	revs, err := s.deps.Exams.Revisions(r.Context(), e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := revisionListData{
		adminPage:        adminPageFor(r),
		Exam:             e,
		Rows:             make([]revisionRow, len(revs)),
		Zone:             s.deps.Location.String(),
		NoRevisionNotice: noRevisionNotice,
	}
	for i, rev := range revs {
		data.Rows[i] = revisionRow{Number: rev.Number, Saved: s.fullTime(rev.CreatedAt), Teacher: rev.TeacherEmail, Summary: rev.Summary}
	}
	s.page(w, r, http.StatusOK, "admin_revisions.html", data)
}

type revisionDetailData struct {
	adminPage
	Exam           exam.Exam
	Revision       exam.Revision
	Saved          string
	ShuffleText    string
	ShowResultText string
	Zone           string
}

func (s *server) revisionDetail(w http.ResponseWriter, r *http.Request) {
	e, ok := s.resultsExam(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	rev, err := s.deps.Exams.Revision(r.Context(), e.ID, n)
	if errors.Is(err, exam.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.page(w, r, http.StatusOK, "admin_revision.html", revisionDetailData{
		adminPage:      adminPageFor(r),
		Exam:           e,
		Revision:       rev,
		Saved:          s.fullTime(rev.CreatedAt),
		ShuffleText:    shuffleText[rev.Shuffle],
		ShowResultText: showResultText[rev.ShowResult],
		Zone:           s.deps.Location.String(),
	})
}
