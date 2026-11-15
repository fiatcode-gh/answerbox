package attempt_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

func (h *harness) record(t *testing.T, token string, kind attempt.EventKind, questionID int64) bool {
	t.Helper()
	ok, err := h.svc.RecordEvent(context.Background(), token, kind, questionID)
	if err != nil {
		t.Fatalf("RecordEvent(%s): %v", kind, err)
	}
	return ok
}

func (h *harness) storedEvents(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attempt_event`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// monitorOff adds "monitor: off" under the title of an exam text.
func monitorOff(text string) string {
	title, rest, _ := strings.Cut(text, "\n")
	return title + "\nmonitor: off\n" + rest
}

func TestRecordEvent_DetailAndFlags(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("questions", examfile.ShowScore, 3, 2))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	h.start(t, "Budi")

	h.clock = t0.Add(2 * time.Minute)
	if !h.record(t, token, attempt.LeftPage, qs[2].ID) {
		t.Fatal("left_page not recorded")
	}
	h.clock = t0.Add(time.Minute)
	h.record(t, token, attempt.Paste, 0)
	h.clock = t0.Add(3 * time.Minute)
	h.record(t, token, attempt.RightClick, qs[0].ID)

	rows := h.list(t, id)
	if rows[0].Flags != 3 || rows[1].Flags != 0 {
		t.Fatalf("flags = %d, %d; want 3, 0", rows[0].Flags, rows[1].Flags)
	}
	d, err := h.svc.Detail(context.Background(), id, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []attempt.Event{
		{At: t0.Add(time.Minute), Kind: attempt.Paste, Question: 0},
		{At: t0.Add(2 * time.Minute), Kind: attempt.LeftPage, Question: 3},
		{At: t0.Add(3 * time.Minute), Kind: attempt.RightClick, Question: 1},
	}
	if !reflect.DeepEqual(d.Events, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", d.Events, want)
	}
	d2, err := h.svc.Detail(context.Background(), id, rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Events) != 0 {
		t.Fatalf("other attempt shows %d events", len(d2.Events))
	}
}

func TestRecordEvent_ForeignQuestionIsUnknown(t *testing.T) {
	h := newHarness(t)
	otherExam := h.importExam(t, examText("none", examfile.ShowScore, 1, 2))
	foreign := h.questions(t, otherExam)[0].ID
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
	token := h.start(t, "Ayu")
	h.record(t, token, attempt.Copy, foreign)
	d, err := h.svc.Detail(context.Background(), id, h.only(t, id).ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 1 || d.Events[0].Question != 0 {
		t.Fatalf("events = %+v, want one with unknown question", d.Events)
	}
}

func TestRecordEvent_Rejections(t *testing.T) {
	h := newHarness(t)
	h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	token := h.start(t, "Ayu")

	_, err := h.svc.RecordEvent(context.Background(), "unknown-token", attempt.Copy, 0)
	wantErr(t, "unknown token", err, attempt.ErrNotFound)
	_, err = h.svc.RecordEvent(context.Background(), token, attempt.EventKind("scroll"), 0)
	wantErr(t, "unknown kind", err, attempt.ErrInvalidEvent)

	h.submit(t, token)
	_, err = h.svc.RecordEvent(context.Background(), token, attempt.Copy, 0)
	wantErr(t, "finished", err, attempt.ErrFinished)

	late := h.start(t, "Budi")
	h.clock = h.clock.Add(attempt.Duration + attempt.Grace + time.Second)
	_, err = h.svc.RecordEvent(context.Background(), late, attempt.Copy, 0)
	wantErr(t, "timed out", err, attempt.ErrFinished)

	if n := h.storedEvents(t); n != 0 {
		t.Fatalf("%d events stored, want 0", n)
	}
}

func TestRecordEvent_MonitorOffIgnored(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, monitorOff(examText("none", examfile.ShowScore, 2, 2)))
	token := h.start(t, "Ayu")
	if v := h.view(t, token); v.Monitor {
		t.Fatal("View.Monitor = true for a monitor: off exam")
	}
	if h.record(t, token, attempt.LeftPage, 0) {
		t.Fatal("event recorded with monitor off")
	}
	if n := h.storedEvents(t); n != 0 || h.only(t, id).Flags != 0 {
		t.Fatalf("%d events stored with monitor off", n)
	}
}

func TestRecordEvent_CapAt500(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
	token := h.start(t, "Ayu")
	if v := h.view(t, token); !v.Monitor {
		t.Fatal("View.Monitor = false for a default exam")
	}
	for i := range attempt.MaxEvents {
		if !h.record(t, token, attempt.Resize, 0) {
			t.Fatalf("event %d not recorded", i+1)
		}
	}
	if h.record(t, token, attempt.Resize, 0) {
		t.Fatal("event 501 recorded")
	}
	if got := h.only(t, id).Flags; got != attempt.MaxEvents {
		t.Fatalf("flags = %d, want %d", got, attempt.MaxEvents)
	}
}

func TestRecordEvent_NoEffectOnAnswersOrScore(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	h.save(t, token, qs[0].ID, correctOption(qs[0]))
	h.clock = t0.Add(10 * time.Minute)
	before := h.only(t, id)
	answers := h.storedAnswers(t, before.ID)

	for _, k := range []attempt.EventKind{attempt.LeftPage, attempt.Copy, attempt.Paste, attempt.RightClick, attempt.Resize} {
		h.record(t, token, k, qs[1].ID)
	}

	after := h.only(t, id)
	after.Flags = before.Flags
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("summary changed:\n%+v\n%+v", before, after)
	}
	if got := h.storedAnswers(t, before.ID); !reflect.DeepEqual(got, answers) {
		t.Fatalf("answers changed: %v, want %v", got, answers)
	}
	h.submit(t, token)
	if v := h.view(t, token); v.Result.Score != 1 || v.Result.Total != 2 {
		t.Fatalf("score = %d/%d, want 1/2", v.Result.Score, v.Result.Total)
	}
}
