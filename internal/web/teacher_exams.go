package web

import (
	"net/http"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// mountTeacherExams registers the exam pages on the admin sub-mux.
func (s *server) mountTeacherExams(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
	})
	admin.HandleFunc("GET /admin/exams", s.examList)
}

type examListData struct {
	adminPage
	Exams []exam.Exam
}

func (s *server) examList(w http.ResponseWriter, r *http.Request) {
	exams, err := s.deps.Exams.List(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.page(w, r, http.StatusOK, "admin_exams.html", examListData{adminPage: adminPageFor(r), Exams: exams})
}
