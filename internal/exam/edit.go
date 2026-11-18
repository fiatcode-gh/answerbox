package exam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// Settings are an exam's title and header settings as the editor shows them.
type Settings struct {
	Title           string
	Shuffle         examfile.Shuffle
	ShowResult      examfile.ShowResult
	Monitor         bool
	DurationMinutes int
}

// OptionSource is an answer option's text as written.
type OptionSource struct {
	Text    string
	Correct bool
}

// QuestionSource is a question's text and options as written.
type QuestionSource struct {
	Text    string
	Options []OptionSource
	// Image is an image file to add with the save, nil for none. Its
	// reference becomes the question text's new last line.
	Image []byte
}

// Source is the current revision of an exam as the editor shows it.
type Source struct {
	Revision  int
	Settings  Settings
	Questions []QuestionSource
}

var (
	errTitleLine   = &examfile.Error{Msg: "the title must be one line of text"}
	errTooLarge    = &examfile.Error{Msg: "exam with its images is larger than 10 MB, the largest file that can be imported"}
	errNotStorable = &examfile.Error{Msg: "this text cannot be stored in an exam file"}
	errImageSize   = &examfile.Error{Msg: "image is larger than 5 MB"}
	errImageType   = &examfile.Error{Msg: "image is not a PNG, JPEG, GIF or WebP file"}
	errImageHidden = &examfile.Error{Msg: "the image line would not show at the end of this question text, " +
		"for example after raw HTML; change the end of the text and save again"}

	lineStartRe = regexp.MustCompile(`^line \d+: `)
	lineRefRe   = regexp.MustCompile(` \(line \d+\)`)
)

// Source returns the current revision of an exam in actor's scope. An exam
// outside the scope is ErrNotFound, one without a revision ErrNoRevision and
// one with attempts ErrFrozen.
func (s *Service) Source(ctx context.Context, actor Actor, examID int64) (Source, error) {
	cur, err := s.currentRevision(ctx, actor, examID)
	if err != nil {
		return Source{}, err
	}
	if cur.Frozen {
		return Source{}, ErrFrozen
	}
	ex := cur.Exam
	src := Source{
		Revision: cur.Number,
		Settings: Settings{
			Title:           ex.Title,
			Shuffle:         ex.Shuffle,
			ShowResult:      ex.ShowResult,
			Monitor:         ex.Monitor,
			DurationMinutes: ex.DurationMinutes,
		},
		Questions: make([]QuestionSource, len(ex.Questions)),
	}
	for i, q := range ex.Questions {
		qs := QuestionSource{Text: q.Source, Options: make([]OptionSource, len(q.Options))}
		for j, o := range q.Options {
			qs.Options[j] = OptionSource{Text: o.Source, Correct: o.Correct}
		}
		src.Questions[i] = qs
	}
	return src, nil
}

// Create makes a new exam from set and its first question: closed, owned by
// actor (also when actor is an admin), with revision 1 "Created in the
// editor". The question's image, if any, goes into the new exam's fresh
// media folder. A text or image the exam file rules refuse is
// *examfile.Error and stores nothing.
func (s *Service) Create(ctx context.Context, actor Actor, set Settings, q QuestionSource) (int64, error) {
	key, err := s.newMediaKey()
	if err != nil {
		return 0, err
	}
	files := map[string][]byte{}
	var imageName string
	if q.Image != nil {
		img, err := newImage(q.Image)
		if err != nil {
			return 0, err
		}
		files[img.Name], imageName = img.Data, img.Name
	}
	cand := examfile.Exam{Dir: ".", Questions: []examfile.Question{typedQuestion(q, imageName)}}
	applySettings(&cand, set)
	p, err := parseEdited(cand, ".", files, key)
	if err != nil {
		return 0, err
	}
	if imageName != "" {
		if _, ok := p.ImagePaths[imageName]; !ok {
			return 0, errImageHidden
		}
	}
	if len(p.Images) > 0 {
		if _, err := s.writeImages(key, p.Images); err != nil {
			s.media.RemoveAll(key)
			return 0, err
		}
	}
	id, err := s.insertExam(ctx, actor.TeacherID, summaryCreated, p, key)
	if err != nil {
		s.media.RemoveAll(key)
		return 0, err
	}
	return id, nil
}

// SaveQuestion replaces question position (1-based) of the exam's revision
// base with q.
func (s *Service) SaveQuestion(ctx context.Context, actor Actor, examID int64, base, position int, q QuestionSource) error {
	return s.edit(ctx, actor, examID, base, q.Image,
		func(next *examfile.Exam, imageName string) error {
			if !inRange(position, next) {
				return ErrNotFound
			}
			next.Questions[position-1] = typedQuestion(q, imageName)
			return nil
		},
		func(before, after examfile.Exam) string {
			summary := fmt.Sprintf("Question %d edited", position)
			if correctSource(before.Questions[position-1]) != correctSource(after.Questions[position-1]) {
				summary += "; correct option changed"
			}
			return summary
		})
}

// AddQuestion appends q to the exam's revision base.
func (s *Service) AddQuestion(ctx context.Context, actor Actor, examID int64, base int, q QuestionSource) error {
	return s.edit(ctx, actor, examID, base, q.Image,
		func(next *examfile.Exam, imageName string) error {
			next.Questions = append(next.Questions, typedQuestion(q, imageName))
			return nil
		},
		func(_, after examfile.Exam) string {
			return fmt.Sprintf("Question %d added", len(after.Questions))
		})
}

// RemoveQuestion removes question position (1-based) from the exam's
// revision base.
func (s *Service) RemoveQuestion(ctx context.Context, actor Actor, examID int64, base, position int) error {
	return s.edit(ctx, actor, examID, base, nil,
		func(next *examfile.Exam, _ string) error {
			if !inRange(position, next) {
				return ErrNotFound
			}
			next.Questions = slices.Delete(next.Questions, position-1, position)
			return nil
		},
		func(_, _ examfile.Exam) string {
			return fmt.Sprintf("Question %d removed", position)
		})
}

// MoveQuestion moves question position to number to (both 1-based) in the
// exam's revision base.
func (s *Service) MoveQuestion(ctx context.Context, actor Actor, examID int64, base, position, to int) error {
	return s.edit(ctx, actor, examID, base, nil,
		func(next *examfile.Exam, _ string) error {
			if !inRange(position, next) || !inRange(to, next) {
				return ErrNotFound
			}
			q := next.Questions[position-1]
			next.Questions = slices.Insert(slices.Delete(next.Questions, position-1, position), to-1, q)
			return nil
		},
		func(_, _ examfile.Exam) string {
			return fmt.Sprintf("Question %d moved to %d", position, to)
		})
}

// SaveSettings sets the title and header settings of the exam's revision
// base.
func (s *Service) SaveSettings(ctx context.Context, actor Actor, examID int64, base int, set Settings) error {
	return s.edit(ctx, actor, examID, base, nil,
		func(next *examfile.Exam, _ string) error {
			applySettings(next, set)
			return nil
		},
		func(before, after examfile.Exam) string {
			var changed []string
			for _, f := range []struct {
				name string
				diff bool
			}{
				{"title", before.Title != after.Title},
				{"shuffle", before.Shuffle != after.Shuffle},
				{"result shown", before.ShowResult != after.ShowResult},
				{"monitor", before.Monitor != after.Monitor},
				{"length", before.DurationMinutes != after.DurationMinutes},
			} {
				if f.diff {
					changed = append(changed, f.name)
				}
			}
			return "Settings changed: " + strings.Join(changed, ", ")
		})
}

// edit is the one editor save path. It applies apply to a copy of the
// current revision, validates the result as an exam file and stores it as
// the next revision with the summary summary builds from the stored texts.
// A result equal to the current revision stores nothing. A non-nil image
// joins the exam's media folder; apply gets its file name ("" without one)
// to reference it.
func (s *Service) edit(ctx context.Context, actor Actor, examID int64, base int, image []byte,
	apply func(next *examfile.Exam, imageName string) error, summary func(before, after examfile.Exam) string) error {
	cur, err := s.currentRevision(ctx, actor, examID)
	if err != nil {
		return err
	}
	if cur.Frozen {
		return ErrFrozen
	}
	if base != cur.Number {
		return ErrStale
	}
	var img examfile.Image
	if image != nil {
		if img, err = newImage(image); err != nil {
			return err
		}
	}
	next := cur.Exam
	next.Questions = make([]examfile.Question, len(cur.Exam.Questions))
	for i, q := range cur.Exam.Questions {
		q.Options = slices.Clone(q.Options)
		next.Questions[i] = q
	}
	if err := apply(&next, img.Name); err != nil {
		return err
	}
	files, err := s.files(cur)
	if err != nil {
		return err
	}
	if image != nil {
		imgPath := path.Join(cur.Exam.Dir, img.Name)
		if held, ok := files[imgPath]; ok && !bytes.Equal(held, img.Data) {
			return &examfile.Error{Msg: "this exam already has a different image file named " + img.Name}
		}
		files[imgPath] = img.Data
	}
	p, err := parseEdited(next, cur.Exam.Dir, files, cur.MediaKey)
	if err != nil {
		return err
	}
	if image != nil {
		if _, ok := p.ImagePaths[path.Join(cur.Exam.Dir, img.Name)]; !ok {
			return errImageHidden
		}
	}
	if sameExam(cur.Exam, p, false) {
		return nil
	}
	if image == nil {
		return s.replaceContent(ctx, actor, examID, base, summary(cur.Exam, p), p, cur.MediaKey)
	}

	s.imageSaves.Lock()
	defer s.imageSaves.Unlock()
	created, err := s.writeImages(cur.MediaKey, []examfile.Image{img})
	if err == nil {
		err = s.replaceContent(ctx, actor, examID, base, summary(cur.Exam, p), p, cur.MediaKey)
	}
	if err != nil {
		for _, name := range created {
			s.media.Remove(cur.MediaKey + "/" + name)
		}
		// Succeeds only for a folder this save created and left empty.
		s.media.Remove(cur.MediaKey)
	}
	return err
}

// newImage checks an uploaded image against the exam file's limits and
// names it by content.
func newImage(data []byte) (examfile.Image, error) {
	if len(data) > examfile.MaxZipEntryBytes {
		return examfile.Image{}, errImageSize
	}
	name, ok := examfile.ImageName(data)
	if !ok {
		return examfile.Image{}, errImageType
	}
	return examfile.Image{Name: name, Data: data}, nil
}

// withImageLine adds a reference to the image file name as text's new last
// line.
func withImageLine(text, name string) string {
	ref := "![](" + name + ")"
	if strings.TrimSpace(text) == "" {
		return ref
	}
	return strings.TrimRightFunc(text, unicode.IsSpace) + "\n" + ref
}

// parseEdited validates next as the exam file it formats to, with dir and
// files as the upload's folder and image files, and returns the parsed exam
// to store under mediaKey. Refusals are *examfile.Error worded for the
// editor. The result is what importing its own download gives.
func parseEdited(next examfile.Exam, dir string, files map[string][]byte, mediaKey string) (examfile.Exam, error) {
	if strings.ContainsAny(next.Title, "\r\n") {
		return examfile.Exam{}, errTitleLine
	}
	base := "/media/" + mediaKey
	p1, err := examfile.Parse(examfile.Upload{Text: examfile.Format(next), Dir: dir, Files: files}, base)
	if err != nil {
		return examfile.Exam{}, editorError(err)
	}
	if p1.Title != next.Title {
		return examfile.Exam{}, errTitleLine
	}
	if k := shapeDiffers(next, p1); k > 0 {
		return examfile.Exam{}, &examfile.Error{Msg: fmt.Sprintf(`question %d: the text cannot be saved as written: `+
			`a line that starts with "-" would become an option, and an option that ends with "*" would become the correct one; `+
			`write \- or \* instead`, k)}
	}

	dl := examfile.Upload{Text: examfile.Format(p1), Dir: dir, Files: make(map[string][]byte, len(p1.ImagePaths))}
	for p := range p1.ImagePaths {
		dl.Files[p] = files[p]
	}
	data, _, err := examfile.EncodeUpload(dl)
	if err != nil {
		return examfile.Exam{}, fmt.Errorf("exam: encode edited exam: %w", err)
	}
	if len(data) > examfile.MaxUploadBytes {
		return examfile.Exam{}, errTooLarge
	}
	u2, err := examfile.ReadUpload(data)
	if err != nil {
		return examfile.Exam{}, editorError(err)
	}
	p2, err := examfile.Parse(u2, base)
	if err != nil {
		return examfile.Exam{}, editorError(err)
	}
	if !sameExam(p1, p2, true) {
		return examfile.Exam{}, errNotStorable
	}
	return p1, nil
}

// editorError rewords a file rule refusal for a form: without line numbers,
// and a missing title as such. Other errors are returned unchanged.
func editorError(err error) error {
	var fileErr *examfile.Error
	if !errors.As(err, &fileErr) {
		return err
	}
	msg := lineRefRe.ReplaceAllString(lineStartRe.ReplaceAllString(fileErr.Msg, ""), "")
	if strings.HasSuffix(msg, `expected "# Title" as the first line`) {
		msg = "the exam needs a title"
	}
	return &examfile.Error{Msg: msg}
}

// shapeDiffers returns the 1-based number of the first question whose
// option count, correct flags or block texts apart from white space differ
// between typed and parsed, or 0. A text that differs moved into another
// block, for example through an unbalanced code fence.
func shapeDiffers(typed, parsed examfile.Exam) int {
	for i := range max(len(typed.Questions), len(parsed.Questions)) {
		if i >= len(typed.Questions) || i >= len(parsed.Questions) {
			return i + 1
		}
		a, b := typed.Questions[i].Options, parsed.Questions[i].Options
		if len(a) != len(b) {
			return i + 1
		}
		for j := range a {
			if a[j].Correct != b[j].Correct {
				return i + 1
			}
		}
		if !sameText(typed.Questions[i].Source, parsed.Questions[i].Source) {
			return i + 1
		}
		for j := range a {
			if !sameText(a[j].Source, b[j].Source) {
				return i + 1
			}
		}
	}
	return 0
}

// sameText reports whether a and b are equal once all white space is removed
// from both.
func sameText(a, b string) bool {
	for {
		a, b = strings.TrimLeftFunc(a, unicode.IsSpace), strings.TrimLeftFunc(b, unicode.IsSpace)
		if a == "" || b == "" {
			return a == b
		}
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			return false
		}
		a, b = a[na:], b[nb:]
	}
}

// sameExam reports whether a and b have the same title, settings, Sources
// and correct flags, and with html also the same HTML.
func sameExam(a, b examfile.Exam, html bool) bool {
	if a.Title != b.Title || a.Shuffle != b.Shuffle || a.ShowResult != b.ShowResult ||
		a.Monitor != b.Monitor || a.DurationMinutes != b.DurationMinutes || len(a.Questions) != len(b.Questions) {
		return false
	}
	for i, qa := range a.Questions {
		qb := b.Questions[i]
		if qa.Source != qb.Source || len(qa.Options) != len(qb.Options) || html && qa.BodyHTML != qb.BodyHTML {
			return false
		}
		for j, oa := range qa.Options {
			ob := qb.Options[j]
			if oa.Source != ob.Source || oa.Correct != ob.Correct || html && oa.BodyHTML != ob.BodyHTML {
				return false
			}
		}
	}
	return true
}

func inRange(position int, ex *examfile.Exam) bool {
	return position >= 1 && position <= len(ex.Questions)
}

// typedQuestion is q as a question to format: every block through
// typedBlock and, for a non-empty imageName, a reference to that image as
// the text's new last line.
func typedQuestion(q QuestionSource, imageName string) examfile.Question {
	text := q.Text
	if imageName != "" {
		text = withImageLine(text, imageName)
	}
	out := examfile.Question{Source: typedBlock(text), Options: make([]examfile.Option, len(q.Options))}
	for i, o := range q.Options {
		out.Options[i] = examfile.Option{Source: typedBlock(o.Text), Correct: o.Correct}
	}
	return out
}

// typedBlock is a typed question or option text as a block to format: line
// ends normalised, and a block whose first line is a code fence moved onto
// continuation lines, the only place the file format reads a fence.
func typedBlock(text string) string {
	text = examfile.NormalizeNewlines(text)
	if first, _, _ := strings.Cut(text, "\n"); examfile.IsFence(first) {
		return "\n" + text
	}
	return text
}

func applySettings(ex *examfile.Exam, set Settings) {
	ex.Title = strings.TrimSpace(examfile.NormalizeNewlines(set.Title))
	ex.Shuffle, ex.ShowResult, ex.Monitor, ex.DurationMinutes = set.Shuffle, set.ShowResult, set.Monitor, set.DurationMinutes
}

// correctSource returns the Source of q's correct option.
func correctSource(q examfile.Question) string {
	for _, o := range q.Options {
		if o.Correct {
			return o.Source
		}
	}
	return ""
}
