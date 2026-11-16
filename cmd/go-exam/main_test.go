package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

// newExams returns an exam.Service on db with a throwaway media folder.
func newExams(t *testing.T, db *sql.DB) *exam.Service {
	t.Helper()
	media, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })
	return exam.NewService(db, media, time.Now, rand.Reader)
}

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

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

func TestNewAdmin_KeepsSessions(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.Open(t)
	exams := newExams(t, db)
	before, err := newAdmin(ctx, db, exams, []string{"admin@example.test"}, discardLogger)
	if err != nil {
		t.Fatal(err)
	}
	token, err := before.SignIn(ctx, adminauth.Identity{Email: "admin@example.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}

	after, err := newAdmin(ctx, db, exams, []string{"admin@example.test"}, discardLogger)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := after.Session(ctx, token); err != nil || !ok {
		t.Fatalf("session from before the restart: valid=%v err=%v, want valid", ok, err)
	}
}

func TestNewAdmin_RejectsBadAdminEmail(t *testing.T) {
	db := sqlitetest.Open(t)
	_, err := newAdmin(context.Background(), db, newExams(t, db), []string{"nope"}, discardLogger)
	if err == nil || !strings.HasPrefix(err.Error(), "GOEXAM_ADMIN_EMAILS:") || strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want a GOEXAM_ADMIN_EMAILS: error without the value", err)
	}
}

// dumpRows renders every row of the exam (without its owner), question,
// option, attempt, answer and event tables, so a test can compare them.
func dumpRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, title, shuffle, show_result, monitor, is_open, media_key, created_at FROM exam ORDER BY id`,
		`SELECT id, exam_id, position, body_html FROM question ORDER BY id`,
		`SELECT id, question_id, position, body_html, is_correct FROM question_option ORDER BY id`,
		`SELECT id, exam_id, student_name, hex(token_hash), resume_code, shuffle_seed, started_at, deadline_at FROM attempt ORDER BY id`,
		`SELECT attempt_id, question_id, option_id FROM answer ORDER BY attempt_id, question_id`,
		`SELECT id, attempt_id, kind, question, at FROM attempt_event ORDER BY id`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, "--")
	}
	return b.String()
}

func TestNewAdmin_AssignsExistingExamsToFirstAdmin(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "go-exam.db")
	old, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_monitor.sql"} {
		schema, err := os.ReadFile("../../internal/platform/sqlite/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.Exec(string(schema)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, q := range []string{
		`PRAGMA user_version = 2`,
		`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor) VALUES (1,'Old','none','score',1,'k1',5,1)`,
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`,
		`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`,
		`INSERT INTO attempt (id, exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at) VALUES (1,1,'Ayu',x'01','ABCDEFGH',7,10,20)`,
		`INSERT INTO answer (attempt_id, question_id, option_id) VALUES (1,1,2)`,
		`INSERT INTO attempt_event (id, attempt_id, kind, question, at) VALUES (1,1,'copy',1,15)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	before := dumpRows(t, old)
	old.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exams := newExams(t, db)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := newAdmin(ctx, db, exams, []string{"First@Example.Test", "second@example.test"}, logger); err != nil {
		t.Fatal(err)
	}

	var firstID, owner int64
	if err := db.QueryRow(`SELECT id FROM teacher WHERE email = 'first@example.test'`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT owner_id FROM exam WHERE id = 1`).Scan(&owner); err != nil || owner != firstID {
		t.Fatalf("owner = %d, %v; want the first admin %d", owner, err, firstID)
	}
	if after := dumpRows(t, db); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	e, err := exams.Get(ctx, exam.Actor{TeacherID: firstID}, 1)
	if err != nil || !e.Open || e.Title != "Old" {
		t.Errorf("Get as the first admin = %+v, %v", e, err)
	}
	const assigned = "existing exams assigned to the first admin"
	if !strings.Contains(logs.String(), assigned) || !strings.Contains(logs.String(), "count=1") ||
		strings.Contains(strings.ToLower(logs.String()), "example.test") {
		t.Errorf("log = %q, want the assignment with count=1 and no email", logs.String())
	}

	logs.Reset()
	if _, err := newAdmin(ctx, db, exams, []string{"second@example.test", "first@example.test"}, logger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT owner_id FROM exam WHERE id = 1`).Scan(&owner); err != nil || owner != firstID {
		t.Fatalf("owner after swapping the emails = %d, %v; want %d", owner, err, firstID)
	}
	if strings.Contains(logs.String(), assigned) {
		t.Errorf("second start logged an assignment: %q", logs.String())
	}
}

// The fake provider signs in anyone; it must never be part of the product.
func TestBinary_DoesNotLinkFakeProvider(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "internal/oidcfake") || strings.Contains(line, "oidc/oidctest") {
			t.Errorf("cmd/go-exam links %s", strings.TrimSpace(line))
		}
	}
}
