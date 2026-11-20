package exam_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

func TestParseSummary_Forms(t *testing.T) {
	allSettings := []string{"title", "shuffle", "result shown", "monitor", "length"}
	for _, c := range []struct {
		stored string
		want   []exam.SummaryClause
	}{
		{"Imported", []exam.SummaryClause{{Kind: exam.SummaryImported}}},
		{"Replaced from file", []exam.SummaryClause{{Kind: exam.SummaryReplaced}}},
		{"Created in the editor", []exam.SummaryClause{{Kind: exam.SummaryCreated}}},
		{"Copied from Maths (ABC234), revision 3", []exam.SummaryClause{{Kind: exam.SummaryCopied, N: 3, Title: "Maths", Code: "ABC234"}}},
		{"Copied from A (b); c, revision 9 (XYZ789), revision 12",
			[]exam.SummaryClause{{Kind: exam.SummaryCopied, N: 12, Title: "A (b); c, revision 9", Code: "XYZ789"}}},
		{"Copied from Two\nlines (ABCDEF), revision 1", []exam.SummaryClause{{Kind: exam.SummaryCopied, N: 1, Title: "Two\nlines", Code: "ABCDEF"}}},
		{"Copied from  (ABCDEF), revision 1", []exam.SummaryClause{{Kind: exam.SummaryCopied, N: 1, Title: "", Code: "ABCDEF"}}},
		{"Question 4 edited", []exam.SummaryClause{{Kind: exam.SummaryQuestionEdited, N: 4}}},
		{"Question 2 edited; correct option changed",
			[]exam.SummaryClause{{Kind: exam.SummaryQuestionEdited, N: 2}, {Kind: exam.SummaryCorrectChanged}}},
		{"Question 12 edited; accepted answers changed",
			[]exam.SummaryClause{{Kind: exam.SummaryQuestionEdited, N: 12}, {Kind: exam.SummaryAnswersChanged}}},
		{"Question 3 added", []exam.SummaryClause{{Kind: exam.SummaryQuestionAdded, N: 3}}},
		{"Question 1 removed", []exam.SummaryClause{{Kind: exam.SummaryQuestionRemoved, N: 1}}},
		{"Question 3 moved to 1", []exam.SummaryClause{{Kind: exam.SummaryQuestionMoved, N: 3, M: 1}}},
		{"Question 3 moved to section 2", []exam.SummaryClause{{Kind: exam.SummaryQuestionSection, N: 3, M: 2}}},
		{"Settings changed: title, length", []exam.SummaryClause{{Kind: exam.SummarySettingsChanged, Settings: []string{"title", "length"}}}},
		{"Settings changed: title, shuffle, result shown, monitor, length",
			[]exam.SummaryClause{{Kind: exam.SummarySettingsChanged, Settings: allSettings}}},
		{"Settings changed: ", []exam.SummaryClause{{Kind: exam.SummarySettingsChanged}}},
		{"Section 2 started at question 5", []exam.SummaryClause{{Kind: exam.SummarySectionStarted, N: 2, M: 5}}},
		{"Section 1 edited", []exam.SummaryClause{{Kind: exam.SummarySectionEdited, N: 1}}},
		{"Section 3 removed", []exam.SummaryClause{{Kind: exam.SummarySectionRemoved, N: 3}}},
	} {
		got, ok := exam.ParseSummary(c.stored)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseSummary(%q) = %+v, %v; want %+v, true", c.stored, got, ok, c.want)
		}
	}

	for _, stored := range []string{
		"",
		"Unchecked",
		"imported",
		"Imported ",
		"Imported; correct option changed",
		"Question x edited",
		"Question -1 edited",
		"Question +1 edited",
		"Question  1 edited",
		"Question 1 edited; something else",
		"Question 1 edited; correct option changed; accepted answers changed",
		"Question 1 added; correct option changed",
		"Question 1 moved to section x",
		"Question 1 moved to",
		"Settings changed: colour",
		"Settings changed: title,length",
		"Settings changed: title, ",
		"Settings changed:",
		"Copied from Maths (abc234), revision 3",
		"Copied from Maths (ABC23), revision 3",
		"Copied from Maths (ABC234) revision 3",
		"Section 1 started at question 2 today",
		"Question 1 removed\n",
		"Replaced from file.",
	} {
		if got, ok := exam.ParseSummary(stored); ok || got != nil {
			t.Errorf("ParseSummary(%q) = %+v, %v; want nil, false", stored, got, ok)
		}
	}
}

// TestParseSummary_EveryWrittenSummary keeps the writers of summaries and
// their reader together: every summary the service writes parses.
func TestParseSummary_EveryWrittenSummary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	latest := func(what string, id int64, want ...exam.SummaryClause) {
		t.Helper()
		requireParses(t, what, h.latestSummary(t, id), want)
	}

	odd := h.mustImport(t, []byte("# A (b); c, revision 9\n\n1. First\n- a *\n- b\n"))
	latest("import", odd, exam.SummaryClause{Kind: exam.SummaryImported})
	copyID := h.mustCopy(t, h.owner, odd)
	latest("copy", copyID, exam.SummaryClause{Kind: exam.SummaryCopied, N: 1, Title: "A (b); c, revision 9", Code: h.storedCode(t, odd)})

	replaced := h.mustImport(t, []byte(editExam))
	mustNil(t, "Replace", h.svc.Replace(ctx, h.owner, replaced, []byte(choiceExam)))
	latest("replace", replaced, exam.SummaryClause{Kind: exam.SummaryReplaced})

	created, err := h.svc.Create(ctx, h.owner, defaultSettings, question("Only", 0, "a", "b"))
	mustNil(t, "Create", err)
	latest("create", created, exam.SummaryClause{Kind: exam.SummaryCreated})

	stored := h.insertStored(t, storedExam{title: "Old", key: "k1", code: "ABCDEF", owner: h.owner.TeacherID, createdAt: fixedNow, questions: paragraphs(2)})
	h.mustBackfill(t)
	latest("backfill", stored, exam.SummaryClause{Kind: exam.SummaryImported})

	plain := h.mustImport(t, []byte(editExam))
	mixed := h.mustImport(t, []byte(mixedExam))
	parts := h.mustImport(t, []byte(sectionedExam))
	steps := []struct {
		what string
		id   int64
		do   func(base int) error
		want []exam.SummaryClause
	}{
		{"edit", plain, func(base int) error {
			return h.svc.SaveQuestion(ctx, h.owner, plain, base, 2, question("Second edited", 1, "c", "d", "e"))
		}, []exam.SummaryClause{{Kind: exam.SummaryQuestionEdited, N: 2}}},
		{"edit correct", plain, func(base int) error {
			return h.svc.SaveQuestion(ctx, h.owner, plain, base, 2, question("Second edited", 0, "c", "d", "e"))
		}, []exam.SummaryClause{{Kind: exam.SummaryQuestionEdited, N: 2}, {Kind: exam.SummaryCorrectChanged}}},
		{"edit answers", mixed, func(base int) error {
			return h.svc.SaveQuestion(ctx, h.owner, mixed, base, 2, fillIn("Ibu kota ....", "Jakarta"))
		}, []exam.SummaryClause{{Kind: exam.SummaryQuestionEdited, N: 2}, {Kind: exam.SummaryAnswersChanged}}},
		{"add", plain, func(base int) error {
			return h.svc.AddQuestion(ctx, h.owner, plain, base, question("Third", 0, "x", "y"))
		}, []exam.SummaryClause{{Kind: exam.SummaryQuestionAdded, N: 3}}},
		{"move", plain, func(base int) error { return h.svc.MoveQuestion(ctx, h.owner, plain, base, 3, 1) },
			[]exam.SummaryClause{{Kind: exam.SummaryQuestionMoved, N: 3, M: 1}}},
		{"remove", plain, func(base int) error { return h.svc.RemoveQuestion(ctx, h.owner, plain, base, 1) },
			[]exam.SummaryClause{{Kind: exam.SummaryQuestionRemoved, N: 1}}},
		{"settings", plain, func(base int) error {
			all := exam.Settings{Title: "All new", Shuffle: examfile.ShuffleQuestionsOptions, ShowResult: examfile.ShowReview, DurationMinutes: 90}
			return h.svc.SaveSettings(ctx, h.owner, plain, base, all)
		}, []exam.SummaryClause{{Kind: exam.SummarySettingsChanged, Settings: []string{"title", "shuffle", "result shown", "monitor", "length"}}}},
		{"move in section", parts, func(base int) error { return h.svc.MoveQuestion(ctx, h.owner, parts, base, 1, 2) },
			[]exam.SummaryClause{{Kind: exam.SummaryQuestionMoved, N: 1, M: 2}}},
		{"move across", parts, func(base int) error { return h.svc.MoveQuestion(ctx, h.owner, parts, base, 3, 2) },
			[]exam.SummaryClause{{Kind: exam.SummaryQuestionSection, N: 3, M: 1}}},
		{"section start", parts, func(base int) error {
			return h.svc.StartSection(ctx, h.owner, parts, base, 2, exam.SectionSource{Title: "Middle"})
		}, []exam.SummaryClause{{Kind: exam.SummarySectionStarted, N: 2, M: 2}}},
		{"section edit", parts, func(base int) error {
			return h.svc.SaveSection(ctx, h.owner, parts, base, 2, exam.SectionSource{Title: "Centre"})
		}, []exam.SummaryClause{{Kind: exam.SummarySectionEdited, N: 2}}},
		{"section remove", parts, func(base int) error { return h.svc.RemoveSection(ctx, h.owner, parts, base, 2) },
			[]exam.SummaryClause{{Kind: exam.SummarySectionRemoved, N: 2}}},
	}
	for _, s := range steps {
		mustNil(t, s.what, s.do(h.current(t, s.id)))
		latest(s.what, s.id, s.want...)
	}
}

func requireParses(t *testing.T, what, stored string, want []exam.SummaryClause) {
	t.Helper()
	got, ok := exam.ParseSummary(stored)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("%s: ParseSummary(%q) = %+v, %v; want %+v, true", what, stored, got, ok, want)
	}
}
