package exam_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

func (h *harness) mustArchive(t *testing.T, actor exam.Actor, id int64) {
	t.Helper()
	if err := h.svc.Archive(context.Background(), actor, id); err != nil {
		t.Fatalf("Archive(%d): %v", id, err)
	}
}

// archivedAt returns the stored archived_at of id; 0 when it is NULL.
func (h *harness) archivedAt(t *testing.T, id int64) int64 {
	t.Helper()
	var at *int64
	if err := h.db.QueryRow(`SELECT archived_at FROM exam WHERE id = ?`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at == nil {
		return 0
	}
	return *at
}

func TestArchive_ClosesAndUnschedules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	closed := h.mustImport(t, []byte(simpleExam))
	open := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Open(ctx, h.owner, open); err != nil {
		t.Fatal(err)
	}
	scheduled := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, scheduled, exam.Schedule{OpensAt: fixedNow.Add(-time.Hour), ClosesAt: fixedNow.Add(time.Hour)})
	opensOnly := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, opensOnly, exam.Schedule{OpensAt: fixedNow.Add(time.Hour)})

	for name, id := range map[string]int64{"closed": closed, "open": open, "scheduled": scheduled, "open time only": opensOnly} {
		h.insertAttempt(t, id, name)
		h.mustArchive(t, h.owner, id)
		if got := h.rawSchedule(t, id); got != (rawSchedule{}) {
			t.Errorf("%s: stored after Archive = %+v, want is_open 0 and no times", name, got)
		}
		ex := h.mustGet(t, id)
		if !ex.Archived() || !ex.ArchivedAt.Equal(h.clock) || ex.Open || ex.Schedule.Set() || ex.AttemptCount != 1 {
			t.Errorf("%s: after Archive: %+v", name, ex)
		}
		if _, ok, err := h.svc.EntryByCode(ctx, ex.Code); err != nil || ok {
			t.Errorf("%s: EntryByCode after Archive: ok=%v err=%v", name, ok, err)
		}
	}
}

func TestArchive_TwiceKeepsFirstDate(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(simpleExam))
	h.mustArchive(t, h.owner, id)
	h.clock = fixedNow.Add(time.Hour)
	h.mustArchive(t, h.owner, id)
	if got := h.mustGet(t, id).ArchivedAt; !got.Equal(fixedNow) {
		t.Errorf("ArchivedAt after a second Archive = %v, want %v", got, fixedNow)
	}
}

func TestArchive_RefusesOpenAndSchedule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	h.mustArchive(t, h.owner, id)
	stored := h.archivedAt(t, id)

	unchanged := func(what string) {
		t.Helper()
		if got := h.rawSchedule(t, id); got != (rawSchedule{}) {
			t.Errorf("%s: stored = %+v, want is_open 0 and no times", what, got)
		}
		if got := h.archivedAt(t, id); got != stored {
			t.Errorf("%s: archived_at = %d, want %d", what, got, stored)
		}
	}
	if err := h.svc.Open(ctx, h.owner, id); !errors.Is(err, exam.ErrArchived) {
		t.Errorf("Open = %v, want ErrArchived", err)
	}
	unchanged("Open")
	for name, sched := range map[string]exam.Schedule{
		"valid":           {OpensAt: fixedNow.Add(time.Hour), ClosesAt: fixedNow.Add(2 * time.Hour)},
		"empty":           {},
		"close in past":   {ClosesAt: fixedNow.Add(-time.Hour)},
		"close not later": {OpensAt: fixedNow.Add(2 * time.Hour), ClosesAt: fixedNow.Add(time.Hour)},
	} {
		if err := h.svc.SetSchedule(ctx, h.owner, id, sched); !errors.Is(err, exam.ErrArchived) {
			t.Errorf("SetSchedule(%s) = %v, want ErrArchived", name, err)
		}
		unchanged("SetSchedule " + name)
	}
	if err := h.svc.Close(ctx, h.owner, id); err != nil {
		t.Errorf("Close on an archived exam: %v", err)
	}
	unchanged("Close")
}

func TestArchive_SchemaRefusesDirectSQL(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(simpleExam))
	h.mustArchive(t, h.owner, id)
	stored := h.archivedAt(t, id)
	for _, c := range []struct {
		query string
		arg   any
	}{
		{`UPDATE exam SET is_open = 1 WHERE id = ?`, nil},
		{`UPDATE exam SET opens_at = ? WHERE id = ?`, fixedNow.UnixMilli()},
		{`UPDATE exam SET closes_at = ? WHERE id = ?`, fixedNow.UnixMilli()},
	} {
		args := []any{id}
		if c.arg != nil {
			args = []any{c.arg, id}
		}
		if _, err := h.db.Exec(c.query, args...); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("%s: err = %v, want a CHECK failure", c.query, err)
		}
		if got := h.rawSchedule(t, id); got != (rawSchedule{}) || h.archivedAt(t, id) != stored {
			t.Errorf("%s: stored = %+v, archived_at %d", c.query, got, h.archivedAt(t, id))
		}
	}
}

func TestUnarchive_LeavesClosedWithoutSchedule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	h.mustSetSchedule(t, id, exam.Schedule{OpensAt: fixedNow.Add(time.Hour), ClosesAt: fixedNow.Add(2 * time.Hour)})
	h.mustArchive(t, h.owner, id)
	if err := h.svc.Unarchive(ctx, h.owner, id); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if ex := h.mustGet(t, id); ex.Archived() || !ex.ArchivedAt.IsZero() || ex.Open || ex.Schedule.Set() {
		t.Errorf("after Unarchive: %+v", ex)
	}
	if got := h.rawSchedule(t, id); got != (rawSchedule{}) {
		t.Errorf("stored after Unarchive = %+v, want is_open 0 and no times", got)
	}
	if err := h.svc.Open(ctx, h.owner, id); err != nil {
		t.Errorf("Open after Unarchive: %v", err)
	}
	if err := h.svc.Close(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	h.mustSetSchedule(t, id, exam.Schedule{OpensAt: fixedNow.Add(time.Hour)})

	open := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Open(ctx, h.owner, open); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Unarchive(ctx, h.owner, open); err != nil {
		t.Errorf("Unarchive of an exam that is not archived: %v", err)
	}
	if ex := h.mustGet(t, open); !ex.Open || ex.Archived() {
		t.Errorf("open exam after Unarchive: %+v", ex)
	}
}

func TestArchive_Scope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.teacher(t, "a@example.test")
	b := h.teacher(t, "b@example.test")
	admin := h.admin(t)
	id := h.importAs(t, a, []byte(simpleExam))
	if err := h.svc.Open(ctx, a, id); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.Archive(ctx, b, id); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Archive as B = %v, want ErrNotFound", err)
	}
	if got := h.rawSchedule(t, id); got != (rawSchedule{isOpen: 1}) || h.archivedAt(t, id) != 0 {
		t.Errorf("after B's Archive: %+v, archived_at %d", got, h.archivedAt(t, id))
	}
	if err := h.svc.Archive(ctx, a, 9999); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Archive(unknown) = %v, want ErrNotFound", err)
	}
	if err := h.svc.Unarchive(ctx, a, 9999); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Unarchive(unknown) = %v, want ErrNotFound", err)
	}

	h.mustArchive(t, admin, id)
	if err := h.svc.Unarchive(ctx, b, id); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Unarchive as B = %v, want ErrNotFound", err)
	}
	if err := h.svc.Open(ctx, b, id); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Open as B on an archived exam = %v, want ErrNotFound", err)
	}
	if err := h.svc.SetSchedule(ctx, b, id, exam.Schedule{OpensAt: fixedNow.Add(time.Hour)}); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("SetSchedule as B on an archived exam = %v, want ErrNotFound", err)
	}
	if err := h.svc.SetSchedule(ctx, b, id, exam.Schedule{}); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("empty SetSchedule as B on an archived exam = %v, want ErrNotFound", err)
	}
	ex, err := h.svc.Get(ctx, a, id)
	if err != nil || !ex.Archived() {
		t.Errorf("A's exam after the admin's Archive: %+v, %v", ex, err)
	}
	if err := h.svc.Unarchive(ctx, a, id); err != nil {
		t.Errorf("Unarchive as A: %v", err)
	}
	if h.archivedAt(t, id) != 0 {
		t.Error("A's Unarchive left the exam archived")
	}
}

func TestList_IncludesArchived(t *testing.T) {
	h := newHarness(t)
	first := h.mustImport(t, []byte(simpleExam))
	middle := h.mustImport(t, []byte(simpleExam))
	last := h.mustImport(t, []byte(simpleExam))
	h.mustArchive(t, h.owner, middle)

	list, err := h.svc.List(context.Background(), h.owner)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{last, middle, first}
	if len(list) != len(want) {
		t.Fatalf("List returned %d exams, want %d", len(list), len(want))
	}
	for i, e := range list {
		if e.ID != want[i] || e.Archived() != (e.ID == middle) {
			t.Errorf("List[%d] = id %d archived %v; want id %d archived %v", i, e.ID, e.Archived(), want[i], want[i] == middle)
		}
	}
}

func TestArchived_CopyAndContent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	h.mustArchive(t, h.owner, id)

	copyID := h.mustCopy(t, h.owner, id)
	if h.mustGet(t, copyID).Archived() {
		t.Error("copy of an archived exam is archived")
	}
	if !h.mustGet(t, id).Archived() {
		t.Error("source not archived after Copy")
	}
	if f := h.mustDownload(t, h.owner, id); len(f.Data) == 0 {
		t.Error("Download of an archived exam is empty")
	}
	if err := h.svc.Replace(ctx, h.owner, id, []byte("# Renamed\n\n1. Q\n- a *\n- b\n")); err != nil {
		t.Fatalf("Replace on an archived exam: %v", err)
	}
	if ex := h.mustGet(t, id); ex.Revision != 2 || !ex.Archived() || ex.Open || ex.Title != "Renamed" {
		t.Errorf("after Replace: %+v", ex)
	}
	if got := h.rawSchedule(t, id); got != (rawSchedule{}) {
		t.Errorf("stored after Replace = %+v, want is_open 0 and no times", got)
	}
}
