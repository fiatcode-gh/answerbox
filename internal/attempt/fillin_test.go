package attempt_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// fillInText is an exam of two sections: Part 1 holds multiple-choice
// question 1 and fill-in question 2, Part 2 holds fill-in question 3 and
// multiple-choice question 4.
func fillInText(show examfile.ShowResult) string {
	return "# Fill-in exam\nshuffle: none\nshow_result: " + string(show) + `

## Part 1
Instructions 1.

1. Pick one
- Right *
- Wrong

2. Ibu kota ....
= Jakarta
= DKI Jakarta

## Part 2
Instructions 2.

3. Seribu tiga ....
= 1.003

4. Pick again
- Right *
- Wrong
`
}

func (h *harness) saveText(t *testing.T, token string, questionID int64, text string) {
	t.Helper()
	if _, err := h.svc.SaveText(context.Background(), token, questionID, text); err != nil {
		t.Fatalf("SaveText(%d, %q): %v", questionID, text, err)
	}
}

// typedAnswers reads the stored typed answers of every attempt, keyed
// "attempt/question".
func (h *harness) typedAnswers(t *testing.T) map[[2]int64]string {
	t.Helper()
	rows, err := h.db.Query(`SELECT attempt_id, question_id, text FROM typed_answer`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[[2]int64]string{}
	for rows.Next() {
		var a, q int64
		var text string
		if err := rows.Scan(&a, &q, &text); err != nil {
			t.Fatal(err)
		}
		out[[2]int64{a, q}] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// override stores a teacher's mark on a typed answer at t0 plus five minutes.
func (h *harness) override(t *testing.T, attemptID, questionID int64, correct bool) {
	t.Helper()
	res, err := h.db.Exec(`UPDATE typed_answer SET override_correct = ?, override_by = ?, override_at = ?
		WHERE attempt_id = ? AND question_id = ?`,
		correct, h.owner.TeacherID, t0.Add(5*time.Minute).UnixMilli(), attemptID, questionID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("override of attempt %d question %d: %d rows", attemptID, questionID, n)
	}
}

func TestSaveText_StoresTrimsAndClears(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, fillInText(examfile.ShowScore))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	a := h.only(t, id).ID
	key := [2]int64{a, qs[1].ID}

	h.saveText(t, token, qs[1].ID, "  Jakarta ")
	if got := h.typedAnswers(t); !reflect.DeepEqual(got, map[[2]int64]string{key: "Jakarta"}) {
		t.Fatalf("stored = %v, want Jakarta", got)
	}
	v := h.view(t, token)
	if q := v.Questions[1]; !q.FillIn || q.Typed != "Jakarta" || len(q.Options) != 0 {
		t.Fatalf("view question 2 = %+v, want fill-in with Jakarta typed", q)
	}
	if q := v.Questions[0]; q.FillIn || q.Typed != "" {
		t.Fatalf("view question 1 = %+v, want multiple choice", q)
	}

	h.saveText(t, token, qs[1].ID, "Bandung")
	if got := h.typedAnswers(t)[key]; got != "Bandung" {
		t.Fatalf("after a second save stored = %q, want Bandung", got)
	}
	h.saveText(t, token, qs[1].ID, "")
	if got := h.typedAnswers(t); len(got) != 0 {
		t.Fatalf("after an empty save stored = %v, want none", got)
	}
	h.saveText(t, token, qs[1].ID, "Medan")
	h.saveText(t, token, qs[1].ID, "   ")
	if got := h.typedAnswers(t); len(got) != 0 {
		t.Fatalf("after a blank save stored = %v, want none", got)
	}
	if q := h.view(t, token).Questions[1]; !q.FillIn || q.Typed != "" {
		t.Fatalf("view question 2 after clearing = %+v, want fill-in with nothing typed", q)
	}
}

func TestSaveText_Refusals(t *testing.T) {
	h := newHarness(t)
	other := h.importExam(t, fillInText(examfile.ShowScore))
	id := h.openExam(t, fillInText(examfile.ShowScore))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	ctx := context.Background()

	refused := func(what string, questionID int64, text string) {
		t.Helper()
		_, err := h.svc.SaveText(ctx, token, questionID, text)
		wantErr(t, what, err, attempt.ErrInvalidAnswer)
		if got := h.typedAnswers(t); len(got) != 0 {
			t.Fatalf("%s: stored %v", what, got)
		}
	}
	refused("multiple-choice question", qs[0].ID, "Right")
	refused("question of another exam", h.questions(t, other)[1].ID, "Jakarta")
	refused("201 runes", qs[1].ID, strings.Repeat("é", 201))
	refused("tab inside", qs[1].ID, "Jak\tarta")
	refused("newline inside", qs[1].ID, "Jak\narta")
	refused("invalid UTF-8", qs[1].ID, "Jak\xffarta")

	h.saveText(t, token, qs[1].ID, strings.Repeat("é", 200))
	if got := h.typedAnswers(t); len(got) != 1 {
		t.Fatalf("200 runes: stored %v, want one row", got)
	}

	for _, o := range append(qs[0].Options, qs[3].Options...) {
		_, err := h.svc.SaveAnswer(ctx, token, qs[1].ID, o.ID)
		wantErr(t, "SaveAnswer on a fill-in question", err, attempt.ErrInvalidChoice)
	}

	_, err := h.svc.SaveText(ctx, "no-such-token", qs[1].ID, "Jakarta")
	wantErr(t, "unknown token", err, attempt.ErrNotFound)

	h.submit(t, token)
	_, err = h.svc.SaveText(ctx, token, qs[2].ID, "1003")
	wantErr(t, "finished attempt", err, attempt.ErrFinished)
	if got := h.typedAnswers(t); len(got) != 1 {
		t.Fatalf("after the refusals stored %v, want only the 200-rune answer", got)
	}
}

func TestView_FillInCarriesNoKey(t *testing.T) {
	names := func(v any) []string {
		typ := reflect.TypeOf(v)
		var out []string
		for i := range typ.NumField() {
			out = append(out, typ.Field(i).Name)
		}
		return out
	}
	if got, want := names(attempt.ViewQuestion{}), []string{"ID", "Number", "BodyHTML", "Options", "Section", "FillIn", "Typed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ViewQuestion fields = %v, want %v", got, want)
	}
	if got, want := names(attempt.ViewOption{}), []string{"ID", "Letter", "BodyHTML", "Picked"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ViewOption fields = %v, want %v", got, want)
	}
}

func TestScore_OneRuleEverywhere(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, fillInText(examfile.ShowReview))
	qs := h.questions(t, id)

	ayu := h.start(t, "Ayu")
	h.save(t, ayu, qs[0].ID, correctOption(qs[0]))
	h.saveText(t, ayu, qs[1].ID, "  jakarta ")
	h.saveText(t, ayu, qs[2].ID, "1003")
	h.save(t, ayu, qs[3].ID, wrongOption(qs[3]))
	h.submit(t, ayu)

	h.clock = t0.Add(time.Minute)
	budi := h.start(t, "Budi")
	h.save(t, budi, qs[0].ID, wrongOption(qs[0]))
	h.saveText(t, budi, qs[1].ID, "Bandung")
	h.saveText(t, budi, qs[2].ID, "1003")
	h.save(t, budi, qs[3].ID, correctOption(qs[3]))
	h.submit(t, budi)

	h.clock = t0.Add(2 * time.Minute)
	citra := h.start(t, "Citra")
	h.submit(t, citra)

	rows := h.list(t, id)
	h.override(t, rows[1].ID, qs[1].ID, true)
	h.override(t, rows[1].ID, qs[2].ID, false)

	want := []struct {
		name     string
		token    string
		score    int
		sections []int
	}{
		{"Ayu", ayu, 3, []int{2, 1}},
		{"Budi", budi, 2, []int{1, 1}},
		{"Citra", citra, 0, []int{0, 0}},
	}
	res, err := h.svc.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if want := []attempt.ResultSection{{Title: "Part 1", Total: 2}, {Title: "Part 2", Total: 2}}; !reflect.DeepEqual(res.Sections, want) {
		t.Fatalf("sections = %+v, want %+v", res.Sections, want)
	}
	if len(res.Attempts) != len(want) {
		t.Fatalf("attempts = %+v, want %d", res.Attempts, len(want))
	}
	for i, w := range want {
		a := res.Attempts[i]
		if a.StudentName != w.name || a.Score != w.score || a.Total != 4 || !reflect.DeepEqual(a.SectionScores, w.sections) {
			t.Errorf("List %s = %s %d / %d %v, want %d / 4 %v", w.name, a.StudentName, a.Score, a.Total, a.SectionScores, w.score, w.sections)
		}
		if d := h.detail(t, id, a.ID); d.Score != w.score {
			t.Errorf("Detail %s score = %d, want %d", w.name, d.Score, w.score)
		}
		if r := h.view(t, w.token).Result; r == nil || r.Score != w.score || r.Total != 4 {
			t.Errorf("View %s result = %+v, want %d / 4", w.name, r, w.score)
		}
	}
}

func TestResult_ReviewFillIn(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, fillInText(examfile.ShowReview))
	qs := h.questions(t, id)
	token := h.start(t, "Budi")
	h.saveText(t, token, qs[1].ID, "Bandung")
	h.saveText(t, token, qs[2].ID, "1003")
	h.submit(t, token)
	h.override(t, h.only(t, id).ID, qs[1].ID, true)

	r := h.view(t, token).Result
	if r == nil || len(r.Review) != 4 {
		t.Fatalf("result = %+v, want four review questions", r)
	}
	type entry struct {
		FillIn, Answered, Right bool
		Typed                   string
		Accepted                []string
	}
	got := make([]entry, 4)
	for i, q := range r.Review {
		got[i] = entry{q.FillIn, q.Answered, q.Right, q.Typed, q.Accepted}
	}
	want := []entry{
		{},
		{FillIn: true, Answered: true, Right: true, Typed: "Bandung", Accepted: []string{"Jakarta", "DKI Jakarta"}},
		{FillIn: true, Answered: true, Right: true, Typed: "1003", Accepted: []string{"1.003"}},
		{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("review = %+v, want %+v", got, want)
	}

	for _, show := range []examfile.ShowResult{examfile.ShowScore, examfile.ShowNone} {
		other := h.importExam(t, fillInText(show))
		if err := h.exams.Open(context.Background(), h.owner, other); err != nil {
			t.Fatal(err)
		}
		tok := h.startIn(t, other, "Citra")
		h.saveText(t, tok, h.questions(t, other)[1].ID, "Jakarta")
		h.submit(t, tok)
		if r := h.view(t, tok).Result; r == nil || r.Review != nil {
			t.Fatalf("%s: result = %+v, want no review", show, r)
		}
	}
}

func TestDetail_FillIn(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, fillInText(examfile.ShowScore))
	qs := h.questions(t, id)
	token := h.start(t, "Budi")
	h.saveText(t, token, qs[1].ID, "Bandung")
	a := h.only(t, id).ID
	h.override(t, a, qs[1].ID, true)

	d := h.detail(t, id, a)
	q := d.Questions[1]
	wantOverride := &attempt.Override{Correct: true, TeacherEmail: "owner@example.test", At: t0.Add(5 * time.Minute)}
	if !q.FillIn || !q.Answered || q.Typed != "Bandung" || q.Auto || !q.Right ||
		!reflect.DeepEqual(q.Accepted, []string{"Jakarta", "DKI Jakarta"}) || !reflect.DeepEqual(q.Override, wantOverride) {
		t.Fatalf("detail question 2 = %+v (override %+v), want Bandung overridden right", q, q.Override)
	}
	if d.Score != 1 {
		t.Fatalf("score = %d, want 1", d.Score)
	}

	q = d.Questions[2]
	if !q.FillIn || q.Answered || q.Typed != "" || q.Auto || q.Right || q.Override != nil ||
		!reflect.DeepEqual(q.Accepted, []string{"1.003"}) {
		t.Fatalf("detail question 3 = %+v, want unanswered", q)
	}
	if q := d.Questions[0]; q.FillIn || q.Accepted != nil {
		t.Fatalf("detail question 1 = %+v, want multiple choice", q)
	}

	h.saveText(t, token, qs[2].ID, "1.003")
	if q := h.detail(t, id, a).Questions[2]; !q.Auto || !q.Right || q.Override != nil {
		t.Fatalf("detail question 3 after a matching answer = %+v, want auto right", q)
	}
}

func TestDelete_RemovesTypedAnswers(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, fillInText(examfile.ShowScore))
	qs := h.questions(t, id)
	gone := h.start(t, "Ayu")
	h.saveText(t, gone, qs[1].ID, "Jakarta")
	h.clock = t0.Add(time.Minute)
	kept := h.start(t, "Budi")
	h.saveText(t, kept, qs[1].ID, "Bandung")
	rows := h.list(t, id)

	if err := h.svc.Delete(context.Background(), id, rows[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, want := h.typedAnswers(t), map[[2]int64]string{{rows[1].ID, qs[1].ID}: "Bandung"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("typed answers after Delete = %v, want %v", got, want)
	}
}

func TestList_NoAttempts(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, fillInText(examfile.ShowScore))
	res, err := h.svc.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := []attempt.ResultSection{{Title: "Part 1", Total: 2}, {Title: "Part 2", Total: 2}}
	if !reflect.DeepEqual(res.Sections, want) || res.Attempts == nil || len(res.Attempts) != 0 {
		t.Fatalf("results = %#v, want both sections and no attempts", res)
	}
}
