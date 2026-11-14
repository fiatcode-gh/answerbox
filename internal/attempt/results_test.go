package attempt_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

func (h *harness) detail(t *testing.T, examID, attemptID int64) attempt.Detail {
	t.Helper()
	d, err := h.svc.Detail(context.Background(), examID, attemptID)
	if err != nil {
		t.Fatalf("Detail(%d, %d): %v", examID, attemptID, err)
	}
	return d
}

func TestDetail_FileOrderWithPickAndKey(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("questions, options", examfile.ShowReview, 4, 3))
	token := h.start(t, "Ayu")
	qs := h.questions(t, id)

	var fileOrder [][]int64
	for _, q := range qs {
		row := []int64{q.ID}
		for _, o := range q.Options {
			row = append(row, o.ID)
		}
		fileOrder = append(fileOrder, row)
	}
	if reflect.DeepEqual(order(h.view(t, token)), fileOrder) {
		t.Fatal("attempt order equals file order; the test needs a shuffled attempt")
	}

	h.save(t, token, qs[0].ID, correctOption(qs[0]))
	h.save(t, token, qs[1].ID, qs[1].Options[2].ID)
	h.save(t, token, qs[3].ID, correctOption(qs[3]))
	h.clock = t0.Add(10 * time.Minute)

	d := h.detail(t, id, h.only(t, id).ID)
	if !reflect.DeepEqual(d.Summary, h.only(t, id)) {
		t.Fatalf("summary = %+v, want the List row %+v", d.Summary, h.only(t, id))
	}
	if d.Score != 2 || d.Total != 4 {
		t.Fatalf("score = %d / %d, want 2 / 4", d.Score, d.Total)
	}
	if len(d.Questions) != 4 {
		t.Fatalf("detail has %d questions, want 4", len(d.Questions))
	}
	picks := map[int]int{1: 1, 2: 3, 4: 1} // file position → picked option position
	for i, dq := range d.Questions {
		q := qs[i]
		if dq.Position != i+1 || dq.BodyHTML != q.BodyHTML {
			t.Fatalf("question %d = position %d body %q, want position %d body %q", i, dq.Position, dq.BodyHTML, i+1, q.BodyHTML)
		}
		pick, answered := picks[dq.Position]
		if dq.Answered != answered {
			t.Errorf("question %d answered = %v, want %v", dq.Position, dq.Answered, answered)
		}
		if len(dq.Options) != 3 {
			t.Fatalf("question %d has %d options, want 3", dq.Position, len(dq.Options))
		}
		for j, do := range dq.Options {
			o := q.Options[j]
			if do.Position != j+1 || do.BodyHTML != o.BodyHTML {
				t.Errorf("question %d option %d = position %d body %q, want %d %q", dq.Position, j, do.Position, do.BodyHTML, j+1, o.BodyHTML)
			}
			if do.Correct != o.Correct {
				t.Errorf("question %d option %d correct = %v, want %v", dq.Position, do.Position, do.Correct, o.Correct)
			}
			if want := answered && pick == do.Position; do.Picked != want {
				t.Errorf("question %d option %d picked = %v, want %v", dq.Position, do.Position, do.Picked, want)
			}
		}
	}
}

func TestDetail_WrongExamOrUnknown(t *testing.T) {
	h := newHarness(t)
	examA := h.importExam(t, examText("none", examfile.ShowScore, 2, 2))
	examB := h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	h.start(t, "Budi")
	attemptB := h.only(t, examB).ID

	_, err := h.svc.Detail(context.Background(), examA, attemptB)
	wantErr(t, "Detail under the wrong exam", err, attempt.ErrNotFound)
	_, err = h.svc.Detail(context.Background(), examB, attemptB+100)
	wantErr(t, "Detail of an unknown attempt", err, attempt.ErrNotFound)
	h.detail(t, examB, attemptB)
}

func TestDelete_RemovesAttemptAndAnswers(t *testing.T) {
	h := newHarness(t)
	other := h.importExam(t, examText("none", examfile.ShowScore, 2, 2))
	id := h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	qs := h.questions(t, id)
	gone := h.start(t, "Ayu")
	h.save(t, gone, qs[0].ID, correctOption(qs[0]))
	h.save(t, gone, qs[1].ID, wrongOption(qs[1]))
	h.clock = t0.Add(time.Minute)
	kept := h.start(t, "Budi")
	h.save(t, kept, qs[0].ID, correctOption(qs[0]))
	rows := h.list(t, id)
	goneID, keptID := rows[0].ID, rows[1].ID

	err := h.svc.Delete(context.Background(), other, goneID)
	wantErr(t, "Delete under the wrong exam", err, attempt.ErrNotFound)
	if len(h.list(t, id)) != 2 || len(h.storedAnswers(t, goneID)) != 2 {
		t.Fatal("Delete under the wrong exam removed data")
	}

	if err := h.svc.Delete(context.Background(), id, goneID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rows := h.list(t, id); len(rows) != 1 || rows[0].ID != keptID {
		t.Fatalf("List after Delete = %+v, want only attempt %d", rows, keptID)
	}
	var left int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM answer WHERE attempt_id = ?`, goneID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d answer rows left for the deleted attempt", left)
	}
	if len(h.storedAnswers(t, keptID)) != 1 {
		t.Fatal("other attempt lost its answer")
	}
	_, err = h.svc.View(context.Background(), gone)
	wantErr(t, "View with the deleted attempt's token", err, attempt.ErrNotFound)

	err = h.svc.Delete(context.Background(), id, goneID)
	wantErr(t, "second Delete", err, attempt.ErrNotFound)
}

func TestDelete_LastAttemptUnfreezesExam(t *testing.T) {
	h := newHarness(t)
	text := examText("none", examfile.ShowScore, 2, 2)
	id := h.openExam(t, text)
	h.start(t, "Ayu")
	err := h.exams.Replace(context.Background(), id, []byte(text))
	wantErr(t, "Replace with an attempt", err, exam.ErrFrozen)

	if err := h.svc.Delete(context.Background(), id, h.only(t, id).ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := h.exams.Replace(context.Background(), id, []byte(examText("none", examfile.ShowScore, 3, 2))); err != nil {
		t.Fatalf("Replace after deleting the only attempt: %v", err)
	}
}
