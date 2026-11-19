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

// screenOf returns the number the question with questionID has on the
// student's screen in v.
func screenOf(t *testing.T, v attempt.View, questionID int64) int {
	t.Helper()
	for _, q := range v.Questions {
		if q.ID == questionID {
			return q.Number
		}
	}
	t.Fatalf("question %d not in the view", questionID)
	return 0
}

func TestRecordEvent_DetailAndFlags(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("questions", examfile.ShowScore, 3, 2))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	v := h.view(t, token)
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
		{At: t0.Add(time.Minute), Kind: attempt.Paste, Question: 0, Screen: 0},
		{At: t0.Add(2 * time.Minute), Kind: attempt.LeftPage, Question: 3, Screen: screenOf(t, v, qs[2].ID)},
		{At: t0.Add(3 * time.Minute), Kind: attempt.RightClick, Question: 1, Screen: screenOf(t, v, qs[0].ID)},
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

func TestDetail_EventScreenNumbers(t *testing.T) {
	kinds := []attempt.EventKind{attempt.LeftPage, attempt.Copy, attempt.Paste, attempt.RightClick, attempt.Resize}
	// recordAll records one event per question and one with an unknown
	// question, one second apart, and returns the attempt's Detail.
	recordAll := func(t *testing.T, h *harness, id int64, token string) attempt.Detail {
		t.Helper()
		for i, q := range h.questions(t, id) {
			h.clock = h.clock.Add(time.Second)
			h.record(t, token, kinds[i%len(kinds)], q.ID)
		}
		h.clock = h.clock.Add(time.Second)
		h.record(t, token, attempt.Copy, 0)
		return h.detail(t, id, h.only(t, id).ID)
	}

	t.Run("shuffled", func(t *testing.T) {
		h := newHarness(t)
		id := h.openExam(t, examText("questions, options", examfile.ShowScore, 6, 3))
		qs := h.questions(t, id)
		token := h.start(t, "Ayu")
		v := h.view(t, token)
		d := recordAll(t, h, id, token)

		reordered := false
		for _, ev := range d.Events {
			if ev.Question == 0 {
				if ev.Screen != 0 {
					t.Errorf("unknown question has Screen %d, want 0", ev.Screen)
				}
				continue
			}
			if want := screenOf(t, v, qs[ev.Question-1].ID); ev.Screen != want {
				t.Errorf("file question %d: Screen = %d, student saw %d", ev.Question, ev.Screen, want)
			}
			reordered = reordered || ev.Screen != ev.Question
		}
		if !reordered {
			t.Fatal("the test needs a shuffled attempt")
		}

		h.submit(t, token)
		if after := h.detail(t, id, h.only(t, id).ID); !reflect.DeepEqual(after.Events, d.Events) {
			t.Fatalf("finished attempt events =\n%+v\nwant\n%+v", after.Events, d.Events)
		}
	})

	t.Run("unshuffled", func(t *testing.T) {
		h := newHarness(t)
		id := h.openExam(t, examText("none", examfile.ShowScore, 3, 2))
		token := h.start(t, "Ayu")
		for _, ev := range recordAll(t, h, id, token).Events {
			if ev.Screen != ev.Question {
				t.Errorf("file question %d: Screen = %d, want the same", ev.Question, ev.Screen)
			}
		}
	})
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
	h.clock = h.clock.Add(defaultLength + attempt.Grace + time.Second)
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
