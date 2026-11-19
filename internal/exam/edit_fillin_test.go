package exam_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// fillIn builds a fill-in QuestionSource with the accepted answers answers.
func fillIn(text string, answers ...string) exam.QuestionSource {
	return exam.QuestionSource{Text: text, Answers: answers}
}

// requireSourceMatchesLive fails unless Source, the live questions and the
// latest revision of examID agree on every question's kind, options and
// accepted answers.
func requireSourceMatchesLive(t *testing.T, h *harness, examID int64) {
	t.Helper()
	requireAnswersMatchRevision(t, h, examID)
	src := h.mustSource(t, h.owner, examID)
	live := h.mustQuestions(t, examID)
	if len(src.Questions) != len(live) {
		t.Fatalf("Source has %d questions, live %d", len(src.Questions), len(live))
	}
	for i, q := range src.Questions {
		if !reflect.DeepEqual(q.Answers, live[i].Answers) {
			t.Errorf("question %d: Source answers = %q, live %q", i+1, q.Answers, live[i].Answers)
		}
		if len(q.Options) != len(live[i].Options) {
			t.Errorf("question %d: Source has %d options, live %d", i+1, len(q.Options), len(live[i].Options))
		}
	}
}

func TestSource_FillIn(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(mixedExam))

	src := h.mustSource(t, h.owner, id)
	if len(src.Questions) != 2 {
		t.Fatalf("questions = %+v, want 2", src.Questions)
	}
	if q := src.Questions[0]; q.Answers != nil || !reflect.DeepEqual(q.Options, question("Pick one", 0, "a", "b").Options) {
		t.Errorf("question 1 = %+v, want multiple choice a*, b", q)
	}
	if q := src.Questions[1]; q.Text != "Ibu kota ...." || !reflect.DeepEqual(q.Answers, jakarta) || len(q.Options) != 0 {
		t.Errorf("question 2 = %+v, want fill-in with answers %q", q, jakarta)
	}
}

func TestCreate_FillInQuestion(t *testing.T) {
	h := newHarness(t)
	id, err := h.svc.Create(context.Background(), h.owner, defaultSettings, fillIn("Ibu kota?", "Jakarta", "DKI Jakarta"))
	mustNil(t, "Create", err)

	revs := h.mustRevisions(t, id)
	if len(revs) != 1 || revs[0].Number != 1 || revs[0].Summary != "Created in the editor" {
		t.Errorf("revisions = %+v, want revision 1 created in the editor", revs)
	}
	if got, want := h.storedAnswers(t, id), []string{`["Jakarta","DKI Jakarta"]`}; !reflect.DeepEqual(got, want) {
		t.Errorf("accepted_answers = %q, want %q", got, want)
	}
	var options int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM question_option o JOIN question q ON q.id = o.question_id
		WHERE q.exam_id = ?`, id).Scan(&options); err != nil || options != 0 {
		t.Errorf("options = %d, %v; want 0", options, err)
	}
	requireSourceMatchesLive(t, h, id)
}

func TestSaveQuestion_FillInSummary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(mixedExam))
	steps := []struct {
		name     string
		position int
		q        exam.QuestionSource
		summary  string
	}{
		{"add an accepted answer", 2, fillIn("Ibu kota ....", "Jakarta", "DKI Jakarta", "Batavia"),
			"Question 2 edited; accepted answers changed"},
		{"change one answer's text", 2, fillIn("Ibu kota ....", "Jakarta", "DKI Jakarta", "Betawi"),
			"Question 2 edited; accepted answers changed"},
		{"reorder the answers", 2, fillIn("Ibu kota ....", "Betawi", "Jakarta", "DKI Jakarta"),
			"Question 2 edited"},
		{"change only the question text", 2, fillIn("Ibu kota negara ....", "Betawi", "Jakarta", "DKI Jakarta"),
			"Question 2 edited"},
		{"multiple choice, move the mark", 1, question("Pick one", 1, "a", "b"),
			"Question 1 edited; correct option changed"},
		{"multiple choice to fill-in", 1, fillIn("Pick one", "b"),
			"Question 1 edited; accepted answers changed"},
		{"fill-in to multiple choice", 2, question("Ibu kota negara ....", 0, "Jakarta", "Bandung"),
			"Question 2 edited; accepted answers changed"},
	}
	for _, s := range steps {
		base := h.current(t, id)
		mustNil(t, s.name, h.svc.SaveQuestion(ctx, h.owner, id, base, s.position, s.q))
		if n := h.current(t, id); n != base+1 {
			t.Errorf("%s: revision = %d, want %d", s.name, n, base+1)
		}
		if got := h.latestSummary(t, id); got != s.summary {
			t.Errorf("%s: summary = %q, want %q", s.name, got, s.summary)
		}
		if got := h.mustSource(t, h.owner, id).Questions[s.position-1].Answers; !reflect.DeepEqual(got, s.q.Answers) {
			t.Errorf("%s: Source answers = %q, want %q", s.name, got, s.q.Answers)
		}
		requireSourceMatchesLive(t, h, id)
	}
}

func TestAddQuestion_FillIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(mixedExam))
	five := []string{"5", "lima"}

	mustNil(t, "AddQuestion", h.svc.AddQuestion(ctx, h.owner, id, 1, fillIn("Hitung 2 + 3", five...)))
	if got := h.latestSummary(t, id); got != "Question 3 added" {
		t.Errorf("summary = %q, want Question 3 added", got)
	}
	requireSourceMatchesLive(t, h, id)

	mustNil(t, "MoveQuestion", h.svc.MoveQuestion(ctx, h.owner, id, 2, 3, 1))
	if got, want := answersOf(h.mustQuestions(t, id)), [][]string{five, nil, jakarta}; !reflect.DeepEqual(got, want) {
		t.Errorf("after move: answers = %q, want %q", got, want)
	}
	requireSourceMatchesLive(t, h, id)

	mustNil(t, "RemoveQuestion", h.svc.RemoveQuestion(ctx, h.owner, id, 3, 1))
	if got, want := answersOf(h.mustQuestions(t, id)), [][]string{nil, jakarta}; !reflect.DeepEqual(got, want) {
		t.Errorf("after remove: answers = %q, want %q", got, want)
	}
	requireSourceMatchesLive(t, h, id)
}

func TestEdit_FillInRefusals(t *testing.T) {
	ctx := context.Background()
	saveQ2 := func(q exam.QuestionSource) func(*harness, int64) error {
		return func(h *harness, id int64) error { return h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, q) }
	}
	var eleven []string
	for i := range 11 {
		eleven = append(eleven, fmt.Sprint("a", i))
	}
	both := fillIn("Ibu kota ....", "Jakarta")
	both.Options = question("", 0, "Jakarta", "Bandung").Options
	cases := []struct {
		name string
		do   func(*harness, int64) error
		want string
	}{
		{"empty answer on create", func(h *harness, _ int64) error {
			_, err := h.svc.Create(ctx, h.owner, defaultSettings, fillIn("Ibu kota?", "Jakarta", " "))
			return err
		}, "question 1, accepted answer 2 has no text"},
		{"empty answer", saveQ2(fillIn("Ibu kota ....", "Jakarta", "")), "question 2, accepted answer 2 has no text"},
		{"eleven answers", saveQ2(fillIn("Ibu kota ....", eleven...)), "question 2 has more than 10 accepted answers"},
		{"201-rune answer", saveQ2(fillIn("Ibu kota ....", strings.Repeat("é", 201))),
			"question 2, accepted answer 1 is longer than 200 characters"},
		{"answer with a line break", saveQ2(fillIn("Ibu kota ....", "Jakarta\nraya")), "an accepted answer must be one line of text"},
		{"answer with a carriage return", saveQ2(fillIn("Ibu kota ....", "Jakarta\r")), "an accepted answer must be one line of text"},
		{"options and answers", saveQ2(both),
			`question 2 has both options and accepted answers; use "-" lines or "=" lines, not both`},
		{"neither", saveQ2(exam.QuestionSource{Text: "Ibu kota ...."}),
			`question 2 needs options (lines starting with "-") or accepted answers (lines starting with "=")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			id := h.mustImport(t, []byte(mixedExam))
			dump, media, count := h.examDump(t, id), h.mediaEntries(t), h.examCount(t)
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
			if n := h.examCount(t); n != count {
				t.Errorf("exam count = %d, want %d", n, count)
			}
		})
	}
}

// answerLikeText is a question text whose last line reads like an accepted
// answer.
const answerLikeText = "Hitung:\n2 + 3\n= 5"

func TestEdit_AnswerLikeLineStaysText(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(editExam))

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(context.Background(), h.owner, id, 1, 1, question(answerLikeText, 0, "5", "6")))
	q := h.mustSource(t, h.owner, id).Questions[0]
	if q.Text != answerLikeText || q.Answers != nil || len(q.Options) != 2 {
		t.Errorf("stored question = %+v, want multiple choice with text %q", q, answerLikeText)
	}
	requireSourceMatchesLive(t, h, id)
	requireRoundTrip(t, h, id)
}

func TestEdit_StoredAnswerLikeLineKeepsSaving(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte("# T\n\n1. Hitung:\n        2 + 3\n        = 5\n- a *\n- b\n\n2. Other\n- x *\n- y\n"))
	requireFirst := func(what string) {
		t.Helper()
		q := h.mustSource(t, h.owner, id).Questions[0]
		if q.Text != answerLikeText || q.Answers != nil || len(q.Options) != 2 {
			t.Errorf("%s: question 1 = %+v, want multiple choice with text %q", what, q, answerLikeText)
		}
	}
	requireFirst("import")

	set := defaultSettings
	set.Title = "Renamed"
	mustNil(t, "SaveSettings", h.svc.SaveSettings(ctx, h.owner, id, 1, set))
	requireFirst("SaveSettings")
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 2, 2, question("Other edited", 1, "x", "y")))
	requireFirst("SaveQuestion")
	if n := h.current(t, id); n != 3 {
		t.Errorf("revision = %d, want 3", n)
	}
	requireSourceMatchesLive(t, h, id)
	requireRoundTrip(t, h, id)
}

func TestEdit_FillInNoChangeStoresNoRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(mixedExam))
	before := h.examDump(t, id)

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, fillIn("Ibu kota ....", jakarta...)))
	mustNil(t, "SaveQuestion spaces", h.svc.SaveQuestion(ctx, h.owner, id, 1, 2, fillIn("Ibu kota ....  ", "  Jakarta", "DKI Jakarta \t")))
	if after := h.examDump(t, id); after != before {
		t.Errorf("no-change saves stored something:\n%s\nwant\n%s", after, before)
	}
}

func TestEdit_FillInStoresParserText(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(mixedExam))

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(context.Background(), h.owner, id, 1, 2, fillIn("Ibu kota ....", "  Jakarta  ")))
	if got := h.mustSource(t, h.owner, id).Questions[1].Answers; !reflect.DeepEqual(got, []string{"Jakarta"}) {
		t.Errorf("Source answers = %q, want [Jakarta]", got)
	}
	if got, want := h.storedAnswers(t, id), []string{"NULL", `["Jakarta"]`}; !reflect.DeepEqual(got, want) {
		t.Errorf("accepted_answers = %q, want %q", got, want)
	}
	requireSourceMatchesLive(t, h, id)
}
