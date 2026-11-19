package attempt_test

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// lockedClock is a test clock that one goroutine moves while another reads it.
type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lockedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *lockedClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// startAcrossWait starts an attempt on code while another transaction holds
// the only connection, moves clock to moved once the start waits for the
// connection, then releases it and returns the start's result.
func (h *harness) startAcrossWait(t *testing.T, svc *attempt.Service, clock *lockedClock, code string, moved time.Time) error {
	t.Helper()
	ctx := context.Background()
	hold, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	waits := h.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, err := svc.Start(ctx, code, "Ayu")
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); h.db.Stats().WaitCount == waits; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			hold.Rollback()
			t.Fatal("Start never waited for the connection")
		}
	}
	clock.set(moved)
	if err := hold.Rollback(); err != nil {
		t.Fatal(err)
	}
	return <-done
}

// startTimes returns started_at and deadline_at (unix ms) of the only attempt.
func (h *harness) startTimes(t *testing.T) (started, deadline int64) {
	t.Helper()
	if err := h.db.QueryRow(`SELECT started_at, deadline_at FROM attempt`).Scan(&started, &deadline); err != nil {
		t.Fatal(err)
	}
	return started, deadline
}

func TestStart_WaitingAcrossCloseTimeIsRefused(t *testing.T) {
	h := newHarness(t)
	sched := exam.Schedule{OpensAt: t0.Add(time.Hour), ClosesAt: t0.Add(2 * time.Hour)}
	id := h.scheduledExam(t, examText("none", examfile.ShowScore, 1, 2), sched)
	clock := &lockedClock{t: sched.ClosesAt.Add(-time.Millisecond)}
	svc := attempt.NewService(h.db, clock.now, rand.NewChaCha8([32]byte{4, 2}))

	err := h.startAcrossWait(t, svc, clock, h.code(t, id), sched.ClosesAt.Add(time.Second))
	wantErr(t, "Start that got the connection after the close time", err, attempt.ErrNoOpenExam)
	if got := h.attemptRows(t); got != 0 {
		t.Fatalf("attempt rows = %d, want 0", got)
	}
}

func TestStart_WaitingAcrossOpenTimeIsLetIn(t *testing.T) {
	h := newHarness(t)
	sched := exam.Schedule{OpensAt: t0.Add(time.Hour), ClosesAt: t0.Add(2 * time.Hour)}
	id := h.scheduledExam(t, examText("none", examfile.ShowScore, 1, 2), sched)
	clock := &lockedClock{t: sched.OpensAt.Add(-time.Millisecond)}
	svc := attempt.NewService(h.db, clock.now, rand.NewChaCha8([32]byte{4, 2}))

	moved := sched.OpensAt.Add(time.Second)
	if err := h.startAcrossWait(t, svc, clock, h.code(t, id), moved); err != nil {
		t.Fatalf("Start that got the connection after the open time: %v, want let in", err)
	}
	started, deadline := h.startTimes(t)
	if want := moved.UnixMilli(); started != want || deadline != want+defaultLength.Milliseconds() {
		t.Errorf("started_at, deadline_at = %d, %d, want %d, %d", started, deadline, want, want+defaultLength.Milliseconds())
	}
}
