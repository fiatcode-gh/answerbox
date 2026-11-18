package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

type sectionData struct {
	adminPage
	ExamID    int64
	ExamTitle string
	// Heading is "Section K" or "New section at question N".
	Heading string
	// Action is the path the form posts to.
	Action string
	// Revision is the revision the form posts, as posted on a re-render.
	Revision    string
	Title, Text string
	// ImageDropped is whether the request carried an image the re-rendered
	// form cannot keep.
	ImageDropped bool
	Alert        string
}

func sectionPath(id int64, k int) string {
	return examPath(id) + "/sections/" + strconv.Itoa(k)
}

// sectionPage is the section form page of exam id under heading, posting to
// action, opened on the current revision of src.
func sectionPage(r *http.Request, id int64, src exam.Source, heading, action string) sectionData {
	return sectionData{
		adminPage: adminPageFor(r),
		ExamID:    id,
		ExamTitle: src.Settings.Title,
		Heading:   heading,
		Action:    action,
		Revision:  strconv.Itoa(src.Revision),
	}
}

// newSectionPage is the blank form of a section starting at question {n},
// which must be a question of the current text that starts no section.
func (s *server) newSectionPage(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > len(src.Questions) || slices.ContainsFunc(src.Sections, func(sec exam.SectionSource) bool { return sec.Start == n }) {
		s.notFound(w, r)
		return
	}
	page := sectionPage(r, id, src, "New section at question "+strconv.Itoa(n), questionPath(id, n)+"/section")
	s.page(w, r, http.StatusOK, "admin_section.html", page)
}

// startSection handles the form of a new section at question {n}. Like
// saveSection it leaves n to the service, so an older form gets the stale
// answer.
func (s *server) startSection(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	page := sectionPage(r, id, src, "New section at question "+strconv.Itoa(n), questionPath(id, n)+"/section")
	s.postSection(w, r, page, "#q"+strconv.Itoa(n), func(base int, sec exam.SectionSource) error {
		return s.deps.Exams.StartSection(r.Context(), actorFrom(r), id, base, n, sec)
	})
}

// editSectionPage is the form of section {n} of the current text.
func (s *server) editSectionPage(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	k, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || k < 1 || k > len(src.Sections) {
		s.notFound(w, r)
		return
	}
	page := sectionPage(r, id, src, "Section "+strconv.Itoa(k), sectionPath(id, k))
	page.Title, page.Text = src.Sections[k-1].Title, src.Sections[k-1].Text
	s.page(w, r, http.StatusOK, "admin_section.html", page)
}

// saveSection handles the edit form of section {n}. It does not check n
// against the loaded text: a form opened on an older revision must get the
// stale answer, and the service answers an unknown number with ErrNotFound.
func (s *server) saveSection(w http.ResponseWriter, r *http.Request) {
	id, src, ok := s.editorSource(w, r)
	if !ok {
		return
	}
	k, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	page := sectionPage(r, id, src, "Section "+strconv.Itoa(k), sectionPath(id, k))
	s.postSection(w, r, page, "#s"+strconv.Itoa(k), func(base int, sec exam.SectionSource) error {
		return s.deps.Exams.SaveSection(r.Context(), actorFrom(r), id, base, k, sec)
	})
}

// postSection answers a posted section form: it stores the section through
// store and on success goes to anchor on the editor page.
func (s *server) postSection(w http.ResponseWriter, r *http.Request, page sectionData, anchor string, store func(base int, sec exam.SectionSource) error) {
	page.Title = r.PostFormValue("title")
	page.Text = r.PostFormValue("text")
	page.Revision = r.PostFormValue("revision")
	img := postedImage(r)
	rerender := func(status int, alert string) {
		page.ImageDropped = img != nil
		page.Alert = alert
		s.page(w, r, status, "admin_section.html", page)
	}
	image, err := readImage(img)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = store(formInt(r, "revision"), exam.SectionSource{Title: page.Title, Text: page.Text, Image: image})
	var invalid *examfile.Error
	switch {
	case err == nil:
		http.Redirect(w, r, editPath(page.ExamID)+anchor, http.StatusSeeOther)
	case errors.Is(err, exam.ErrStale):
		rerender(http.StatusConflict, staleNotice)
	case errors.As(err, &invalid):
		rerender(http.StatusUnprocessableEntity, notSavedPrefix+invalid.Msg)
	default:
		s.editorRefused(w, r, page.ExamID, err)
	}
}
