package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
)

// mountTeacherAccounts registers the Teachers page, for admins only.
func (s *server) mountTeacherAccounts(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/teachers", s.adminOnly(s.teacherList))
	admin.HandleFunc("POST /admin/teachers", s.adminOnly(s.addTeacher))
	admin.HandleFunc("GET /admin/teachers/{tid}/remove", s.adminOnly(s.removeTeacherConfirm))
	admin.HandleFunc("POST /admin/teachers/{tid}/remove", s.adminOnly(s.removeTeacher))
}

// adminOnly answers 404 to a teacher who is not an admin, as for any unknown
// address. It is the only place the admin role gates a route.
func (s *server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sessionFrom(r.Context()).Teacher.Admin {
			s.notFound(w, r)
			return
		}
		next(w, r)
	}
}

type teacherRow struct {
	ID                     int64
	Email, Name, LastLogin string
	Admin                  bool
	Exams                  int
}

type teachersData struct {
	adminPage
	Teachers              []teacherRow
	AddEmail, Alert, Zone string
}

func (s *server) teacherList(w http.ResponseWriter, r *http.Request) {
	s.renderTeachers(w, r, loc(r), http.StatusOK, "", "")
}

func (s *server) renderTeachers(w http.ResponseWriter, r *http.Request, l localizer, status int, addEmail, alert string) {
	list, err := s.deps.Admin.Teachers(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows := make([]teacherRow, len(list))
	for i, t := range list {
		row := teacherRow{ID: t.ID, Email: t.Email, Name: t.Name, LastLogin: l.T("teachers.never"), Admin: t.Admin, Exams: t.ExamCount}
		if row.Name == "" {
			row.Name = "—"
		}
		if !t.LastLoginAt.IsZero() {
			row.LastLogin = s.fullTime(t.LastLoginAt)
		}
		rows[i] = row
	}
	s.page(w, r, status, "admin_teachers.html", teachersData{
		adminPage: adminPageFor(r),
		Teachers:  rows,
		AddEmail:  addEmail,
		Alert:     alert,
		Zone:      s.deps.Location.String(),
	})
}

func (s *server) addTeacher(w http.ResponseWriter, r *http.Request) {
	email := r.PostFormValue("email")
	err := s.deps.Admin.AddTeacher(r.Context(), email)
	switch {
	case errors.Is(err, adminauth.ErrInvalidEmail):
		l := loc(r)
		s.renderTeachers(w, r, l, http.StatusUnprocessableEntity, email, l.T("teachers.invalid_email"))
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/admin/teachers", http.StatusSeeOther)
	}
}

// removeTeacherConfirm asks before Remove: 404 for a teacher not on the
// list, the Teachers page with 409 for an admin, as the POST answers.
func (s *server) removeTeacherConfirm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("tid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	list, err := s.deps.Admin.Teachers(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	i := slices.IndexFunc(list, func(t adminauth.Teacher) bool { return t.ID == id })
	l := loc(r)
	switch {
	case i < 0:
		s.notFound(w, r)
		return
	case list[i].Admin:
		s.renderTeachers(w, r, l, http.StatusConflict, "", l.T("teachers.admin_email"))
		return
	}
	s.confirmPage(w, r, confirmData{
		adminPage: adminPageFor(r),
		Heading:   l.T("confirm.remove_teacher.heading"),
		Question:  l.T("confirm.remove_teacher.question", list[i].Email),
		Notes: []string{
			l.T("confirm.remove_teacher.note_signed_out"),
			l.T("confirm.remove_teacher.note_exams", list[i].ExamCount),
			l.T("confirm.remove_teacher.note_add_again"),
		},
		FormID: "remove-teacher-form",
		Action: "/admin/teachers/" + strconv.FormatInt(id, 10) + "/remove",
		Submit: l.T("confirm.remove_teacher.submit"),
		Danger: true,
		Cancel: "/admin/teachers",
	})
}

func (s *server) removeTeacher(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("tid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	err = s.deps.Admin.RemoveTeacher(r.Context(), id)
	switch {
	case errors.Is(err, adminauth.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, adminauth.ErrAdminEmail):
		l := loc(r)
		s.renderTeachers(w, r, l, http.StatusConflict, "", l.T("teachers.admin_email"))
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/admin/teachers", http.StatusSeeOther)
	}
}
