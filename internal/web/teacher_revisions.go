package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	l := loc(r)
	data := revisionListData{
		adminPage:        adminPageFor(r),
		Exam:             e,
		Rows:             make([]revisionRow, len(revs)),
		Zone:             s.deps.Location.String(),
		NoRevisionNotice: l.T("examdetail.no_revision"),
	}
	for i, rev := range revs {
		data.Rows[i] = revisionRow{Number: rev.Number, Saved: s.fullTime(rev.CreatedAt), Teacher: rev.TeacherEmail, Summary: l.summary(rev.Summary)}
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
	Summary        string
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
	l := loc(r)
	s.page(w, r, http.StatusOK, "admin_revision.html", revisionDetailData{
		adminPage:      adminPageFor(r),
		Exam:           e,
		Revision:       rev,
		Saved:          s.fullTime(rev.CreatedAt),
		ShuffleText:    l.shuffle(rev.Shuffle),
		ShowResultText: l.showResult(rev.ShowResult),
		Summary:        l.summary(rev.Summary),
		Zone:           s.deps.Location.String(),
	})
}

// summary is a stored revision summary in l's language. English and a
// summary the app does not write are shown as stored; stored summaries stay
// English whatever the teacher's language.
func (l localizer) summary(stored string) string {
	if l.lang == langEN {
		return stored
	}
	clauses, ok := exam.ParseSummary(stored)
	if !ok {
		return stored
	}
	parts := make([]string, len(clauses))
	for i, c := range clauses {
		var args []any
		switch c.Kind {
		case exam.SummaryCopied:
			args = []any{c.Title, c.Code, c.N}
		case exam.SummaryQuestionEdited, exam.SummaryQuestionAdded, exam.SummaryQuestionRemoved,
			exam.SummarySectionEdited, exam.SummarySectionRemoved:
			args = []any{c.N}
		case exam.SummaryQuestionMoved, exam.SummaryQuestionSection, exam.SummarySectionStarted:
			args = []any{c.N, c.M}
		case exam.SummarySettingsChanged:
			names := make([]string, len(c.Settings))
			for j, name := range c.Settings {
				names[j] = l.T("summary.setting." + strings.ReplaceAll(name, " ", "_"))
			}
			args = []any{strings.Join(names, ", ")}
		}
		parts[i] = l.T("summary."+string(c.Kind), args...)
	}
	return strings.Join(parts, "; ")
}
