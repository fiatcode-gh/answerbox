package exam_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
)

// rawSchedule is the stored button state and schedule of one exam.
type rawSchedule struct {
	isOpen            int
	opensAt, closesAt sql.NullInt64
}

func (h *harness) rawSchedule(t *testing.T, id int64) rawSchedule {
	t.Helper()
	var r rawSchedule
	if err := h.db.QueryRow(`SELECT is_open, opens_at, closes_at FROM exam WHERE id = ?`, id).
		Scan(&r.isOpen, &r.opensAt, &r.closesAt); err != nil {
		t.Fatal(err)
	}
	return r
}

func (h *harness) mustSetSchedule(t *testing.T, id int64, sched exam.Schedule) {
	t.Helper()
	if err := h.svc.SetSchedule(context.Background(), h.owner, id, sched); err != nil {
		t.Fatalf("SetSchedule(%d, %+v): %v", id, sched, err)
	}
}

// listOpen returns Exam.Open of id as List reports it.
func (h *harness) listOpen(t *testing.T, id int64) bool {
	t.Helper()
	list, err := h.svc.List(context.Background(), h.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.ID == id {
			return e.Open
		}
	}
	t.Fatalf("List has no exam %d", id)
	return false
}

func TestAvailabilityAt(t *testing.T) {
	opens := fixedNow
	closes := fixedNow.Add(time.Hour)
	ms := time.Millisecond
	none := exam.Schedule{}
	openOnly := exam.Schedule{OpensAt: opens}
	closeOnly := exam.Schedule{ClosesAt: closes}
	both := exam.Schedule{OpensAt: opens, ClosesAt: closes}

	tests := []struct {
		name       string
		buttonOpen bool
		sched      exam.Schedule
		now        time.Time
		want       exam.Availability
	}{
		{"no schedule, button open", true, none, opens, exam.OpenNow},
		{"no schedule, button closed", false, none, opens, exam.Closed},
		{"open only, 1 ms before", false, openOnly, opens.Add(-ms), exam.NotYetOpen},
		{"open only, at", false, openOnly, opens, exam.OpenNow},
		{"open only, after", false, openOnly, opens.Add(24 * time.Hour), exam.OpenNow},
		{"close only, 1 ms before", false, closeOnly, closes.Add(-ms), exam.OpenNow},
		{"close only, at", false, closeOnly, closes, exam.Closed},
		{"close only, after", false, closeOnly, closes.Add(ms), exam.Closed},
		{"both, before", false, both, opens.Add(-ms), exam.NotYetOpen},
		{"both, at open", false, both, opens, exam.OpenNow},
		{"both, inside", false, both, opens.Add(30 * time.Minute), exam.OpenNow},
		{"both, at close", false, both, closes, exam.Closed},
		{"both, after", false, both, closes.Add(time.Hour), exam.Closed},
		{"button open, before the schedule", true, both, opens.Add(-ms), exam.NotYetOpen},
		{"button open, inside the schedule", true, both, opens, exam.OpenNow},
		{"button open, after the schedule", true, both, closes, exam.Closed},
	}
	for _, tt := range tests {
		if got := exam.AvailabilityAt(tt.buttonOpen, tt.sched, tt.now); got != tt.want {
			t.Errorf("%s: AvailabilityAt = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSetSchedule_Validation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	closed := h.mustImport(t, []byte(simpleExam))
	open := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Open(ctx, h.owner, open); err != nil {
		t.Fatal(err)
	}
	later := fixedNow.Add(2 * time.Hour)

	tests := []struct {
		name  string
		sched exam.Schedule
		want  error
	}{
		{"empty", exam.Schedule{}, exam.ErrScheduleEmpty},
		{"close equals open", exam.Schedule{OpensAt: later, ClosesAt: later}, exam.ErrScheduleOrder},
		{"close before open", exam.Schedule{OpensAt: later, ClosesAt: later.Add(-time.Minute)}, exam.ErrScheduleOrder},
		{"close equals the clock", exam.Schedule{ClosesAt: fixedNow}, exam.ErrSchedulePast},
		{"close before the clock", exam.Schedule{OpensAt: fixedNow.Add(-2 * time.Hour), ClosesAt: fixedNow.Add(-time.Hour)}, exam.ErrSchedulePast},
	}
	for _, tt := range tests {
		for id, wantOpen := range map[int64]int{closed: 0, open: 1} {
			if err := h.svc.SetSchedule(ctx, h.owner, id, tt.sched); !errors.Is(err, tt.want) {
				t.Errorf("%s: SetSchedule(%d) = %v, want %v", tt.name, id, err, tt.want)
			}
			if got := h.rawSchedule(t, id); got != (rawSchedule{isOpen: wantOpen}) {
				t.Errorf("%s: exam %d after refused SetSchedule = %+v, want is_open %d and no times", tt.name, id, got, wantOpen)
			}
		}
	}
}

func TestSetSchedule_StoresAndFollowsClock(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Open(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	sched := exam.Schedule{OpensAt: fixedNow.Add(time.Hour), ClosesAt: fixedNow.Add(2 * time.Hour)}
	h.mustSetSchedule(t, id, sched)

	want := rawSchedule{
		opensAt:  sql.NullInt64{Int64: sched.OpensAt.UnixMilli(), Valid: true},
		closesAt: sql.NullInt64{Int64: sched.ClosesAt.UnixMilli(), Valid: true},
	}
	if got := h.rawSchedule(t, id); got != want {
		t.Errorf("stored = %+v, want %+v", got, want)
	}
	if got := h.mustGet(t, id).Schedule; !got.OpensAt.Equal(sched.OpensAt) || !got.ClosesAt.Equal(sched.ClosesAt) {
		t.Errorf("Get().Schedule = %+v, want %+v", got, sched)
	}

	for _, step := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before", sched.OpensAt.Add(-time.Millisecond), false},
		{"inside", sched.OpensAt, true},
		{"after", sched.ClosesAt, false},
	} {
		h.clock = step.at
		if got := h.mustGet(t, id).Open; got != step.want {
			t.Errorf("%s: Get().Open = %v, want %v", step.name, got, step.want)
		}
		if got := h.listOpen(t, id); got != step.want {
			t.Errorf("%s: List().Open = %v, want %v", step.name, got, step.want)
		}
	}

	h.clock = fixedNow
	past := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, past, exam.Schedule{OpensAt: fixedNow.Add(-time.Hour)})
	if !h.mustGet(t, past).Open || !h.listOpen(t, past) {
		t.Error("an open time in the past without a close time is not open at once")
	}
}

func TestOpenClose_ClearSchedule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sched := exam.Schedule{OpensAt: fixedNow.Add(time.Hour), ClosesAt: fixedNow.Add(2 * time.Hour)}

	early := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, early, sched)
	if err := h.svc.Open(ctx, h.owner, early); err != nil {
		t.Fatal(err)
	}
	if got := h.rawSchedule(t, early); got != (rawSchedule{isOpen: 1}) {
		t.Errorf("after Open = %+v, want is_open 1 and no times", got)
	}
	if !h.mustGet(t, early).Open {
		t.Error("Open before the open time: exam not open")
	}

	inside := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, inside, sched)
	h.clock = sched.OpensAt.Add(time.Minute)
	if !h.mustGet(t, inside).Open {
		t.Fatal("scheduled exam not open inside its window")
	}
	if err := h.svc.Close(ctx, h.owner, inside); err != nil {
		t.Fatal(err)
	}
	if got := h.rawSchedule(t, inside); got != (rawSchedule{}) {
		t.Errorf("after Close = %+v, want is_open 0 and no times", got)
	}
	if h.mustGet(t, inside).Open {
		t.Error("Close inside the window: exam still open")
	}
	h.clock = sched.ClosesAt.Add(-time.Minute)
	if h.mustGet(t, inside).Open {
		t.Error("closed exam opened again later in its old window")
	}
}

func TestSetSchedule_ForeignExamIsNotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.teacher(t, "a@example.test")
	b := h.teacher(t, "b@example.test")
	admin := h.admin(t)
	id := h.importAs(t, a, []byte(simpleExam))
	sched := exam.Schedule{OpensAt: fixedNow.Add(time.Hour)}

	if err := h.svc.SetSchedule(ctx, b, id, sched); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("SetSchedule as B = %v, want ErrNotFound", err)
	}
	if got := h.rawSchedule(t, id); got != (rawSchedule{}) {
		t.Errorf("after B's SetSchedule = %+v, want unchanged", got)
	}
	if err := h.svc.SetSchedule(ctx, h.owner, 9999, sched); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("SetSchedule(unknown) = %v, want ErrNotFound", err)
	}
	if err := h.svc.SetSchedule(ctx, admin, id, sched); err != nil {
		t.Errorf("SetSchedule as admin: %v", err)
	}
	if got := h.rawSchedule(t, id); !got.opensAt.Valid || got.opensAt.Int64 != sched.OpensAt.UnixMilli() {
		t.Errorf("after the admin's SetSchedule = %+v", got)
	}
}

func TestReplace_KeepsSchedule(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, id, exam.Schedule{OpensAt: fixedNow.Add(time.Hour), ClosesAt: fixedNow.Add(2 * time.Hour)})
	before := h.rawSchedule(t, id)
	if err := h.svc.Replace(context.Background(), h.owner, id, []byte("# Renamed\nduration: 30\n\n1. Q\n- a *\n- b\n")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := h.rawSchedule(t, id); got != before || got.isOpen != 0 || !got.opensAt.Valid || !got.closesAt.Valid {
		t.Errorf("after Replace = %+v, want %+v", got, before)
	}
}
