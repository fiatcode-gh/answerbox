package exam_test

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// sectionedExam has two sections of two questions each; Part A has
// instructions, Part B none.
const sectionedExam = "# Parts\nshuffle: none\n\n" +
	"## Part A\nRead **carefully**.\n\n" +
	"1. First\n- a *\n- b\n\n2. Second\n- c\n- d *\n\n" +
	"## Part B\n\n" +
	"3. Third\n- e *\n- f\n\n4. Fourth\n- g\n- h *\n"

const partAHTML = "<p>Read <strong>carefully</strong>.</p>\n"

// sectionStarts returns the title and start of each section of src as
// "Title@start".
func sectionStarts(src exam.Source) []string {
	var out []string
	for _, s := range src.Sections {
		out = append(out, fmt.Sprintf("%s@%d", s.Title, s.Start))
	}
	return out
}

// requireNoSection fails unless no live question of id starts a section and
// its latest revision has no sections key.
func requireNoSection(t *testing.T, h *harness, id int64) {
	t.Helper()
	for _, q := range h.mustQuestions(t, id) {
		if q.Section != nil {
			t.Errorf("question %d starts section %+v, want none", q.Position, q.Section)
		}
	}
	if got := h.contentKeys(t, id); !slices.Equal(got, []string{"dir", "questions"}) {
		t.Errorf("content keys = %q, want dir, questions", got)
	}
}

// requireSectioned fails unless id holds the two sections of sectionedExam.
func requireSectioned(t *testing.T, h *harness, id int64) {
	t.Helper()
	want := map[int]exam.Section{1: {Title: "Part A", BodyHTML: partAHTML}, 3: {Title: "Part B"}}
	for _, q := range h.mustQuestions(t, id) {
		w, starts := want[q.Position]
		switch {
		case !starts && q.Section != nil:
			t.Errorf("question %d starts section %+v, want none", q.Position, q.Section)
		case starts && (q.Section == nil || *q.Section != w):
			t.Errorf("question %d section = %+v, want %+v", q.Position, q.Section, w)
		}
	}
	if got := h.contentKeys(t, id); !slices.Equal(got, []string{"dir", "questions", "sections"}) {
		t.Errorf("content keys = %q, want dir, questions, sections", got)
	}
}

func TestImport_StoresSections(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(sectionedExam))
	requireSectioned(t, h, id)

	var title, source, html string
	var start, second int
	err := h.db.QueryRow(`SELECT json_extract(content, '$.sections[0].title'), json_extract(content, '$.sections[0].source'),
		json_extract(content, '$.sections[0].html'), json_extract(content, '$.sections[0].start'), json_extract(content, '$.sections[1].start')
		FROM exam_revision WHERE exam_id = ? AND number = 1`, id).Scan(&title, &source, &html, &start, &second)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Part A" || source != "Read **carefully**." || html != partAHTML || start != 1 || second != 3 {
		t.Errorf("content sections: %q, %q, %q, %d, %d", title, source, html, start, second)
	}
	requireLiveMatchesRevision(t, h, id)

	rev := h.mustRevision(t, id, 1)
	if s := rev.Questions[0].Section; s == nil || *s != (exam.Section{Title: "Part A", BodyHTML: partAHTML}) {
		t.Errorf("revision question 1 section = %+v", s)
	}
	if s := rev.Questions[2].Section; s == nil || *s != (exam.Section{Title: "Part B"}) {
		t.Errorf("revision question 3 section = %+v", s)
	}
	if rev.Questions[1].Section != nil || rev.Questions[3].Section != nil {
		t.Errorf("revision questions 2 and 4 start a section")
	}
}

func TestReplace_SectionsFollowTheFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))

	mustNil(t, "Replace without sections", h.svc.Replace(ctx, h.owner, id, []byte(editExam)))
	requireNoSection(t, h, id)
	requireLiveMatchesRevision(t, h, id)

	mustNil(t, "Replace with sections", h.svc.Replace(ctx, h.owner, id, []byte(sectionedExam)))
	requireSectioned(t, h, id)
	requireLiveMatchesRevision(t, h, id)
}

func TestDownload_SectionedRoundTrip(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(sectionedExam))
	f := h.mustDownload(t, h.owner, id)
	if !strings.Contains(string(f.Data), "\n## Part A\nRead **carefully**.\n\n1. First\n") {
		t.Errorf("download =\n%s\nwant Part A with its instructions before question 1", f.Data)
	}
	requireEqualExams(t, h, id, h.mustImport(t, f.Data))

	img := pngBytes(t, color.RGBA{4, 5, 6, 255})
	zipped := h.mustImport(t, buildZip(t,
		zipEntry{"exam.md", []byte("# Pictures\n\n## Look\nSee ![map](i.png)\n\n1. Q\n- a *\n- b\n")},
		zipEntry{"i.png", img},
	))
	if html := string(h.mustQuestions(t, zipped)[0].Section.BodyHTML); !strings.Contains(html, "/media/"+h.mediaKey(t, zipped)+"/"+imageName(img)) {
		t.Errorf("section HTML = %q, want the image in the media folder", html)
	}
	zf := h.mustDownload(t, h.owner, zipped)
	if zf.ContentType != "application/zip" {
		t.Errorf("content type = %q, want application/zip", zf.ContentType)
	}
	requireEqualExams(t, h, zipped, h.mustImport(t, zf.Data))
}

func TestCopy_KeepsSections(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(sectionedExam))
	copyID := h.mustCopy(t, h.owner, id)
	requireSameQuestions(t, h, id, copyID)
	requireSectioned(t, h, copyID)
}

func TestSource_Sections(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(sectionedExam))
	want := []exam.SectionSource{
		{Title: "Part A", Text: "Read **carefully**.", Start: 1},
		{Title: "Part B", Text: "", Start: 3},
	}
	if got := h.mustSource(t, h.owner, id).Sections; !reflect.DeepEqual(got, want) {
		t.Errorf("Sections = %+v, want %+v", got, want)
	}
	plain := h.mustImport(t, []byte(editExam))
	if got := h.mustSource(t, h.owner, plain).Sections; got != nil {
		t.Errorf("Sections without sections = %+v, want nil", got)
	}
}

func TestStartEditRemoveSection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))
	steps := []struct {
		do      func(base int) error
		summary string
		starts  []string
	}{
		{func(base int) error {
			return h.svc.StartSection(ctx, h.owner, id, base, 2, exam.SectionSource{Title: " Middle ", Text: "Mind the\r\nunits."})
		}, "Section 2 started at question 2", []string{"Part A@1", "Middle@2", "Part B@3"}},
		{func(base int) error {
			return h.svc.SaveSection(ctx, h.owner, id, base, 2, exam.SectionSource{Title: "Centre"})
		},
			"Section 2 edited", []string{"Part A@1", "Centre@2", "Part B@3"}},
		{func(base int) error { return h.svc.RemoveSection(ctx, h.owner, id, base, 2) },
			"Section 2 removed", []string{"Part A@1", "Part B@3"}},
		{func(base int) error { return h.svc.RemoveSection(ctx, h.owner, id, base, 1) },
			"Section 1 removed", []string{"Part B@1"}},
		{func(base int) error { return h.svc.RemoveSection(ctx, h.owner, id, base, 1) },
			"Section 1 removed", nil},
	}
	for i, s := range steps {
		mustNil(t, s.summary, s.do(h.current(t, id)))
		if got := h.latestSummary(t, id); got != s.summary {
			t.Errorf("summary = %q, want %q", got, s.summary)
		}
		src := h.mustSource(t, h.owner, id)
		if got := sectionStarts(src); !slices.Equal(got, s.starts) {
			t.Errorf("after %q sections = %q, want %q", s.summary, got, s.starts)
		}
		switch i {
		case 0:
			if got := src.Sections[1].Text; got != "Mind the\nunits." {
				t.Errorf("section 2 text = %q, want %q", got, "Mind the\nunits.")
			}
			requireRoundTrip(t, h, id)
		case 1:
			if got := src.Sections[1].Text; got != "" {
				t.Errorf("section 2 text = %q, want empty", got)
			}
		}
		if got := questionTexts(src); !slices.Equal(got, []string{"First", "Second", "Third", "Fourth"}) {
			t.Errorf("after %q questions = %q", s.summary, got)
		}
		requireLiveMatchesRevision(t, h, id)
	}
	if src := h.mustSource(t, h.owner, id); src.Sections != nil {
		t.Errorf("Sections = %+v, want nil", src.Sections)
	}
	requireNoSection(t, h, id)
	requireRoundTrip(t, h, id)

	plain := h.mustImport(t, []byte(editExam))
	mustNil(t, "StartSection", h.svc.StartSection(ctx, h.owner, plain, 1, 1, exam.SectionSource{Title: "Only"}))
	if got := h.latestSummary(t, plain); got != "Section 1 started at question 1" {
		t.Errorf("summary = %q", got)
	}
	if got := sectionStarts(h.mustSource(t, h.owner, plain)); !slices.Equal(got, []string{"Only@1"}) {
		t.Errorf("sections = %q, want Only@1", got)
	}
	requireLiveMatchesRevision(t, h, plain)
}

// requireFileError fails unless err is *examfile.Error with message want.
func requireFileError(t *testing.T, what string, err error, want string) {
	t.Helper()
	var fileErr *examfile.Error
	if !errors.As(err, &fileErr) || fileErr.Msg != want {
		t.Errorf("%s: err = %v, want *examfile.Error %q", what, err, want)
	}
}

const leftWithout = "section %d would be left without questions; remove the section first"

func TestMoveQuestion_AcrossSections(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))
	steps := []struct {
		position, to int
		summary      string
		starts       []string
		texts        []string
	}{
		{3, 2, "Question 3 moved to section 1", []string{"Part A@1", "Part B@4"}, []string{"First", "Second", "Third", "Fourth"}},
		{3, 4, "Question 3 moved to section 2", []string{"Part A@1", "Part B@3"}, []string{"First", "Second", "Third", "Fourth"}},
		{1, 2, "Question 1 moved to 2", []string{"Part A@1", "Part B@3"}, []string{"Second", "First", "Third", "Fourth"}},
	}
	for _, s := range steps {
		mustNil(t, s.summary, h.svc.MoveQuestion(ctx, h.owner, id, h.current(t, id), s.position, s.to))
		if got := h.latestSummary(t, id); got != s.summary {
			t.Errorf("summary = %q, want %q", got, s.summary)
		}
		src := h.mustSource(t, h.owner, id)
		if got := sectionStarts(src); !slices.Equal(got, s.starts) {
			t.Errorf("after %q sections = %q, want %q", s.summary, got, s.starts)
		}
		if got := questionTexts(src); !slices.Equal(got, s.texts) {
			t.Errorf("after %q questions = %q, want %q", s.summary, got, s.texts)
		}
		requireLiveMatchesRevision(t, h, id)
	}

	dump := h.examDump(t, id)
	if err := h.svc.MoveQuestion(ctx, h.owner, id, h.current(t, id), 1, 3); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Move(1, 3): err = %v, want ErrNotFound", err)
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("refused move stored something:\n%s", got)
	}

	mustNil(t, "Move(3, 2)", h.svc.MoveQuestion(ctx, h.owner, id, h.current(t, id), 3, 2))
	dump = h.examDump(t, id)
	err := h.svc.MoveQuestion(ctx, h.owner, id, h.current(t, id), 4, 3)
	requireFileError(t, "Move(4, 3)", err, fmt.Sprintf(leftWithout, 2))
	if got := h.examDump(t, id); got != dump {
		t.Errorf("refused move stored something:\n%s", got)
	}
}

func TestRemoveQuestion_InSections(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))

	mustNil(t, "RemoveQuestion(1)", h.svc.RemoveQuestion(ctx, h.owner, id, 1, 1))
	src := h.mustSource(t, h.owner, id)
	if got := sectionStarts(src); !slices.Equal(got, []string{"Part A@1", "Part B@2"}) {
		t.Errorf("sections = %q, want Part A@1, Part B@2", got)
	}
	if got := questionTexts(src); !slices.Equal(got, []string{"Second", "Third", "Fourth"}) {
		t.Errorf("questions = %q", got)
	}
	requireLiveMatchesRevision(t, h, id)

	dump := h.examDump(t, id)
	requireFileError(t, "RemoveQuestion(1) again", h.svc.RemoveQuestion(ctx, h.owner, id, 2, 1), fmt.Sprintf(leftWithout, 1))
	if got := h.examDump(t, id); got != dump {
		t.Errorf("refused remove stored something:\n%s", got)
	}

	added := h.mustImport(t, []byte(sectionedExam))
	mustNil(t, "AddQuestion", h.svc.AddQuestion(ctx, h.owner, added, 1, question("Fifth", 0, "x", "y")))
	if got := h.latestSummary(t, added); got != "Question 5 added" {
		t.Errorf("summary = %q, want Question 5 added", got)
	}
	if got := sectionStarts(h.mustSource(t, h.owner, added)); !slices.Equal(got, []string{"Part A@1", "Part B@3"}) {
		t.Errorf("sections after AddQuestion = %q, want Part A@1, Part B@3", got)
	}
	requireLiveMatchesRevision(t, h, added)
}

// manySections is an exam text with n two-option questions, each of the
// first sections ones starting a section, the last one also holding the
// questions after it.
func manySections(n, sections int) string {
	var b strings.Builder
	b.WriteString("# Many\n")
	for i := range n {
		if i < sections {
			fmt.Fprintf(&b, "\n## S%d\n", i+1)
		}
		fmt.Fprintf(&b, "\n%d. Q%d\n- a *\n- b\n", i+1, i+1)
	}
	return b.String()
}

func TestSections_Refusals(t *testing.T) {
	ctx := context.Background()
	save1 := func(sec exam.SectionSource) func(*harness, int64) error {
		return func(h *harness, id int64) error { return h.svc.SaveSection(ctx, h.owner, id, 1, 1, sec) }
	}
	start := func(position int) func(*harness, int64) error {
		return func(h *harness, id int64) error {
			return h.svc.StartSection(ctx, h.owner, id, 1, position, exam.SectionSource{Title: "New"})
		}
	}
	cases := []struct {
		name string
		text string // exam to edit; sectionedExam when empty
		do   func(*harness, int64) error
		want string
	}{
		{"empty title", "", save1(exam.SectionSource{Title: " \r\n "}), "section title is empty"},
		{"long title", "", save1(exam.SectionSource{Title: strings.Repeat("é", 201)}), "section title is longer than 200 characters"},
		{"title with a line break", "", save1(exam.SectionSource{Title: "One\r\nTwo"}), "the section title must be one line of text"},
		{"51st section", manySections(51, 50), start(51), "exam has more than 50 sections"},
		{"first section after question 1", editExam, start(2), "the first section must start at question 1"},
		{"unclosed fence", "", save1(exam.SectionSource{Title: "Part A", Text: "Code:\n```\nx"}), "code block is never closed"},
		{"missing image", "", save1(exam.SectionSource{Title: "Part A", Text: "See ![](gone.png)"}), `section 1: image "gone.png" not found in upload`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			text := c.text
			if text == "" {
				text = sectionedExam
			}
			id := h.mustImport(t, []byte(text))
			dump, media := h.examDump(t, id), h.mediaEntries(t)
			requireFileError(t, c.name, c.do(h, id), c.want)
			if got := h.examDump(t, id); got != dump {
				t.Errorf("refused save stored something:\n%s", got)
			}
			if got := h.mediaEntries(t); !slices.Equal(got, media) {
				t.Errorf("media entries = %v, want %v", got, media)
			}
		})
	}

	h := newHarness(t)
	id := h.mustImport(t, []byte(sectionedExam))
	plain := h.mustImport(t, []byte(editExam))
	dump, plainDump := h.examDump(t, id), h.examDump(t, plain)
	sec := exam.SectionSource{Title: "X"}
	calls := map[string]error{
		"StartSection 0":                   h.svc.StartSection(ctx, h.owner, id, 1, 0, sec),
		"StartSection 5":                   h.svc.StartSection(ctx, h.owner, id, 1, 5, sec),
		"StartSection 3":                   h.svc.StartSection(ctx, h.owner, id, 1, 3, sec),
		"SaveSection 0":                    h.svc.SaveSection(ctx, h.owner, id, 1, 0, sec),
		"SaveSection 3":                    h.svc.SaveSection(ctx, h.owner, id, 1, 3, sec),
		"RemoveSection 0":                  h.svc.RemoveSection(ctx, h.owner, id, 1, 0),
		"RemoveSection 3":                  h.svc.RemoveSection(ctx, h.owner, id, 1, 3),
		"SaveSection 1 without sections":   h.svc.SaveSection(ctx, h.owner, plain, 1, 1, sec),
		"RemoveSection 1 without sections": h.svc.RemoveSection(ctx, h.owner, plain, 1, 1),
	}
	for _, name := range slices.Sorted(maps.Keys(calls)) {
		if err := calls[name]; !errors.Is(err, exam.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("unknown section stored something:\n%s", got)
	}
	if got := h.examDump(t, plain); got != plainDump {
		t.Errorf("unknown section stored something:\n%s", got)
	}
}

func TestSections_StaleFrozenScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))
	calls := []editCall{
		{"SaveSection", func(a exam.Actor, base int) error {
			return h.svc.SaveSection(ctx, a, id, base, 1, exam.SectionSource{Title: "Renamed"})
		}},
		{"RemoveSection", func(a exam.Actor, base int) error { return h.svc.RemoveSection(ctx, a, id, base, 2) }},
	}
	dump := h.examDump(t, id)
	other := h.teacher(t, "other@example.test")
	for _, c := range calls {
		for _, base := range []int{0, 2} {
			if err := c.do(h.owner, base); !errors.Is(err, exam.ErrStale) {
				t.Errorf("%s(base %d): err = %v, want ErrStale", c.name, base, err)
			}
		}
		if err := c.do(other, 1); !errors.Is(err, exam.ErrNotFound) {
			t.Errorf("foreign %s: err = %v, want ErrNotFound", c.name, err)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("refused saves stored something:\n%s", got)
	}

	admin := h.admin(t)
	for i, c := range calls {
		mustNil(t, "admin "+c.name, c.do(admin, i+1))
	}
	if rev := h.mustRevisions(t, id)[0]; rev.Number != 3 || rev.TeacherEmail != "admin@example.test" || rev.Summary != "Section 2 removed" {
		t.Errorf("latest revision = %+v, want 3 Section 2 removed by admin@example.test", rev)
	}
	if got := sectionStarts(h.mustSource(t, h.owner, id)); !slices.Equal(got, []string{"Renamed@1"}) {
		t.Errorf("sections = %q, want Renamed@1", got)
	}

	h.insertAttempt(t, id, "Ann")
	dump = h.examDump(t, id)
	for i, c := range calls {
		if err := c.do(h.owner, 3); !errors.Is(err, exam.ErrFrozen) {
			t.Errorf("frozen %s (%d): err = %v, want ErrFrozen", c.name, i, err)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("frozen saves stored something:\n%s", got)
	}
}

func TestSaveSection_NoChangeStoresNoRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))
	before := h.examDump(t, id)

	mustNil(t, "SaveSection", h.svc.SaveSection(ctx, h.owner, id, 1, 1, exam.SectionSource{Title: "Part A", Text: "Read **carefully**."}))
	mustNil(t, "SaveSection trailing space", h.svc.SaveSection(ctx, h.owner, id, 1, 1,
		exam.SectionSource{Title: "Part A  ", Text: "Read **carefully**.  \r\n"}))
	mustNil(t, "SaveSection 2", h.svc.SaveSection(ctx, h.owner, id, 1, 2, exam.SectionSource{Title: "Part B"}))
	if after := h.examDump(t, id); after != before {
		t.Errorf("no-change saves stored something:\n%s\nwant\n%s", after, before)
	}
}

func TestSaveSection_Image(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(sectionedExam))
	key := h.mediaKey(t, id)
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})
	name := imageName(img)

	mustNil(t, "SaveSection", h.svc.SaveSection(ctx, h.owner, id, 1, 1,
		exam.SectionSource{Title: "Part A", Text: "Read **carefully**.", Image: img}))
	if got, want := h.mediaEntries(t), []string{key + "/", key + "/" + name}; !slices.Equal(got, want) {
		t.Errorf("media entries = %q, want %q", got, want)
	}
	if got := h.mustSource(t, h.owner, id).Sections[0].Text; got != "Read **carefully**.\n![]("+name+")" {
		t.Errorf("section 1 text = %q, want it to end with a line ![](%s)", got, name)
	}
	if got, want := h.revisionImages(t, id), map[string]string{name: name}; !reflect.DeepEqual(got, want) {
		t.Errorf("content.images = %q, want %q", got, want)
	}
	if html := string(h.mustQuestions(t, id)[0].Section.BodyHTML); !strings.Contains(html, "/media/"+key+"/"+name) {
		t.Errorf("section 1 HTML = %q, want /media/%s/%s", html, key, name)
	}
	if got := h.latestSummary(t, id); got != "Section 1 edited" {
		t.Errorf("summary = %q", got)
	}
	requireLiveMatchesRevision(t, h, id)

	dump, media := h.examDump(t, id), h.mediaEntries(t)
	err := h.svc.SaveSection(ctx, h.owner, id, 2, 2,
		exam.SectionSource{Title: "Part B", Text: "pic\n\n<div>", Image: pngBytes(t, color.RGBA{9, 8, 7, 255})})
	requireFileError(t, "hidden image line", err, hiddenImageMsg)
	if got := h.examDump(t, id); got != dump {
		t.Errorf("refused save stored something:\n%s", got)
	}
	if got := h.mediaEntries(t); !slices.Equal(got, media) {
		t.Errorf("media entries = %q, want %q", got, media)
	}
}
