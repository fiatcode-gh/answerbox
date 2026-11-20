package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// mountTeacherExams registers the exam pages on the admin sub-mux.
func (s *server) mountTeacherExams(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/exams", http.StatusSeeOther)
	})
	admin.HandleFunc("GET /admin/exams", s.examList)
	admin.HandleFunc("POST /admin/exams", s.importExam)
	admin.HandleFunc("GET /admin/exams/{id}", s.examDetail)
	admin.HandleFunc("GET /admin/exams/{id}/projector", s.examProjector)
	admin.HandleFunc("POST /admin/exams/{id}/replace", s.replaceExam)
	admin.HandleFunc("POST /admin/exams/{id}/open", s.examAction(s.deps.Exams.Open, openCloseLanding))
	admin.HandleFunc("POST /admin/exams/{id}/close", s.examAction(s.deps.Exams.Close, openCloseLanding))
	admin.HandleFunc("POST /admin/exams/{id}/schedule", s.setExamSchedule)
	admin.HandleFunc("GET /admin/exams/{id}/download", s.downloadExam)
	admin.HandleFunc("POST /admin/exams/{id}/copy", s.copyExam)
	admin.HandleFunc("GET /admin/exams/{id}/archive", s.archiveConfirm)
	admin.HandleFunc("POST /admin/exams/{id}/archive", s.examAction(s.deps.Exams.Archive, toExamPage))
	admin.HandleFunc("POST /admin/exams/{id}/unarchive", s.examAction(s.deps.Exams.Unarchive, toExamPage))
}

type examListData struct {
	adminPage
	Exams []examRow
	Alert string
	Zone  string
	// Scheduled is whether a listed exam has a schedule.
	Scheduled bool
	// ShowArchived is whether the list includes archived exams.
	ShowArchived bool
	// HiddenArchived counts the archived exams in scope the list leaves out.
	HiddenArchived int
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
	data := examListData{adminPage: adminPageFor(r), Alert: alert, Zone: s.deps.Location.String()}
	data.ShowArchived = r.URL.Query().Get("archived") == "1"
	for _, e := range exams {
		if e.Archived() && !data.ShowArchived {
			data.HiddenArchived++
			continue
		}
		data.Exams = append(data.Exams, examRow{Exam: e, Opens: s.scheduleTime(e.Schedule.OpensAt), Closes: s.scheduleTime(e.Schedule.ClosesAt)})
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

// rerenderFunc renders the form page a request came from again, with status
// and an alert above the form.
type rerenderFunc func(status int, alert string)

// readUpload reads the "file" part of the multipart form requireTeacher has
// already parsed. On failure it has responded — 422 through rerender, 413 or
// 500 — and returns ok false.
func (s *server) readUpload(w http.ResponseWriter, r *http.Request, l localizer, rerender rerenderFunc) (data []byte, ok bool) {
	f, _, err := r.FormFile("file")
	if err != nil {
		rerender(http.StatusUnprocessableEntity, l.T("exams.missing_file"))
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
		rerender(http.StatusUnprocessableEntity, l.T("exams.missing_file"))
	default:
		return data, true
	}
	return nil, false
}

func (s *server) importExam(w http.ResponseWriter, r *http.Request) {
	l := loc(r)
	rerender := func(status int, alert string) { s.renderExamList(w, r, status, alert) }
	data, ok := s.readUpload(w, r, l, rerender)
	if !ok {
		return
	}
	id, err := s.deps.Exams.Import(r.Context(), actorFrom(r), data)
	var invalid *examfile.Error
	switch {
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, l.T("exams.import_failed", l.FileError(invalid)))
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, examPath(id), http.StatusSeeOther)
	}
}

func examPath(id int64) string {
	return "/admin/exams/" + strconv.FormatInt(id, 10)
}

// shuffle names a shuffle setting; an unknown value is "".
func (l localizer) shuffle(v examfile.Shuffle) string {
	switch v {
	case examfile.ShuffleNone:
		return l.T("setting.shuffle.none")
	case examfile.ShuffleQuestions:
		return l.T("setting.shuffle.questions")
	case examfile.ShuffleQuestionsOptions:
		return l.T("setting.shuffle.questions_options")
	}
	return ""
}

// showResult names what students see after an attempt; an unknown value is
// "".
func (l localizer) showResult(v examfile.ShowResult) string {
	switch v {
	case examfile.ShowNone:
		return l.T("setting.show_result.none")
	case examfile.ShowScore:
		return l.T("setting.show_result.score")
	case examfile.ShowReview:
		return l.T("setting.show_result.review")
	}
	return ""
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
	// ScheduleOpen is whether the schedule form starts unfolded: a schedule
	// is set or a refused save is shown again.
	ScheduleOpen bool
	Zone         string
	// ArchivedOn is the archive date as text, empty when not archived.
	ArchivedOn string
	// Counts splits the exam's attempts by status; AttemptsLabel is their
	// total as "1 attempt" or "N attempts".
	Counts        attempt.Counts
	AttemptsLabel string
}

// scheduleInput is the schedule form's two datetime-local values.
type scheduleInput struct{ Opens, Closes string }

func (s *server) examDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderExamDetail(w, r, loc(r), http.StatusOK, id, "", nil)
}

// examID parses the {id} path value; ok is false when it is not a number.
func examID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// renderExamDetail renders the exam page in l. typed holds the schedule
// form values as the teacher entered them; nil fills the form from the exam.
func (s *server) renderExamDetail(w http.ResponseWriter, r *http.Request, l localizer, status int, id int64, alert string, typed *scheduleInput) {
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
	counts, err := s.deps.Attempts.Counts(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	archivedOn := ""
	if e.Archived() {
		archivedOn = l.Date(e.ArchivedAt.In(s.deps.Location))
	}
	input := scheduleInput{Opens: s.scheduleInputValue(e.Schedule.OpensAt), Closes: s.scheduleInputValue(e.Schedule.ClosesAt)}
	if typed != nil {
		input = *typed
	}
	s.page(w, r, status, "admin_exam.html", examDetailData{
		adminPage:        adminPageFor(r),
		Exam:             e,
		ShuffleText:      l.shuffle(e.Shuffle),
		ShowResultText:   l.showResult(e.ShowResult),
		Questions:        questions,
		Alert:            alert,
		FrozenNotice:     l.T("examdetail.frozen"),
		NoRevisionNotice: l.T("examdetail.no_revision"),
		StudentLink:      s.deps.PublicURL + "/?code=" + e.Code,
		Opens:            s.scheduleTime(e.Schedule.OpensAt),
		Closes:           s.scheduleTime(e.Schedule.ClosesAt),
		Schedule:         input,
		ScheduleOpen:     e.Schedule.Set() || typed != nil,
		Zone:             s.deps.Location.String(),
		ArchivedOn:       archivedOn,
		Counts:           counts,
		AttemptsLabel:    l.N("examdetail.attempts", counts.Total()),
	})
}

// projectorData is the page a teacher shows the room: the exam's title, the
// address to open and the code to type.
type projectorData struct {
	adminPage
	Exam        exam.Exam
	Host        string // the public address without its scheme, as students type it
	StudentLink string
}

func (s *server) examProjector(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	e, err := s.deps.Exams.Get(r.Context(), actorFrom(r), id)
	if errors.Is(err, exam.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	host := strings.TrimPrefix(strings.TrimPrefix(s.deps.PublicURL, "https://"), "http://")
	s.page(w, r, http.StatusOK, "admin_projector.html", projectorData{
		adminPage:   adminPageFor(r),
		Exam:        e,
		Host:        host,
		StudentLink: s.deps.PublicURL + "/?code=" + e.Code,
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

// scheduleMessages are the keys of the schedule rules' refusals.
var scheduleMessages = []struct {
	err error
	key string
}{
	{exam.ErrScheduleEmpty, "examdetail.schedule.empty"},
	{exam.ErrScheduleOrder, "examdetail.schedule.order"},
	{exam.ErrSchedulePast, "examdetail.schedule.past"},
}

// setExamSchedule gives an exam the open and close times of the schedule
// form. The rules live in exam.Service; this only reads the two inputs.
func (s *server) setExamSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	l := loc(r)
	typed := scheduleInput{Opens: r.PostFormValue("opens_at"), Closes: r.PostFormValue("closes_at")}
	rerender := func(alert string) {
		s.renderExamDetail(w, r, l, http.StatusUnprocessableEntity, id, alert, &typed)
	}
	opens, okOpens := s.parseScheduleInput(typed.Opens)
	closes, okCloses := s.parseScheduleInput(typed.Closes)
	if !okOpens || !okCloses {
		rerender(l.T("examdetail.schedule.invalid_time"))
		return
	}
	err := s.deps.Exams.SetSchedule(r.Context(), actorFrom(r), id, exam.Schedule{OpensAt: opens, ClosesAt: closes})
	for _, m := range scheduleMessages {
		if errors.Is(err, m.err) {
			rerender(l.T(m.key))
			return
		}
	}
	switch {
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, exam.ErrArchived):
		s.renderExamDetail(w, r, l, http.StatusConflict, id, l.T("examdetail.archived_refusal"), nil)
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
	l := loc(r)
	rerender := func(status int, alert string) { s.renderExamDetail(w, r, l, status, id, alert, nil) }
	data, ok := s.readUpload(w, r, l, rerender)
	if !ok {
		return
	}
	err := s.deps.Exams.Replace(r.Context(), actorFrom(r), id, data)
	var invalid *examfile.Error
	switch {
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, l.T("examdetail.replace_failed", l.FileError(invalid)))
	case errors.Is(err, exam.ErrFrozen):
		rerender(http.StatusConflict, l.T("examdetail.frozen"))
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
		l := loc(r)
		s.renderExamDetail(w, r, l, http.StatusConflict, id, l.T("examdetail.no_revision"), nil)
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
		l := loc(r)
		s.renderExamDetail(w, r, l, http.StatusConflict, id, l.T("examdetail.no_revision"), nil)
	case err != nil:
		s.serverError(w, r, err)
	default:
		http.Redirect(w, r, examPath(newID), http.StatusSeeOther)
	}
}

// fromExamPage is the value of the "from" field in the exam page's Open
// and Close forms.
const fromExamPage = "exam"

// openCloseLanding is where a successful Open or Close goes: the exam page
// when the form says it was pressed there, the exams list otherwise. The
// field only chooses between these two paths and is never part of one.
func openCloseLanding(r *http.Request, id int64) string {
	if r.PostFormValue("from") == fromExamPage {
		return examPath(id)
	}
	return "/admin/exams"
}

// toExamPage is the exam page as the landing of a one-press action.
func toExamPage(_ *http.Request, id int64) string { return examPath(id) }

// examAction returns the handler of a one-press exam action (open, close,
// archive, unarchive): it applies change for the signed-in teacher and
// redirects to next(r, id).
func (s *server) examAction(change func(context.Context, exam.Actor, int64) error, next func(r *http.Request, id int64) string) http.HandlerFunc {
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
		case errors.Is(err, exam.ErrArchived):
			l := loc(r)
			s.renderExamDetail(w, r, l, http.StatusConflict, id, l.T("examdetail.archived_refusal"), nil)
		case err != nil:
			s.serverError(w, r, err)
		default:
			http.Redirect(w, r, next(r, id), http.StatusSeeOther)
		}
	}
}

// archiveConfirm asks before Archive; an exam already archived has nothing
// to ask, so it leads back to its page.
func (s *server) archiveConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	e, err := s.deps.Exams.Get(r.Context(), actorFrom(r), id)
	switch {
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	case e.Archived():
		http.Redirect(w, r, examPath(id), http.StatusSeeOther)
		return
	}
	l := loc(r)
	s.confirmPage(w, r, confirmData{
		adminPage: adminPageFor(r),
		Heading:   l.T("confirm.archive.heading"),
		Question:  l.T("confirm.archive.question", e.Title),
		Notes: []string{
			l.T("confirm.archive.note_close"),
			l.T("confirm.archive.note_list"),
			l.T("confirm.archive.note_keeps"),
			l.T("confirm.archive.note_unarchive"),
		},
		FormID: "archive-form",
		Action: examPath(id) + "/archive",
		Submit: l.T("confirm.archive.submit"),
		Cancel: examPath(id),
	})
}
