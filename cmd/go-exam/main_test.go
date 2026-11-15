package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
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

func TestNewAdmin_ClearsSessions(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.Open(t)
	before, err := newAdmin(ctx, db, "old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, err := before.Login(ctx, "old-password-123", netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}

	after, err := newAdmin(ctx, db, "new-password-456")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := after.Session(ctx, token); err != nil || ok {
		t.Fatalf("session from before the restart: valid=%v err=%v, want invalid", ok, err)
	}
}
