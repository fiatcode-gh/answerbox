package web

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

const staleNotice = "This exam changed since you opened this page. Nothing was saved."

// notSavedPrefix starts the alert of an editor save the file rules refused.
const notSavedPrefix = "Not saved: "

// mountTeacherEditor registers the exam editor on the admin sub-mux. The
// rules of every save live in exam.Service; these handlers only read the
// path and form values and choose the answer.
func (s *server) mountTeacherEditor(admin *http.ServeMux) {
	admin.HandleFunc("GET /admin/exams/{id}/edit", s.editorPage)
	admin.HandleFunc("POST /admin/exams/{id}/questions/{n}/move", s.editorAction(func(ctx context.Context, a exam.Actor, id int64, base, n int, r *http.Request) error {
		return s.deps.Exams.MoveQuestion(ctx, a, id, base, n, formInt(r, "to"))
	}))
	admin.HandleFunc("GET /admin/exams/{id}/questions/{n}/remove", s.removeQuestionConfirm)
	admin.HandleFunc("POST /admin/exams/{id}/questions/{n}/remove", s.editorAction(func(ctx context.Context, a exam.Actor, id int64, base, n int, _ *http.Request) error {
		return s.deps.Exams.RemoveQuestion(ctx, a, id, base, n)
	}))
	admin.HandleFunc("GET /admin/exams/{id}/sections/{n}/remove", s.removeSectionConfirm)
	admin.HandleFunc("POST /admin/exams/{id}/sections/{n}/remove", s.editorAction(func(ctx context.Context, a exam.Actor, id int64, base, n int, _ *http.Request) error {
		return s.deps.Exams.RemoveSection(ctx, a, id, base, n)
	}))
	admin.HandleFunc("GET /admin/exams/{id}/settings", s.settingsPage)
	admin.HandleFunc("POST /admin/exams/{id}/settings", s.saveSettings)
	admin.HandleFunc("GET /admin/exams/{id}/questions/{n}", s.editQuestionPage)
	admin.HandleFunc("POST /admin/exams/{id}/questions/{n}", s.saveQuestion)
	admin.HandleFunc("GET /admin/exams/{id}/questions/new", s.newQuestionPage)
	admin.HandleFunc("POST /admin/exams/{id}/questions/new", s.addQuestion)
	admin.HandleFunc("GET /admin/exams/{id}/questions/{n}/section", s.newSectionPage)
	admin.HandleFunc("POST /admin/exams/{id}/questions/{n}/section", s.startSection)
	admin.HandleFunc("GET /admin/exams/{id}/sections/{n}", s.editSectionPage)
	admin.HandleFunc("POST /admin/exams/{id}/sections/{n}", s.saveSection)
	admin.HandleFunc("GET /admin/exams/new", s.newExamPage)
	admin.HandleFunc("POST /admin/exams/new", s.createExam)
}

// formInt is the form value name as a number, 0 when it is not one.
func formInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(r.PostFormValue(name))
	if err != nil {
		return 0
	}
	return n
}

func editPath(id int64) string {
	return examPath(id) + "/edit"
}

// editorRefused answers err when it is not a refusal the editor form shows
// itself (ErrStale or a file rule): 404 for ErrNotFound, the exam page with
// 409 for ErrFrozen and ErrNoRevision, 500 for anything else. It reports
// whether it answered.
func (s *server) editorRefused(w http.ResponseWriter, r *http.Request, id int64, err error) bool {
	var invalid *examfile.Error
	switch {
	case err == nil, errors.Is(err, exam.ErrStale), errors.As(err, &invalid):
		return false
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, exam.ErrNoRevision):
		s.renderExamDetail(w, r, http.StatusConflict, id, noRevisionNotice, nil)
	case errors.Is(err, exam.ErrFrozen):
		s.renderExamDetail(w, r, http.StatusConflict, id, frozenNotice, nil)
	default:
		s.serverError(w, r, err)
	}
	return true
}

// answerSave answers an editor save that returned err: a redirect to target
// when it saved, rerender with 409 and the stale notice for an outdated
// page, with 422 and "Not saved: …" when the file rules refused the text or
// image, and editorRefused for anything else.
func (s *server) answerSave(w http.ResponseWriter, r *http.Request, id int64, err error, target string, rerender rerenderFunc) {
	var invalid *examfile.Error
	switch {
	case err == nil:
		http.Redirect(w, r, target, http.StatusSeeOther)
	case errors.Is(err, exam.ErrStale):
		rerender(http.StatusConflict, staleNotice)
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, notSavedPrefix+invalid.Msg)
	default:
		s.editorRefused(w, r, id, err)
	}
}

// editorQuestion is a question of the editor page with its actions.
type editorQuestion struct {
	exam.Question
	// SectionNumber is the number of the section that starts at this
	// question, 0 when none does.
	SectionNumber int
	// Up and Down are the numbers Move up and Move down move it to.
	Up, Down                       int
	CanMoveUp, CanMoveDown, Remove bool
	// StartSection is whether the page offers to start a section here.
	StartSection bool
}

type editorData struct {
	adminPage
	Exam      exam.Exam
	Revision  int
	Questions []editorQuestion
	Alert     string
}

func (s *server) editorPage(w http.ResponseWriter, r *http.Request) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderEditor(w, r, http.StatusOK, id, "")
}

// editorRevision loads exam id and its current revision for an editor page:
// it answers 404 out of scope, the 409 exam page for an exam without a
// revision or with attempts, and 500 on a failed read. It reports whether
// the caller goes on.
func (s *server) editorRevision(w http.ResponseWriter, r *http.Request, id int64) (exam.Exam, exam.Revision, bool) {
	e, err := s.deps.Exams.Get(r.Context(), actorFrom(r), id)
	switch {
	case errors.Is(err, exam.ErrNotFound):
		s.notFound(w, r)
		return exam.Exam{}, exam.Revision{}, false
	case err != nil:
		s.serverError(w, r, err)
		return exam.Exam{}, exam.Revision{}, false
	case e.Revision == 0:
		s.renderExamDetail(w, r, http.StatusConflict, id, noRevisionNotice, nil)
		return exam.Exam{}, exam.Revision{}, false
	case e.AttemptCount > 0:
		s.renderExamDetail(w, r, http.StatusConflict, id, frozenNotice, nil)
		return exam.Exam{}, exam.Revision{}, false
	}
	rev, err := s.deps.Exams.Revision(r.Context(), id, e.Revision)
	if err != nil {
		s.serverError(w, r, err)
		return exam.Exam{}, exam.Revision{}, false
	}
	return e, rev, true
}

// renderEditor renders the editor page of the exam's current revision. The
// questions and the revision number the forms post come from one revision
// row, so they agree even when a save lands between the two reads.
func (s *server) renderEditor(w http.ResponseWriter, r *http.Request, status int, id int64, alert string) {
	e, rev, ok := s.editorRevision(w, r, id)
	if !ok {
		return
	}
	data := editorData{
		adminPage: adminPageFor(r),
		Exam:      e,
		Revision:  rev.Number,
		Questions: make([]editorQuestion, len(rev.Questions)),
		Alert:     alert,
	}
	// group[i] is the number of the section question i+1 belongs to, 0
	// before any section; size[k] counts the questions of section k.
	last := len(rev.Questions)
	group := make([]int, last)
	size := []int{0}
	for i, q := range rev.Questions {
		if q.Section != nil {
			size = append(size, 0)
		}
		group[i] = len(size) - 1
		size[group[i]]++
	}
	sectioned := len(size) > 1
	for i, q := range rev.Questions {
		n := i + 1
		// The only question of a section can neither leave it nor go: the
		// service would refuse the empty section.
		alone := size[group[i]] == 1
		eq := editorQuestion{
			Question:     q,
			Up:           n - 1,
			Down:         n + 1,
			CanMoveUp:    n > 1 && !alone,
			CanMoveDown:  n < last && !alone,
			Remove:       last > 1 && !alone,
			StartSection: q.Section == nil && (sectioned || n == 1),
		}
		if q.Section != nil {
			eq.SectionNumber = group[i]
		}
		data.Questions[i] = eq
	}
	s.page(w, r, status, "admin_exam_edit.html", data)
}

// editorConfirmTarget reads the exam and number {n} of an editor
// confirmation and loads the exam's current revision, answering as
// editorRevision does and 404 for an {n} that is not a number. It reports
// whether the caller goes on.
func (s *server) editorConfirmTarget(w http.ResponseWriter, r *http.Request) (exam.Exam, exam.Revision, int, bool) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return exam.Exam{}, exam.Revision{}, 0, false
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		s.notFound(w, r)
		return exam.Exam{}, exam.Revision{}, 0, false
	}
	e, rev, ok := s.editorRevision(w, r, id)
	return e, rev, n, ok
}

func (s *server) removeQuestionConfirm(w http.ResponseWriter, r *http.Request) {
	e, rev, n, ok := s.editorConfirmTarget(w, r)
	if !ok {
		return
	}
	if n < 1 || n > len(rev.Questions) {
		s.notFound(w, r)
		return
	}
	// The editor offers no Remove for a section's only question or the
	// exam's only question; a typed URL gets what posting the removal
	// answers. The parser refuses an empty section before an empty exam.
	section := 0 // the number of question n's section, 0 before any
	for _, q := range rev.Questions[:n] {
		if q.Section != nil {
			section++
		}
	}
	alone := rev.Questions[n-1].Section != nil && (n == len(rev.Questions) || rev.Questions[n].Section != nil)
	switch {
	case alone:
		s.renderEditor(w, r, http.StatusUnprocessableEntity, e.ID, notSavedPrefix+"section "+strconv.Itoa(section)+" would be left without questions; remove the section first")
		return
	case len(rev.Questions) == 1:
		s.renderEditor(w, r, http.StatusUnprocessableEntity, e.ID, notSavedPrefix+"exam has no questions")
		return
	}
	num := strconv.Itoa(n)
	s.confirmPage(w, r, confirmData{
		adminPage: adminPageFor(r),
		Heading:   "Remove question " + num,
		Question:  "Remove question " + num + " from ‘" + e.Title + "’?",
		Notes:     []string{"This makes a new revision. The question stays readable in earlier revisions, but it cannot be restored from there."},
		Preview:   &rev.Questions[n-1],
		FormID:    "remove-question-form",
		Action:    questionPath(e.ID, n) + "/remove",
		Revision:  strconv.Itoa(rev.Number),
		Submit:    "Remove question",
		Danger:    true,
		Cancel:    editPath(e.ID) + "#q" + num,
	})
}

func (s *server) removeSectionConfirm(w http.ResponseWriter, r *http.Request) {
	e, rev, k, ok := s.editorConfirmTarget(w, r)
	if !ok {
		return
	}
	var sections []*exam.Section
	for _, q := range rev.Questions {
		if q.Section != nil {
			sections = append(sections, q.Section)
		}
	}
	if k < 1 || k > len(sections) {
		s.notFound(w, r)
		return
	}
	var first string
	switch {
	case k > 1:
		first = "Its questions join the section before it."
	case len(sections) > 1:
		first = "Its questions join the next section, which then starts at question 1."
	default:
		first = "The questions stay. The exam has no sections after this."
	}
	num := strconv.Itoa(k)
	s.confirmPage(w, r, confirmData{
		adminPage: adminPageFor(r),
		Heading:   "Remove section " + num,
		Question:  "Remove section " + num + ", ‘" + sections[k-1].Title + "’, from ‘" + e.Title + "’?",
		Notes:     []string{first, "This makes a new revision. The section stays readable in earlier revisions."},
		FormID:    "remove-section-form",
		Action:    sectionPath(e.ID, k) + "/remove",
		Revision:  strconv.Itoa(rev.Number),
		Submit:    "Remove section",
		Danger:    true,
		Cancel:    editPath(e.ID) + "#s" + num,
	})
}

// editorAction returns the handler of a question or section action of the
// editor page: act stores it for question or section {n} on the posted
// revision.
func (s *server) editorAction(act func(ctx context.Context, a exam.Actor, id int64, base, n int, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := examID(r)
		if !ok {
			s.notFound(w, r)
			return
		}
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil {
			s.notFound(w, r)
			return
		}
		err = act(r.Context(), actorFrom(r), id, formInt(r, "revision"), n, r)
		s.answerSave(w, r, id, err, editPath(id), func(status int, alert string) {
			s.renderEditor(w, r, status, id, alert)
		})
	}
}

// choice is an option of a settings select.
type choice struct {
	Value, Label string
	Selected     bool
}

// settingsInput holds the values of the settings fields as typed.
type settingsInput struct {
	Title, Shuffle, ShowResult string
	Monitor                    bool
	Duration                   string
}

func settingsInputOf(set exam.Settings) settingsInput {
	return settingsInput{
		Title:      set.Title,
		Shuffle:    string(set.Shuffle),
		ShowResult: string(set.ShowResult),
		Monitor:    set.Monitor,
		Duration:   strconv.Itoa(set.DurationMinutes),
	}
}

// settingsInputFrom reads the settings fields of the posted form.
func settingsInputFrom(r *http.Request) settingsInput {
	return settingsInput{
		Title:      r.PostFormValue("title"),
		Shuffle:    r.PostFormValue("shuffle"),
		ShowResult: r.PostFormValue("show_result"),
		Monitor:    r.PostForm.Has("monitor"),
		Duration:   r.PostFormValue("duration"),
	}
}

// settings is the typed input for the service; a duration that is not a
// number is 0, which the service refuses with its own message.
func (in settingsInput) settings() exam.Settings {
	minutes, err := strconv.Atoi(strings.TrimSpace(in.Duration))
	if err != nil {
		minutes = 0
	}
	return exam.Settings{
		Title:           in.Title,
		Shuffle:         examfile.Shuffle(in.Shuffle),
		ShowResult:      examfile.ShowResult(in.ShowResult),
		Monitor:         in.Monitor,
		DurationMinutes: minutes,
	}
}

// ShuffleChoices are the shuffle select's options; an unknown typed value
// selects none.
func (in settingsInput) ShuffleChoices() []choice {
	out := make([]choice, 0, 3)
	for _, v := range []examfile.Shuffle{examfile.ShuffleNone, examfile.ShuffleQuestions, examfile.ShuffleQuestionsOptions} {
		out = append(out, choice{Value: string(v), Label: shuffleText[v], Selected: in.Shuffle == string(v)})
	}
	return out
}

// ShowResultChoices are the show_result select's options; an unknown typed
// value selects none.
func (in settingsInput) ShowResultChoices() []choice {
	out := make([]choice, 0, 3)
	for _, v := range []examfile.ShowResult{examfile.ShowNone, examfile.ShowScore, examfile.ShowReview} {
		out = append(out, choice{Value: string(v), Label: showResultText[v], Selected: in.ShowResult == string(v)})
	}
	return out
}

type settingsData struct {
	adminPage
	ExamID int64
	// ExamTitle is the stored title, for the page title and back link.
	ExamTitle string
	// Revision is the revision the form posts, as posted on a refusal.
	Revision string
	Input    settingsInput
	Alert    string
}

// editorSource reads the exam's current text; on failure it has answered.
func (s *server) editorSource(w http.ResponseWriter, r *http.Request) (int64, exam.Source, bool) {
	id, ok := examID(r)
	if !ok {
		s.notFound(w, r)
		return 0, exam.Source{}, false
	}
	src, err := s.deps.Exams.Source(r.Context(), actorFrom(r), id)
	if err != nil {
		if !s.editorRefused(w, r, id, err) {
			s.serverError(w, r, err)
		}
		return 0, exam.Source{}, false
	}
	return id, src, true
}

func (s *server) settingsPage(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	s.page(w, r, http.StatusOK, "admin_exam_settings.html", settingsData{
		adminPage: adminPageFor(r),
		ExamID:    id,
		ExamTitle: src.Settings.Title,
		Revision:  strconv.Itoa(src.Revision),
		Input:     settingsInputOf(src.Settings),
	})
}

func (s *server) saveSettings(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	in := settingsInputFrom(r)
	err := s.deps.Exams.SaveSettings(r.Context(), actorFrom(r), id, formInt(r, "revision"), in.settings())
	rerender := func(status int, alert string) {
		s.page(w, r, status, "admin_exam_settings.html", settingsData{
			adminPage: adminPageFor(r),
			ExamID:    id,
			ExamTitle: src.Settings.Title,
			Revision:  r.PostFormValue("revision"),
			Input:     in,
			Alert:     alert,
		})
	}
	s.answerSave(w, r, id, err, editPath(id), rerender)
}

// questionForm is the question part of an editor form as posted. The form
// carries both the option list and the accepted-answer list; FillIn says
// which one a save stores.
type questionForm struct {
	Text    string
	FillIn  bool // the question is answered by typing
	Options []string
	Correct int      // index into Options; -1 when none is chosen
	Answers []string // accepted answers
}

// blankQuestionForm is the form of a question not written yet.
func blankQuestionForm() questionForm {
	return questionForm{Options: make([]string, 4), Correct: -1, Answers: []string{""}}
}

// questionFormOf is the form opened on a stored question.
func questionFormOf(q exam.QuestionSource) questionForm {
	if len(q.Answers) > 0 {
		return questionForm{Text: q.Text, FillIn: true, Options: make([]string, 4), Correct: -1, Answers: slices.Clone(q.Answers)}
	}
	f := questionForm{Text: q.Text, Options: make([]string, len(q.Options)), Correct: -1, Answers: []string{""}}
	for i, o := range q.Options {
		f.Options[i] = o.Text
		if o.Correct {
			f.Correct = i
		}
	}
	return f
}

// questionFormFrom reads the question fields of the posted form.
func questionFormFrom(r *http.Request) questionForm {
	f := questionForm{
		Text:    r.PostFormValue("text"),
		FillIn:  r.PostFormValue("kind") == "fill_in",
		Options: slices.Clone(r.PostForm["option"]),
		Correct: -1,
		Answers: slices.Clone(r.PostForm["answer"]),
	}
	if c, err := strconv.Atoi(r.PostFormValue("correct")); err == nil && c >= 0 && c < len(f.Options) {
		f.Correct = c
	}
	return f
}

// apply changes the form as a form button do asks: "kind:choice" or
// "kind:fill_in" switch the kind, "answer-add" and "answer-remove:<i>"
// change the accepted-answer list, "add", "remove:<i>", "up:<i>" and
// "down:<i>" the option list. The correct choice follows the option it
// points at. Any other value, or an index that is not a row or cannot move
// that way, changes nothing.
func (f *questionForm) apply(do string) {
	switch do {
	case "kind:choice":
		f.FillIn = false
		return
	case "kind:fill_in":
		f.FillIn = true
		return
	case "answer-add":
		if len(f.Answers) < examfile.MaxAnswers {
			f.Answers = append(f.Answers, "")
		}
		return
	case "add":
		f.Options = append(f.Options, "")
		return
	}
	verb, arg, _ := strings.Cut(do, ":")
	i, err := strconv.Atoi(arg)
	if err != nil || i < 0 {
		return
	}
	if verb == "answer-remove" {
		if i < len(f.Answers) && len(f.Answers) > 1 {
			f.Answers = slices.Delete(f.Answers, i, i+1)
		}
		return
	}
	if i >= len(f.Options) {
		return
	}
	switch verb {
	case "remove":
		f.Options = slices.Delete(f.Options, i, i+1)
		switch {
		case f.Correct == i:
			f.Correct = -1
		case f.Correct > i:
			f.Correct--
		}
	case "up":
		if i >= 1 {
			f.swap(i-1, i)
		}
	case "down":
		if i < len(f.Options)-1 {
			f.swap(i, i+1)
		}
	}
}

func (f *questionForm) swap(i, j int) {
	f.Options[i], f.Options[j] = f.Options[j], f.Options[i]
	switch f.Correct {
	case i:
		f.Correct = j
	case j:
		f.Correct = i
	}
}

// source is the form as the service takes it, with image to add (nil for
// none): the accepted answers of a fill-in form, the options otherwise.
func (f questionForm) source(image []byte) exam.QuestionSource {
	if f.FillIn {
		return exam.QuestionSource{Text: f.Text, Answers: f.Answers, Image: image}
	}
	q := exam.QuestionSource{Text: f.Text, Options: make([]exam.OptionSource, len(f.Options)), Image: image}
	for i, o := range f.Options {
		q.Options[i] = exam.OptionSource{Text: o, Correct: i == f.Correct}
	}
	return q
}

// questionFields is the view of the question_fields partial.
type questionFields struct {
	Text string
	// FillIn is whether the form is a fill-in form; the other list stays in
	// the form, hidden.
	FillIn  bool
	Options []optionRow
	// CanAdd is whether the form offers Add option.
	CanAdd  bool
	Answers []answerRow
	// CanAddAnswer is whether the form offers Add accepted answer.
	CanAddAnswer bool
	// ImageDropped is whether the request carried an image the re-rendered
	// form cannot keep.
	ImageDropped bool
}

// optionRow is an option of the question form with the buttons it shows.
type optionRow struct {
	Index, Number    int
	Text             string
	Checked          bool
	Up, Down, Remove bool
}

// answerRow is an accepted answer of the question form; Remove is whether
// it shows a Remove button.
type answerRow struct {
	Index, Number int
	Text          string
	Remove        bool
}

func (f questionForm) fields(imageDropped bool) questionFields {
	last := len(f.Options) - 1
	v := questionFields{
		Text:         f.Text,
		FillIn:       f.FillIn,
		Options:      make([]optionRow, len(f.Options)),
		CanAdd:       len(f.Options) < examfile.MaxOptions,
		Answers:      make([]answerRow, len(f.Answers)),
		CanAddAnswer: len(f.Answers) < examfile.MaxAnswers,
		ImageDropped: imageDropped,
	}
	for i, o := range f.Options {
		v.Options[i] = optionRow{
			Index:   i,
			Number:  i + 1,
			Text:    o,
			Checked: i == f.Correct,
			Up:      i > 0,
			Down:    i < last,
			Remove:  len(f.Options) > 2,
		}
	}
	for i, a := range f.Answers {
		v.Answers[i] = answerRow{Index: i, Number: i + 1, Text: a, Remove: len(f.Answers) > 1}
	}
	return v
}

// postedImage is the "image" part of the posted multipart form; nil when
// there is none or it is empty.
func postedImage(r *http.Request) *multipart.FileHeader {
	if r.MultipartForm == nil {
		return nil
	}
	files := r.MultipartForm.File["image"]
	if len(files) == 0 || files[0].Size == 0 {
		return nil
	}
	return files[0]
}

// readImage reads the image part, nil for none. It reads one byte past the
// largest image so that the service refuses an oversize one with its
// message.
func readImage(fh *multipart.FileHeader) ([]byte, error) {
	if fh == nil {
		return nil, nil
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, examfile.MaxZipEntryBytes+1))
}

type questionData struct {
	adminPage
	ExamID    int64
	ExamTitle string
	// Heading is "Question N" or "New question N".
	Heading string
	// Action is the path the form posts to.
	Action string
	// Revision is the revision the form posts, as posted on a re-render.
	Revision string
	Fields   questionFields
	Alert    string
}

func questionPath(id int64, n int) string {
	return examPath(id) + "/questions/" + strconv.Itoa(n)
}

// questionPage is the question form page of exam id under heading, posting
// to action, opened on the current revision of src.
func questionPage(r *http.Request, id int64, src exam.Source, heading, action string) questionData {
	return questionData{
		adminPage: adminPageFor(r),
		ExamID:    id,
		ExamTitle: src.Settings.Title,
		Heading:   heading,
		Action:    action,
		Revision:  strconv.Itoa(src.Revision),
	}
}

func (s *server) editQuestionPage(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > len(src.Questions) {
		s.notFound(w, r)
		return
	}
	page := questionPage(r, id, src, "Question "+strconv.Itoa(n), questionPath(id, n))
	page.Fields = questionFormOf(src.Questions[n-1]).fields(false)
	s.page(w, r, http.StatusOK, "admin_question.html", page)
}

// saveQuestion handles the edit form of question {n}. It does not check n
// against the loaded text: a form opened on an older revision must get the
// stale answer, and the service answers an unknown number with ErrNotFound.
func (s *server) saveQuestion(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	page := questionPage(r, id, src, "Question "+strconv.Itoa(n), questionPath(id, n))
	s.postQuestion(w, r, page, n, func(base int, q exam.QuestionSource) error {
		return s.deps.Exams.SaveQuestion(r.Context(), actorFrom(r), id, base, n, q)
	})
}

func (s *server) newQuestionPage(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	page := questionPage(r, id, src, "New question "+strconv.Itoa(len(src.Questions)+1), examPath(id)+"/questions/new")
	page.Fields = blankQuestionForm().fields(false)
	s.page(w, r, http.StatusOK, "admin_question.html", page)
}

func (s *server) addQuestion(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	n := len(src.Questions) + 1
	page := questionPage(r, id, src, "New question "+strconv.Itoa(n), examPath(id)+"/questions/new")
	s.postQuestion(w, r, page, n, func(base int, q exam.QuestionSource) error {
		return s.deps.Exams.AddQuestion(r.Context(), actorFrom(r), id, base, q)
	})
}

// postQuestion answers a posted question form: an option action re-renders
// page, do=save stores the question through store and on success goes to
// question n on the editor page.
func (s *server) postQuestion(w http.ResponseWriter, r *http.Request, page questionData, n int, store func(base int, q exam.QuestionSource) error) {
	form := questionFormFrom(r)
	img := postedImage(r)
	page.Revision = r.PostFormValue("revision")
	rerender := func(status int, alert string) {
		page.Fields = form.fields(img != nil)
		page.Alert = alert
		s.page(w, r, status, "admin_question.html", page)
	}
	if do := r.PostFormValue("do"); do != "save" {
		form.apply(do)
		rerender(http.StatusOK, "")
		return
	}
	image, err := readImage(img)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = store(formInt(r, "revision"), form.source(image))
	s.answerSave(w, r, page.ExamID, err, editPath(page.ExamID)+"#q"+strconv.Itoa(n), rerender)
}

type newExamData struct {
	adminPage
	Input  settingsInput
	Fields questionFields
	Alert  string
}

func (s *server) newExamPage(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, http.StatusOK, "admin_exam_new.html", newExamData{
		adminPage: adminPageFor(r),
		Input: settingsInput{
			Shuffle:    string(examfile.ShuffleQuestions),
			ShowResult: string(examfile.ShowScore),
			Monitor:    true,
			Duration:   strconv.Itoa(examfile.DefaultDurationMinutes),
		},
		Fields: blankQuestionForm().fields(false),
	})
}

// createExam answers the new-exam form: an option action re-renders it,
// do=save creates the exam and goes to its editor page.
func (s *server) createExam(w http.ResponseWriter, r *http.Request) {
	in := settingsInputFrom(r)
	form := questionFormFrom(r)
	img := postedImage(r)
	rerender := func(status int, alert string) {
		s.page(w, r, status, "admin_exam_new.html", newExamData{
			adminPage: adminPageFor(r),
			Input:     in,
			Fields:    form.fields(img != nil),
			Alert:     alert,
		})
	}
	if do := r.PostFormValue("do"); do != "save" {
		form.apply(do)
		rerender(http.StatusOK, "")
		return
	}
	image, err := readImage(img)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	id, err := s.deps.Exams.Create(r.Context(), actorFrom(r), in.settings(), form.source(image))
	var invalid *examfile.Error
	switch {
	case err == nil:
		http.Redirect(w, r, editPath(id), http.StatusSeeOther)
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, notSavedPrefix+invalid.Msg)
	default:
		s.serverError(w, r, err)
	}
}
