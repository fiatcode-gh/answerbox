package attempt_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

// overrideRow is one stored typed answer with its override columns.
type overrideRow struct {
	Text            string
	Correct, By, At any
}

// overrideRows reads every typed_answer row, keyed "attempt/question".
func (h *harness) overrideRows(t *testing.T) map[[2]int64]overrideRow {
	t.Helper()
	rows, err := h.db.Query(`SELECT attempt_id, question_id, text, override_correct, override_by, override_at FROM typed_answer`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[[2]int64]overrideRow{}
	for rows.Next() {
		var a, q int64
		var r overrideRow
		if err := rows.Scan(&a, &q, &r.Text, &r.Correct, &r.By, &r.At); err != nil {
			t.Fatal(err)
		}
		out[[2]int64{a, q}] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) mark(t *testing.T, examID, attemptID int64, position int, teacherID int64, mark attempt.Mark) {
	t.Helper()
	if err := h.svc.Override(context.Background(), examID, attemptID, position, teacherID, mark); err != nil {
		t.Fatalf("Override(position %d, %s): %v", position, mark, err)
	}
}

// typedAttempt opens fillInText(show) and finishes an attempt by "Ayu" that
// picks the key of question 1, types "Bandung" (not accepted) to question 2,
// "1003" (accepted) to question 3 and picks a wrong option of question 4:
// score 2. It returns the exam, the attempt and its token.
func (h *harness) typedAttempt(t *testing.T, show examfile.ShowResult) (examID, attemptID int64, token string) {
	t.Helper()
	examID = h.openExam(t, fillInText(show))
	qs := h.questions(t, examID)
	token = h.start(t, "Ayu")
	h.save(t, token, qs[0].ID, correctOption(qs[0]))
	h.saveText(t, token, qs[1].ID, "Bandung")
	h.saveText(t, token, qs[2].ID, "1003")
	h.save(t, token, qs[3].ID, wrongOption(qs[3]))
	h.submit(t, token)
	return examID, h.only(t, examID).ID, token
}

func TestOverride_MarksAndRemoves(t *testing.T) {
	h := newHarness(t)
	id, a, _ := h.typedAttempt(t, examfile.ShowScore)
	owner := h.owner.TeacherID
	h.clock = t0.Add(10 * time.Minute)
	at := t0.Add(10 * time.Minute)

	h.mark(t, id, a, 2, owner, attempt.MarkRight)
	h.mark(t, id, a, 3, owner, attempt.MarkWrong)
	d := h.detail(t, id, a)
	q2, q3 := d.Questions[1], d.Questions[2]
	if want := (&attempt.Override{Correct: true, TeacherEmail: "owner@example.test", At: at}); q2.Auto || !q2.Right || !reflect.DeepEqual(q2.Override, want) {
		t.Errorf("question 2 marked right = auto %v right %v override %+v, want auto false, right, %+v", q2.Auto, q2.Right, q2.Override, want)
	}
	if want := (&attempt.Override{Correct: false, TeacherEmail: "owner@example.test", At: at}); !q3.Auto || q3.Right || !reflect.DeepEqual(q3.Override, want) {
		t.Errorf("question 3 marked wrong = auto %v right %v override %+v, want auto true, wrong, %+v", q3.Auto, q3.Right, q3.Override, want)
	}

	h.mark(t, id, a, 2, owner, attempt.MarkNone)
	h.mark(t, id, a, 3, owner, attempt.MarkNone)
	d = h.detail(t, id, a)
	q2, q3 = d.Questions[1], d.Questions[2]
	if q2.Right || q2.Override != nil || !q3.Right || q3.Override != nil || d.Score != 2 {
		t.Fatalf("after removing both marks: q2 %+v, q3 %+v, score %d; want the automatic marks and score 2", q2, q3, d.Score)
	}

	before := h.overrideRows(t)
	h.mark(t, id, a, 2, owner, attempt.MarkNone)
	if got := h.overrideRows(t); !reflect.DeepEqual(got, before) {
		t.Errorf("removing a missing override changed rows: %v, want %v", got, before)
	}
	if got := h.detail(t, id, a); !reflect.DeepEqual(got, d) {
		t.Errorf("removing a missing override changed the detail:\n%+v\nwant\n%+v", got, d)
	}
}

func TestOverride_KeepsOnlyLatest(t *testing.T) {
	h := newHarness(t)
	id, a, _ := h.typedAttempt(t, examfile.ShowScore)
	second := sqlitetest.Teacher(t, h.db, "second@example.test")

	h.clock = t0.Add(10 * time.Minute)
	h.mark(t, id, a, 2, h.owner.TeacherID, attempt.MarkRight)
	h.clock = t0.Add(20 * time.Minute)
	h.mark(t, id, a, 2, second, attempt.MarkWrong)

	want := &attempt.Override{Correct: false, TeacherEmail: "second@example.test", At: t0.Add(20 * time.Minute)}
	if q := h.detail(t, id, a).Questions[1]; !reflect.DeepEqual(q.Override, want) || q.Right {
		t.Fatalf("question 2 = right %v override %+v, want wrong by %+v", q.Right, q.Override, want)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM typed_answer WHERE attempt_id = ? AND override_by IS NOT NULL`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("overridden rows = %d, want 1", n)
	}
}

func TestOverride_Refusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := h.importExam(t, fillInText(examfile.ShowScore))
	id, a, _ := h.typedAttempt(t, examfile.ShowScore)
	qs := h.questions(t, id)
	owner := h.owner.TeacherID

	h.clock = t0.Add(time.Minute)
	budi := h.start(t, "Budi")
	h.saveText(t, budi, qs[1].ID, "Jakarta")
	b := h.list(t, id)[1].ID
	before := h.overrideRows(t)

	for _, c := range []struct {
		what              string
		examID, attemptID int64
		position          int
		mark              attempt.Mark
		want              error
	}{
		{"unknown attempt", id, b + 100, 2, attempt.MarkRight, attempt.ErrNotFound},
		{"attempt of another exam", other, a, 2, attempt.MarkRight, attempt.ErrNotFound},
		{"position 0", id, a, 0, attempt.MarkRight, attempt.ErrNotFound},
		{"one past the end", id, a, 5, attempt.MarkRight, attempt.ErrNotFound},
		{"in progress", id, b, 2, attempt.MarkRight, attempt.ErrInProgress},
		{"multiple choice", id, a, 1, attempt.MarkRight, attempt.ErrNotOverridable},
		{"unanswered fill-in in progress", id, b, 3, attempt.MarkRight, attempt.ErrInProgress},
		{"maybe", id, a, 2, attempt.Mark("maybe"), attempt.ErrInvalidMark},
		{"empty mark", id, a, 2, attempt.Mark(""), attempt.ErrInvalidMark},
	} {
		err := h.svc.Override(ctx, c.examID, c.attemptID, c.position, owner, c.mark)
		wantErr(t, c.what, err, c.want)
		if got := h.overrideRows(t); !reflect.DeepEqual(got, before) {
			t.Fatalf("%s: rows = %v, want %v", c.what, got, before)
		}
	}

	h.submit(t, budi)
	err := h.svc.Override(ctx, id, b, 3, owner, attempt.MarkRight)
	wantErr(t, "unanswered fill-in", err, attempt.ErrNotOverridable)
	if got := h.overrideRows(t); !reflect.DeepEqual(got, before) {
		t.Fatalf("unanswered fill-in: rows = %v, want %v", got, before)
	}

	// An overdue attempt that no call has swept yet: a refused call keeps
	// its sweep, and a mark finishes it first.
	h.clock = t0.Add(2 * time.Minute)
	citra := h.start(t, "Citra")
	h.saveText(t, citra, qs[1].ID, "Jakarta")
	c := h.list(t, id)[2].ID
	h.clock = t0.Add(2*time.Minute + defaultLength + time.Minute)
	err = h.svc.Override(ctx, id, c, 1, owner, attempt.MarkRight)
	wantErr(t, "multiple choice of an overdue attempt", err, attempt.ErrNotOverridable)
	var status attempt.Status
	if err := h.db.QueryRow(`SELECT status FROM attempt WHERE id = ?`, c).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != attempt.TimedOut {
		t.Fatalf("overdue attempt after a refused mark = %s, want timed out", status)
	}

	h.clock = t0.Add(3 * time.Minute)
	dora := h.start(t, "Dora")
	h.saveText(t, dora, qs[1].ID, "Bandung")
	d := h.list(t, id)[3].ID
	h.clock = t0.Add(3*time.Minute + defaultLength + time.Minute)
	h.mark(t, id, d, 2, owner, attempt.MarkRight)
	if got := h.detail(t, id, d); got.Status != attempt.TimedOut || !got.Questions[1].Right {
		t.Fatalf("overdue attempt marked = %s, right %v; want timed out and right", got.Status, got.Questions[1].Right)
	}
}

func TestOverride_FrozenAndArchivedExam(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id, a, _ := h.typedAttempt(t, examfile.ShowScore)
	if err := h.exams.Close(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	if err := h.exams.Archive(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	revisions := func() int {
		t.Helper()
		var n int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM exam_revision WHERE exam_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := revisions()

	h.mark(t, id, a, 2, h.owner.TeacherID, attempt.MarkRight)
	if d := h.detail(t, id, a); !d.Questions[1].Right || d.Score != 3 {
		t.Fatalf("after marking on an archived exam: right %v score %d, want right and 3", d.Questions[1].Right, d.Score)
	}
	if got := revisions(); got != before {
		t.Fatalf("revisions = %d, want %d", got, before)
	}
}

func TestOverride_ChangesEveryScore(t *testing.T) {
	h := newHarness(t)
	id, a, token := h.typedAttempt(t, examfile.ShowReview)
	check := func(when string, score int, sections []int, right2, right3 bool) {
		t.Helper()
		res, err := h.svc.List(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if s := res.Attempts[0]; s.Score != score || !reflect.DeepEqual(s.SectionScores, sections) {
			t.Errorf("%s: List = %d %v, want %d %v", when, s.Score, s.SectionScores, score, sections)
		}
		if d := h.detail(t, id, a); d.Score != score || d.Questions[1].Right != right2 || d.Questions[2].Right != right3 {
			t.Errorf("%s: Detail = %d, right %v %v; want %d, %v %v", when, d.Score, d.Questions[1].Right, d.Questions[2].Right, score, right2, right3)
		}
		r := h.view(t, token).Result
		if r == nil || r.Score != score || len(r.Review) != 4 || r.Review[1].Right != right2 || r.Review[2].Right != right3 {
			t.Errorf("%s: View result = %+v, want %d with right %v %v", when, r, score, right2, right3)
		}
	}

	check("before", 2, []int{1, 1}, false, true)
	h.mark(t, id, a, 2, h.owner.TeacherID, attempt.MarkRight)
	check("question 2 marked right", 3, []int{2, 1}, true, true)
	h.mark(t, id, a, 3, h.owner.TeacherID, attempt.MarkWrong)
	check("question 3 marked wrong", 2, []int{2, 0}, true, false)
	h.mark(t, id, a, 2, h.owner.TeacherID, attempt.MarkNone)
	h.mark(t, id, a, 3, h.owner.TeacherID, attempt.MarkNone)
	check("marks removed", 2, []int{1, 1}, false, true)
}

func TestDelete_RemovesOverrides(t *testing.T) {
	h := newHarness(t)
	id, a, _ := h.typedAttempt(t, examfile.ShowScore)
	h.mark(t, id, a, 2, h.owner.TeacherID, attempt.MarkRight)
	if err := h.svc.Delete(context.Background(), id, a); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := h.overrideRows(t); len(got) != 0 {
		t.Fatalf("typed_answer rows after Delete = %v, want none", got)
	}
}
