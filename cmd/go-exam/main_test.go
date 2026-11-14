package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a log sink safe for the sweeper goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSweeper_SweepsLogsAndStops(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	calls := make(chan time.Duration, 16)
	sweep := func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("sweep context has no deadline")
		}
		calls <- time.Until(deadline)
		return errors.New("database is locked")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSweeper(ctx, time.Millisecond, sweep, logger)
		close(done)
	}()

	for range 2 {
		select {
		case left := <-calls:
			if left <= 0 || left > sweepTimeout {
				t.Errorf("sweep deadline in %v, want within %v", left, sweepTimeout)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("sweep was not called")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweeper did not stop after cancel")
	}
	if !strings.Contains(logs.String(), "database is locked") {
		t.Errorf("sweep error not logged: %s", logs.String())
	}
	if sweepInterval != 5*time.Second || sweepTimeout != 5*time.Second {
		t.Errorf("sweep interval/timeout = %v/%v, want 5s/5s", sweepInterval, sweepTimeout)
	}
}
