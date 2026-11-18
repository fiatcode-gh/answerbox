package exam_test

import (
	"context"
	"encoding/json"
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

// editExam has the default settings and two questions; question 2's correct
// option is the second one.
const editExam = "# Base\n\n1. First\n- a *\n- b\n\n2. Second\n- c\n- d *\n- e\n"

var defaultSettings = exam.Settings{
	Title:           "Base",
	Shuffle:         examfile.ShuffleQuestions,
	ShowResult:      examfile.ShowScore,
	Monitor:         true,
	DurationMinutes: 60,
}

// question builds a QuestionSource whose option at index correct (none for
// -1) is the correct one.
func question(text string, correct int, opts ...string) exam.QuestionSource {
	q := exam.QuestionSource{Text: text}
	for i, o := range opts {
		q.Options = append(q.Options, exam.OptionSource{Text: o, Correct: i == correct})
	}
	return q
}

func (h *harness) mustSource(t *testing.T, actor exam.Actor, id int64) exam.Source {
	t.Helper()
	src, err := h.svc.Source(context.Background(), actor, id)
	if err != nil {
		t.Fatalf("Source(%d): %v", id, err)
	}
	return src
}

// current returns the number of examID's current revision.
func (h *harness) current(t *testing.T, id int64) int {
	t.Helper()
	return h.mustGet(t, id).Revision
}

func (h *harness) latestSummary(t *testing.T, id int64) string {
	t.Helper()
	return h.mustRevisions(t, id)[0].Summary
}

func questionTexts(src exam.Source) []string {
	var out []string
	for _, q := range src.Questions {
		out = append(out, q.Text)
	}
	return out
}

func mustNil(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

type editCall struct {
	name string
	do   func(actor exam.Actor, base int) error
}

// editCalls returns one changing call of every edit method on an exam
// imported from editExam.
func (h *harness) editCalls(id int64) []editCall {
	ctx := context.Background()
	other := defaultSettings
	other.Title = "Other"
	return []editCall{
		{"SaveQuestion", func(a exam.Actor, base int) error {
			return h.svc.SaveQuestion(ctx, a, id, base, 1, question("Changed", 0, "a", "b"))
		}},
		{"AddQuestion", func(a exam.Actor, base int) error {
			return h.svc.AddQuestion(ctx, a, id, base, question("Added", 0, "x", "y"))
		}},
		{"RemoveQuestion", func(a exam.Actor, base int) error { return h.svc.RemoveQuestion(ctx, a, id, base, 1) }},
		{"MoveQuestion", func(a exam.Actor, base int) error { return h.svc.MoveQuestion(ctx, a, id, base, 2, 1) }},
		{"SaveSettings", func(a exam.Actor, base int) error { return h.svc.SaveSettings(ctx, a, id, base, other) }},
	}
}

func TestSource_CurrentRevisionText(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(formattedExam))

	want := exam.Source{
		Revision: 1,
		Settings: exam.Settings{
			Title:           "Formatting & Co",
			Shuffle:         examfile.ShuffleNone,
			ShowResult:      examfile.ShowReview,
			Monitor:         false,
			DurationMinutes: 25,
		},
		Questions: []exam.QuestionSource{
			{
				Text: "A **bold** start with `code span`\nand a second line.\n\n```\nfenced\n  block\n```",
				Options: []exam.OptionSource{
					{Text: "*plain* option"},
					{Text: "`x < y`", Correct: true},
				},
			},
			question("Second", 1, "one", "two", "three"),
		},
	}
	if got := h.mustSource(t, h.owner, id); !reflect.DeepEqual(got, want) {
		t.Errorf("Source =\n%+v\nwant\n%+v", got, want)
	}

	if _, err := h.svc.Source(ctx, h.teacher(t, "other@example.test"), id); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("foreign Source: err = %v, want ErrNotFound", err)
	}
	stored := h.insertStored(t, storedExam{title: "Old", key: "oldkey", code: "ABCDEF", owner: h.owner.TeacherID,
		createdAt: fixedNow, questions: paragraphs(1)})
	if _, err := h.svc.Source(ctx, h.owner, stored); !errors.Is(err, exam.ErrNoRevision) {
		t.Errorf("Source without revision: err = %v, want ErrNoRevision", err)
	}
	h.insertAttempt(t, id, "Ann")
	if _, err := h.svc.Source(ctx, h.owner, id); !errors.Is(err, exam.ErrFrozen) {
		t.Errorf("Source with attempt: err = %v, want ErrFrozen", err)
	}
}

func TestCreate_NewExamRevision1(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	set := exam.Settings{Title: "  New\r\n", Shuffle: examfile.ShuffleNone, ShowResult: examfile.ShowReview, DurationMinutes: 30}
	admin := h.admin(t)
	for _, actor := range []exam.Actor{h.owner, admin} {
		id, err := h.svc.Create(ctx, actor, set, question("What?", 1, "x", "y"))
		mustNil(t, "Create", err)
		ex, err := h.svc.Get(ctx, actor, id)
		mustNil(t, "Get", err)
		if ex.Open || ex.OwnerID != actor.TeacherID || !examCode.MatchString(ex.Code) {
			t.Errorf("created exam = %+v, want closed, owned by %d, with a code", ex, actor.TeacherID)
		}
		if ex.Title != "New" || ex.Shuffle != examfile.ShuffleNone || ex.ShowResult != examfile.ShowReview || ex.Monitor || ex.DurationMinutes != 30 {
			t.Errorf("created exam settings = %+v", ex)
		}
		revs := h.mustRevisions(t, id)
		email := ex.OwnerEmail
		if len(revs) != 1 || revs[0].Number != 1 || revs[0].Summary != "Created in the editor" ||
			revs[0].TeacherEmail != email || !revs[0].CreatedAt.Equal(fixedNow) {
			t.Errorf("revisions = %+v, want revision 1 created in the editor by %s at %v", revs, email, fixedNow)
		}
		if actor == h.owner { // the helper reads through the owner's scope
			requireLiveMatchesRevision(t, h, id)
		}
	}
	if entries := h.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries = %v, want none", entries)
	}
}

func TestSaveQuestion_WritesNextRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	key := h.mediaKey(t, id)
	first := h.mustQuestions(t, id)[0].BodyHTML

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, question("Second edited", 1, "c", "d", "e")))
	if got := h.latestSummary(t, id); got != "Question 2 edited" {
		t.Errorf("summary = %q, want Question 2 edited", got)
	}
	if got := revisionNumbers(h.mustRevisions(t, id)); !slices.Equal(got, []int{2, 1}) {
		t.Errorf("revision numbers = %v, want [2 1]", got)
	}
	requireLiveMatchesRevision(t, h, id)

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 2, 2, question("Second edited", 0, "c", "d", "e")))
	if got := h.latestSummary(t, id); got != "Question 2 edited; correct option changed" {
		t.Errorf("summary = %q, want the correct-option suffix", got)
	}
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 3, 2, question("Second edited", 1, "d", "c", "e")))
	if got := h.latestSummary(t, id); got != "Question 2 edited" {
		t.Errorf("summary after reordering = %q, want Question 2 edited", got)
	}
	if got := h.mustQuestions(t, id)[0].BodyHTML; got != first {
		t.Errorf("question 1 HTML = %q, want %q", got, first)
	}
	if got := h.mediaKey(t, id); got != key {
		t.Errorf("media_key = %q, want %q", got, key)
	}
	if got := questionTexts(h.mustSource(t, h.owner, id)); !slices.Equal(got, []string{"First", "Second edited"}) {
		t.Errorf("questions = %q", got)
	}

	zipData, _ := imageExam(t, "Pictures", color.Black)
	img := h.mustImport(t, zipData)
	before := h.mediaEntries(t)
	imgKey := h.mediaKey(t, img)
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, img, 1, 1, question("Q edited ![x](i.png)", 0, "a", "b")))
	if after := h.mediaEntries(t); !slices.Equal(after, before) {
		t.Errorf("media entries = %v, want %v", after, before)
	}
	if got := h.mediaKey(t, img); got != imgKey {
		t.Errorf("media_key = %q, want %q", got, imgKey)
	}
	if html := string(h.mustQuestions(t, img)[0].BodyHTML); !strings.Contains(html, "/media/"+imgKey+"/") {
		t.Errorf("question HTML = %q, want the image under %s", html, imgKey)
	}
	requireLiveMatchesRevision(t, h, img)
}

func TestAddRemoveMoveQuestion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	steps := []struct {
		do      func(base int) error
		summary string
		texts   []string
	}{
		{func(base int) error { return h.svc.AddQuestion(ctx, h.owner, id, base, question("Third", 0, "x", "y")) },
			"Question 3 added", []string{"First", "Second", "Third"}},
		{func(base int) error { return h.svc.MoveQuestion(ctx, h.owner, id, base, 3, 1) },
			"Question 3 moved to 1", []string{"Third", "First", "Second"}},
		{func(base int) error { return h.svc.RemoveQuestion(ctx, h.owner, id, base, 1) },
			"Question 1 removed", []string{"First", "Second"}},
		{func(base int) error { return h.svc.MoveQuestion(ctx, h.owner, id, base, 1, 2) },
			"Question 1 moved to 2", []string{"Second", "First"}},
	}
	for _, s := range steps {
		mustNil(t, s.summary, s.do(h.current(t, id)))
		if got := h.latestSummary(t, id); got != s.summary {
			t.Errorf("summary = %q, want %q", got, s.summary)
		}
		if got := questionTexts(h.mustSource(t, h.owner, id)); !slices.Equal(got, s.texts) {
			t.Errorf("after %q questions = %q, want %q", s.summary, got, s.texts)
		}
		requireLiveMatchesRevision(t, h, id)
	}
}

func TestSaveSettings_Summary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))

	set := defaultSettings
	set.Title, set.DurationMinutes = "Renamed", 30
	mustNil(t, "SaveSettings", h.svc.SaveSettings(ctx, h.owner, id, 1, set))
	if got := h.latestSummary(t, id); got != "Settings changed: title, length" {
		t.Errorf("summary = %q", got)
	}

	all := exam.Settings{Title: "All new", Shuffle: examfile.ShuffleQuestionsOptions, ShowResult: examfile.ShowReview, DurationMinutes: 90}
	mustNil(t, "SaveSettings", h.svc.SaveSettings(ctx, h.owner, id, 2, all))
	if got := h.latestSummary(t, id); got != "Settings changed: title, shuffle, result shown, monitor, length" {
		t.Errorf("summary = %q", got)
	}
	ex := h.mustGet(t, id)
	got := exam.Settings{Title: ex.Title, Shuffle: ex.Shuffle, ShowResult: ex.ShowResult, Monitor: ex.Monitor, DurationMinutes: ex.DurationMinutes}
	if got != all {
		t.Errorf("Get settings = %+v, want %+v", got, all)
	}
	var title string
	var duration int
	if err := h.db.QueryRow(`SELECT title, duration_minutes FROM exam WHERE id = ?`, id).Scan(&title, &duration); err != nil {
		t.Fatal(err)
	}
	if title != "All new" || duration != 90 {
		t.Errorf("exam row = %q, %d", title, duration)
	}
	if src := h.mustSource(t, h.owner, id); src.Settings != all || src.Revision != 3 {
		t.Errorf("Source = %+v, want settings %+v at revision 3", src, all)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestEdit_NoChangeStoresNoRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	before := h.examDump(t, id)

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, question("Second", 1, "c", "d", "e")))
	mustNil(t, "SaveQuestion trailing space", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, question("Second  \r\n", 1, "c ", "d", "e")))
	mustNil(t, "SaveSettings", h.svc.SaveSettings(ctx, h.owner, id, 1, defaultSettings))
	mustNil(t, "MoveQuestion", h.svc.MoveQuestion(ctx, h.owner, id, 1, 2, 2))
	if after := h.examDump(t, id); after != before {
		t.Errorf("no-change saves stored something:\n%s\nwant\n%s", after, before)
	}
}

func TestEdit_StaleBaseRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zipData, _ := imageExam(t, "Pictures", color.Black)
	h.mustImport(t, zipData)
	id := h.mustImport(t, []byte(editExam))
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, question("Second edited", 1, "c", "d", "e")))
	dump, media := h.examDump(t, id), h.mediaEntries(t)

	for _, c := range h.editCalls(id) {
		for _, base := range []int{0, 1, 3} {
			if err := c.do(h.owner, base); !errors.Is(err, exam.ErrStale) {
				t.Errorf("%s(base %d): err = %v, want ErrStale", c.name, base, err)
			}
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("stale saves stored something:\n%s", got)
	}
	if got := h.mediaEntries(t); !slices.Equal(got, media) {
		t.Errorf("media entries = %v, want %v", got, media)
	}
}

func TestReplaceContent_StaleInsideTransaction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, question("Second edited", 1, "c", "d", "e")))
	key := h.mediaKey(t, id)
	ex, err := examfile.Parse(examfile.Upload{Text: []byte(simpleExam), Dir: "."}, "/media/"+key)
	mustNil(t, "Parse", err)
	dump := h.examDump(t, id)

	if err := h.svc.ReplaceContent(ctx, h.owner, id, 1, "Stale", ex, key); !errors.Is(err, exam.ErrStale) {
		t.Errorf("ReplaceContent(base 1): err = %v, want ErrStale", err)
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("stale ReplaceContent stored something:\n%s", got)
	}
	mustNil(t, "ReplaceContent(base 0)", h.svc.ReplaceContent(ctx, h.owner, id, 0, "Unchecked", ex, key))
	if revs := h.mustRevisions(t, id); revs[0].Number != 3 || revs[0].Summary != "Unchecked" {
		t.Errorf("latest revision = %+v, want 3 Unchecked", revs[0])
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestEdit_FrozenRefused(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(editExam))
	h.insertAttempt(t, id, "Ann")
	dump, media := h.examDump(t, id), h.mediaEntries(t)

	for _, c := range h.editCalls(id) {
		if err := c.do(h.owner, 1); !errors.Is(err, exam.ErrFrozen) {
			t.Errorf("%s: err = %v, want ErrFrozen", c.name, err)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("frozen saves stored something:\n%s", got)
	}
	if got := h.mediaEntries(t); !slices.Equal(got, media) {
		t.Errorf("media entries = %v, want %v", got, media)
	}

	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO exam_revision (exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
			SELECT exam_id, 2, created_at, teacher_id, 'Sneaky', title, shuffle, show_result, monitor, duration_minutes, media_key, content
			FROM exam_revision WHERE exam_id = ? AND number = 1`, []any{id}},
		{`UPDATE question SET body_html = '<p>x</p>' WHERE exam_id = ?`, []any{id}},
		{`UPDATE exam SET title = 'Sneaky' WHERE id = ?`, []any{id}},
	} {
		if _, err := h.db.Exec(stmt.sql, stmt.args...); err == nil {
			t.Errorf("schema allowed on a frozen exam: %s", stmt.sql)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("direct SQL changed a frozen exam:\n%s", got)
	}
}

func TestEdit_Scope(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(editExam))
	dump := h.examDump(t, id)
	other := h.teacher(t, "other@example.test")
	for _, c := range h.editCalls(id) {
		if err := c.do(other, 1); !errors.Is(err, exam.ErrNotFound) {
			t.Errorf("foreign %s: err = %v, want ErrNotFound", c.name, err)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("foreign saves stored something:\n%s", got)
	}

	admin := h.admin(t)
	mustNil(t, "admin SaveQuestion", h.svc.SaveQuestion(context.Background(), admin, id, 1, 1, question("By admin", 0, "a", "b")))
	if rev := h.mustRevisions(t, id)[0]; rev.Number != 2 || rev.TeacherEmail != "admin@example.test" {
		t.Errorf("latest revision = %+v, want 2 by admin@example.test", rev)
	}
	if ex := h.mustGet(t, id); ex.OwnerID != h.owner.TeacherID {
		t.Errorf("owner = %d, want %d", ex.OwnerID, h.owner.TeacherID)
	}
}

func TestEdit_UnknownQuestion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	dump := h.examDump(t, id)
	q := question("Changed", 0, "a", "b")
	calls := map[string]error{
		"SaveQuestion 0":   h.svc.SaveQuestion(ctx, h.owner, id, 1, 0, q),
		"SaveQuestion 3":   h.svc.SaveQuestion(ctx, h.owner, id, 1, 3, q),
		"RemoveQuestion 0": h.svc.RemoveQuestion(ctx, h.owner, id, 1, 0),
		"RemoveQuestion 3": h.svc.RemoveQuestion(ctx, h.owner, id, 1, 3),
		"MoveQuestion 0,1": h.svc.MoveQuestion(ctx, h.owner, id, 1, 0, 1),
		"MoveQuestion 3,1": h.svc.MoveQuestion(ctx, h.owner, id, 1, 3, 1),
		"MoveQuestion 1,0": h.svc.MoveQuestion(ctx, h.owner, id, 1, 1, 0),
		"MoveQuestion 1,3": h.svc.MoveQuestion(ctx, h.owner, id, 1, 1, 3),
	}
	for _, name := range slices.Sorted(maps.Keys(calls)) {
		if err := calls[name]; !errors.Is(err, exam.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	if got := h.examDump(t, id); got != dump {
		t.Errorf("unknown question stored something:\n%s", got)
	}
}

// manyQuestions is an exam text with n two-option questions.
func manyQuestions(n int) string {
	var b strings.Builder
	b.WriteString("# Many\n")
	for i := range n {
		fmt.Fprintf(&b, "\n%d. Q%d\n- a *\n- b\n", i+1, i+1)
	}
	return b.String()
}

const shapeMessage = `question 2: the text cannot be saved as written: a line that starts with "-" would become an option, ` +
	`and an option that ends with "*" would become the correct one; write \- or \* instead`

func TestEdit_Refusals(t *testing.T) {
	ctx := context.Background()
	saveQ2 := func(q exam.QuestionSource) func(*harness, int64) error {
		return func(h *harness, id int64) error { return h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, q) }
	}
	settings := func(change func(*exam.Settings)) func(*harness, int64) error {
		return func(h *harness, id int64) error {
			set := defaultSettings
			change(&set)
			return h.svc.SaveSettings(ctx, h.owner, id, 1, set)
		}
	}
	eleven := question("Second", 0)
	for i := range 11 {
		eleven.Options = append(eleven.Options, exam.OptionSource{Text: fmt.Sprint("o", i), Correct: i == 0})
	}
	const duration = "duration must be a whole number of minutes from 5 to 240"
	cases := []struct {
		name string
		text string // exam to edit; editExam when empty
		do   func(*harness, int64) error
		want string
	}{
		{"empty question text", "", saveQ2(question("  ", 1, "c", "d", "e")), "question 2 has no text"},
		{"empty option", "", saveQ2(question("Second", 0, "c", "", "e")), "question 2, option 2 has no text"},
		{"one option", "", saveQ2(question("Second", 0, "c")), "question 2 needs at least two options"},
		{"eleven options", "", saveQ2(eleven), "question 2 has more than 10 options"},
		{"no correct option", "", saveQ2(question("Second", -1, "c", "d")), "question 2 has no correct option"},
		{"two correct options", "", saveQ2(exam.QuestionSource{Text: "Second", Options: []exam.OptionSource{{"c", true}, {"d", true}}}),
			"question 2 has more than one correct option"},
		{"501st question", manyQuestions(500), func(h *harness, id int64) error {
			return h.svc.AddQuestion(ctx, h.owner, id, 1, question("One too many", 0, "a", "b"))
		}, "exam has more than 500 questions"},
		{"only question removed", simpleExam, func(h *harness, id int64) error {
			return h.svc.RemoveQuestion(ctx, h.owner, id, 1, 1)
		}, "exam has no questions"},
		{"long title", "", settings(func(s *exam.Settings) { s.Title = strings.Repeat("é", 201) }), "title is longer than 200 characters"},
		{"empty title", "", settings(func(s *exam.Settings) { s.Title = " \r\n " }), "the exam needs a title"},
		{"title with a line break", "", settings(func(s *exam.Settings) { s.Title = "One\r\nTwo" }), "the title must be one line of text"},
		{"missing image", "", saveQ2(question("See ![](gone.png)", 1, "c", "d")), `question 2: image "gone.png" not found in upload`},
		{"dash line in question", "", saveQ2(question("Which?\n- not this", 1, "c", "d")), shapeMessage},
		{"unclosed fence", "", saveQ2(question("Code:\n```\nx", 1, "c", "d")), "code block is never closed"},
		{"inline code with three backticks on a first line", "", saveQ2(question("Second", 0, "```x```", "d")), "code block is never closed"},
		{"duration 4", "", settings(func(s *exam.Settings) { s.DurationMinutes = 4 }), duration},
		{"duration 241", "", settings(func(s *exam.Settings) { s.DurationMinutes = 241 }), duration},
		{"duration 0", "", settings(func(s *exam.Settings) { s.DurationMinutes = 0 }), duration},
		{"shuffle bogus", "", settings(func(s *exam.Settings) { s.Shuffle = "bogus" }), `shuffle must be "none", "questions" or "questions, options"`},
		{"invalid UTF-8", "", saveQ2(question("Bad \xff byte", 1, "c", "d")), "exam text is not valid UTF-8"},
		{"text over 1 MB", "", saveQ2(question(strings.Repeat("a", examfile.MaxTextBytes), 1, "c", "d")), "exam text is larger than 1 MB"},
		{"text moved between options", "", saveQ2(question("Second", 0, "a\n```", "b\n```\n- x")), shapeMessage},
		{"imported fence on a first line", "# Fences\n\n1. First\n- a *\n- b\n\n2. Second\n- ```go\nx\n```\n```\n- d *\n",
			saveQ2(question("Second", 1, "```go\nx\n```\n```", "d")), "code block is never closed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			text := c.text
			if text == "" {
				text = editExam
			}
			id := h.mustImport(t, []byte(text))
			dump, media := h.examDump(t, id), h.mediaEntries(t)
			err := c.do(h, id)
			var fileErr *examfile.Error
			if !errors.As(err, &fileErr) {
				t.Fatalf("err = %T %v, want *examfile.Error", err, err)
			}
			if fileErr.Msg != c.want {
				t.Errorf("message = %q, want %q", fileErr.Msg, c.want)
			}
			if got := h.examDump(t, id); got != dump {
				t.Errorf("refused save stored something:\n%s", got)
			}
			if got := h.mediaEntries(t); !slices.Equal(got, media) {
				t.Errorf("media entries = %v, want %v", got, media)
			}
		})
	}
}

func TestCreate_RefusedStoresNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	good := question("What?", 0, "x", "y")
	titled := defaultSettings
	untitled := defaultSettings
	untitled.Title = "  "
	short := defaultSettings
	short.DurationMinutes = 4
	cases := []struct {
		set  exam.Settings
		q    exam.QuestionSource
		want string
	}{
		{titled, question("", 0, "x", "y"), "question 1 has no text"},
		{untitled, good, "the exam needs a title"},
		{short, good, "duration must be a whole number of minutes from 5 to 240"},
	}
	for _, c := range cases {
		_, err := h.svc.Create(ctx, h.owner, c.set, c.q)
		var fileErr *examfile.Error
		if !errors.As(err, &fileErr) || fileErr.Msg != c.want {
			t.Errorf("Create: err = %v, want %q", err, c.want)
		}
	}
	if n := h.examCount(t); n != 0 {
		t.Errorf("exam count = %d, want 0", n)
	}
	if entries := h.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries = %v, want none", entries)
	}
}

func TestEdit_StoresParserText(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, question("line1\r\n  line2  \r\n", 1, " b ", "a\r\nmore")))
	want := question("line1\nline2", 1, "b", "a\nmore")
	if got := h.mustSource(t, h.owner, id).Questions[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("stored question = %+v, want %+v", got, want)
	}

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 2, 2, question("Sum?", 0, "5 *", "6")))
	want = question("Sum?", 0, "5 *", "6")
	if got := h.mustSource(t, h.owner, id).Questions[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("stored question = %+v, want %+v", got, want)
	}
	requireLiveMatchesRevision(t, h, id)

	// A block typed with a code fence on its first line is stored with an
	// empty first line, the only place the file format reads a fence.
	mustNil(t, "SaveQuestion fence", h.svc.SaveQuestion(ctx, h.owner, id, 3, 2,
		question("```\r\nprint(1)\r\n```\r\nWhat?", 0, "```go\r\nx := 1\r\n```", "b")))
	want = question("\n```\nprint(1)\n```\nWhat?", 0, "\n```go\nx := 1\n```", "b")
	if got := h.mustSource(t, h.owner, id).Questions[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("stored question = %+v, want %+v", got, want)
	}
	if got, wantHTML := string(h.mustQuestions(t, id)[1].Options[0].BodyHTML), "<pre><code class=\"language-go\">x := 1\n</code></pre>\n"; got != wantHTML {
		t.Errorf("option 1 HTML = %q, want %q", got, wantHTML)
	}
	mustNil(t, "SaveQuestion stored fence", h.svc.SaveQuestion(ctx, h.owner, id, 4, 2, want))
	if n := h.current(t, id); n != 4 {
		t.Errorf("saving the stored texts again moved the revision to %d, want 4", n)
	}

	mustNil(t, "SaveQuestion correct fence", h.svc.SaveQuestion(ctx, h.owner, id, 4, 2, question("Pick", 1, "```go\nx := 1\n```", "b")))
	want = question("Pick", 1, "\n```go\nx := 1\n```", "b")
	if got := h.mustSource(t, h.owner, id).Questions[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("stored question = %+v, want %+v", got, want)
	}

	mustNil(t, "SaveQuestion two fences", h.svc.SaveQuestion(ctx, h.owner, id, 5, 2, question("Pick", 0, "```\na\n```", "```\nb\n```")))
	want = question("Pick", 0, "\n```\na\n```", "\n```\nb\n```")
	if got := h.mustSource(t, h.owner, id).Questions[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("stored question = %+v, want %+v", got, want)
	}
	requireLiveMatchesRevision(t, h, id)
	requireRoundTrip(t, h, id)
}

// contentKeys returns the top-level keys of the latest revision's content.
func (h *harness) contentKeys(t *testing.T, id int64) []string {
	t.Helper()
	var doc string
	if err := h.db.QueryRow(`SELECT content FROM exam_revision WHERE exam_id = ? ORDER BY number DESC LIMIT 1`, id).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(m))
}

// requireRoundTrip fails unless id's download imports as an equal exam and
// id can be copied.
func requireRoundTrip(t *testing.T, h *harness, id int64) {
	t.Helper()
	again := h.mustImport(t, h.mustDownload(t, h.owner, id).Data)
	requireEqualExams(t, h, id, again)
	requireSameQuestions(t, h, id, h.mustCopy(t, h.owner, id))
}

func TestEdit_DownloadRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	id, err := h.svc.Create(ctx, h.owner, defaultSettings, question("One", 0, "a", "b"))
	mustNil(t, "Create", err)
	saves := []func(base int) error{
		func(base int) error {
			return h.svc.AddQuestion(ctx, h.owner, id, base, question("Two\n\n```\n1. code\n```", 1, "x", "y *"))
		},
		func(base int) error {
			return h.svc.AddQuestion(ctx, h.owner, id, base, question("Three", 0, "**bold**", "plain"))
		},
		func(base int) error {
			return h.svc.SaveQuestion(ctx, h.owner, id, base, 1, question("One edited\n2. not a question", 1, "a", "b"))
		},
		func(base int) error { return h.svc.MoveQuestion(ctx, h.owner, id, base, 3, 1) },
		func(base int) error { return h.svc.RemoveQuestion(ctx, h.owner, id, base, 2) },
		func(base int) error {
			return h.svc.SaveSettings(ctx, h.owner, id, base, exam.Settings{Title: "Round trip", Shuffle: examfile.ShuffleNone,
				ShowResult: examfile.ShowReview, DurationMinutes: 45})
		},
	}
	for i, save := range saves {
		mustNil(t, fmt.Sprint("save ", i), save(h.current(t, id)))
	}
	if n := h.current(t, id); n != 7 {
		t.Errorf("current revision = %d, want 7", n)
	}
	requireRoundTrip(t, h, id)
	if got := h.contentKeys(t, id); !slices.Equal(got, []string{"dir", "questions"}) {
		t.Errorf("content keys = %q, want dir, questions", got)
	}

	data, _, _ := imageZip(t)
	img := h.mustImport(t, data)
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, img, 1, 1, question("Look again ![a](img/a%20b.png)", 0, "yes", "no")))
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, img, 2, 2,
		question("And ![s](../shared.png) too", 1, "![again](img/a%20b.png)", "other")))
	requireRoundTrip(t, h, img)
	if got := h.contentKeys(t, img); !slices.Equal(got, []string{"dir", "images", "questions"}) {
		t.Errorf("content keys = %q, want dir, images, questions", got)
	}
}

func TestEdit_OlderRevisionsUnchanged(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(editExam))
	revision1 := func() string {
		var b strings.Builder
		rows, err := h.db.Query(`SELECT * FROM exam_revision WHERE exam_id = ? AND number = 1`, id)
		if err != nil {
			t.Fatal(err)
		}
		dumpRows(t, &b, rows)
		return b.String()
	}
	before := revision1()
	for i, c := range h.editCalls(id)[:4] {
		mustNil(t, c.name, c.do(h.owner, i+1))
	}
	if n := h.current(t, id); n != 5 {
		t.Fatalf("current revision = %d, want 5", n)
	}
	if after := revision1(); after != before {
		t.Errorf("revision 1 =\n%s\nwant\n%s", after, before)
	}
}

func TestEdit_OpenExamWithoutAttempts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	mustNil(t, "Open", h.svc.Open(ctx, h.owner, id))
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 1, question("Changed", 0, "a", "b")))
	if !h.mustGet(t, id).Open {
		t.Error("exam closed by an edit")
	}
	h.insertAttempt(t, id, "Ann")
	if got := h.attemptRevisions(t, id); !slices.Equal(got, []int{2}) {
		t.Errorf("attempt revisions = %v, want [2]", got)
	}
}
