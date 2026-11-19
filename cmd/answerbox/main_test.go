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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/adminauth"
	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite"
	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite/sqlitetest"
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
	if err == nil || !strings.HasPrefix(err.Error(), "ANSWERBOX_ADMIN_EMAILS:") || strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want a ANSWERBOX_ADMIN_EMAILS: error without the value", err)
	}
}

// dumpRows renders every row of the exam (without its owner), question,
// option, attempt, answer and event tables, so a test can compare them.
func dumpRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	return dumpQueries(t, db,
		`SELECT id, title, shuffle, show_result, monitor, is_open, media_key, created_at FROM exam ORDER BY id`,
		`SELECT id, exam_id, position, body_html FROM question ORDER BY id`,
		`SELECT id, question_id, position, body_html, is_correct FROM question_option ORDER BY id`,
		`SELECT id, exam_id, student_name, hex(token_hash), resume_code, shuffle_seed, started_at, deadline_at FROM attempt ORDER BY id`,
		`SELECT attempt_id, question_id, option_id FROM answer ORDER BY attempt_id, question_id`,
		`SELECT id, attempt_id, kind, question, at FROM attempt_event ORDER BY id`,
	)
}

// dumpQueries renders the rows of every query, so two dumps of the same data
// are equal.
func dumpQueries(t *testing.T, db *sql.DB, queries ...string) string {
	t.Helper()
	var b strings.Builder
	for _, q := range queries {
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

// openOldDatabase creates a database file at path with the first version
// migrations applied by hand, as that release left it, and returns a raw
// handle.
func openOldDatabase(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	old, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob("../../internal/platform/sqlite/migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names[:version] {
		schema, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.Exec(string(schema)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := old.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestNewAdmin_AssignsExistingExamsToFirstAdmin(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 2)
	for _, q := range []string{
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

var examCodePattern = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTVWXYZ2-9]{6}$`)

// examCodes returns every exam's code by id.
func examCodes(t *testing.T, db *sql.DB) map[int64]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, code FROM exam ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	codes := map[int64]string{}
	for rows.Next() {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			t.Fatal(err)
		}
		codes[id] = code
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return codes
}

func TestAssignExamCodes_UpgradedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 4)
	for _, q := range []string{
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id) VALUES (1,'Live','none','score',1,'k1',5,1,1)`,
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`,
		`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`,
		`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id) VALUES (2,'Draft','none','score',0,'k2',6,1,1)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := range 51 {
		if _, err := old.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, status, started_at, deadline_at, finished_at)
			VALUES (1, ?, ?, ?, 'submitted', 10, 20, 15)`, fmt.Sprintf("Student %d", i), []byte{byte(i)}, i); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
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
	if _, err := newAdmin(ctx, db, exams, []string{"first@example.test"}, logger); err != nil {
		t.Fatal(err)
	}
	if err := assignExamCodes(ctx, exams, logger); err != nil {
		t.Fatalf("assignExamCodes: %v", err)
	}

	codes := examCodes(t, db)
	if len(codes) != 2 || !examCodePattern.MatchString(codes[1]) || !examCodePattern.MatchString(codes[2]) || codes[1] == codes[2] {
		t.Fatalf("codes = %v, want two different exam codes", codes)
	}
	var open, attempts int
	if err := db.QueryRow(`SELECT is_open, (SELECT COUNT(*) FROM attempt WHERE exam_id = 1) FROM exam WHERE id = 1`).Scan(&open, &attempts); err != nil || open != 1 || attempts != 51 {
		t.Errorf("exam 1: open=%d attempts=%d err=%v; want open with 51 attempts", open, attempts, err)
	}
	if after := dumpRows(t, db); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	const given = "existing exams given a code"
	if !strings.Contains(logs.String(), given) || !strings.Contains(logs.String(), "count=2") {
		t.Errorf("log = %q, want %q with count=2", logs.String(), given)
	}

	third, err := exams.Import(ctx, exam.Actor{TeacherID: 1}, []byte("# Third\n\n1. Q\n- a *\n- b\n"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	codes = examCodes(t, db)
	logs.Reset()
	if err := assignExamCodes(ctx, exams, logger); err != nil {
		t.Fatalf("second assignExamCodes: %v", err)
	}
	if again := examCodes(t, db); len(again) != 3 || !maps.Equal(again, codes) || !examCodePattern.MatchString(again[third]) {
		t.Errorf("codes after the second call = %v, want %v", again, codes)
	}
	if logs.Len() != 0 {
		t.Errorf("second call logged %q, want nothing", logs.String())
	}
}

func TestUpgrade_Version5OpenExamKeeps60MinutesAndNoSchedule(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 5)
	for _, q := range []string{
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code) VALUES (1,'Live','none','score',1,'k1',5,1,1,'3VK583')`,
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`,
		`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := range 51 {
		if _, err := old.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, status, started_at, deadline_at, finished_at)
			VALUES (1, ?, ?, ?, 'submitted', 10, 20, 15)`, fmt.Sprintf("Student %d", i), []byte{byte(i)}, i); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
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
	if _, err := newAdmin(ctx, db, exams, []string{"first@example.test"}, logger); err != nil {
		t.Fatal(err)
	}
	if err := assignExamCodes(ctx, exams, logger); err != nil {
		t.Fatalf("assignExamCodes: %v", err)
	}

	var version string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != "10" {
		t.Fatalf("user_version = %s, %v; want 10", version, err)
	}
	var (
		open, minutes, noOpens, noCloses int
		code                             string
	)
	err = db.QueryRow(`SELECT is_open, duration_minutes, opens_at IS NULL, closes_at IS NULL, code FROM exam WHERE id = 1`).
		Scan(&open, &minutes, &noOpens, &noCloses, &code)
	if err != nil {
		t.Fatal(err)
	}
	if open != 1 || minutes != 60 || noOpens != 1 || noCloses != 1 || code != "3VK583" {
		t.Errorf("exam 1 = %d, %d, %d, %d, %q; want 1, 60, 1, 1, \"3VK583\"", open, minutes, noOpens, noCloses, code)
	}
	if after := dumpRows(t, db); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	if strings.Contains(logs.String(), "existing exams given a code") {
		t.Errorf("log = %q, want no code assignment", logs.String())
	}

	clock := time.UnixMilli(1_800_000_000_000)
	students := attempt.NewService(db, func() time.Time { return clock }, rand.Reader)
	if _, err := students.Start(ctx, "3VK583", "New student"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var length int64
	if err := db.QueryRow(`SELECT deadline_at - started_at FROM attempt WHERE student_name = 'New student'`).Scan(&length); err != nil || length != 3600000 {
		t.Errorf("deadline_at - started_at = %d, %v; want 3600000", length, err)
	}
}

// attemptScores returns each attempt's score, counted as the results page
// counts it (internal/attempt/results.go).
func attemptScores(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT (SELECT COUNT(*) FROM answer an JOIN question_option o ON o.id = an.option_id
		WHERE an.attempt_id = a.id AND o.is_correct = 1) FROM attempt a ORDER BY a.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUpgrade_Version6OlympiadShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 6)
	const createdAt = 1_763_340_000_000
	for _, q := range []string{
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
			VALUES (1,'Olimpiade','questions_options','score',1,'k1',%d,1,1,'3VK583')`, createdAt),
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for q := 1; q <= 20; q++ {
		if _, err := old.Exec(`INSERT INTO question (id, exam_id, position, body_html) VALUES (?, 1, ?, ?)`,
			q, q, fmt.Sprintf("<p>Soal nomor %d: berapa %d + %d?</p>\n", q, q, q)); err != nil {
			t.Fatal(err)
		}
		for m := 1; m <= 4; m++ {
			if _, err := old.Exec(`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (?, ?, ?, ?, ?)`,
				(q-1)*4+m, q, m, fmt.Sprintf("<p>%d</p>\n", 2*q+m-1), m == 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range 51 {
		res, err := old.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, status, started_at, deadline_at, finished_at)
			VALUES (1, ?, ?, ?, 'submitted', 10, 20, 15)`, fmt.Sprintf("Student %d", i), []byte{byte(i)}, i)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		id, _ := res.LastInsertId()
		for q := 1; q <= 20; q++ {
			if _, err := old.Exec(`INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, ?, ?)`,
				id, q, (q-1)*4+(i+q)%4+1); err != nil {
				t.Fatal(err)
			}
		}
		if i%10 == 0 {
			if _, err := old.Exec(`INSERT INTO attempt_event (attempt_id, kind, question, at) VALUES (?, 'copy', 1, 12)`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, scores := dumpRows(t, old), attemptScores(t, old)
	old.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exams := newExams(t, db)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := newAdmin(ctx, db, exams, []string{"first@example.test"}, logger); err != nil {
		t.Fatal(err)
	}
	if err := assignExamCodes(ctx, exams, logger); err != nil {
		t.Fatalf("assignExamCodes: %v", err)
	}
	if err := backfillRevisions(ctx, exams, logger); err != nil {
		t.Fatalf("backfillRevisions: %v", err)
	}

	var version string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != "10" {
		t.Fatalf("user_version = %s, %v; want 10", version, err)
	}
	if after := dumpRows(t, db); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	if after := attemptScores(t, db); !slices.Equal(after, scores) {
		t.Errorf("scores = %v, want %v", after, scores)
	}
	revs, err := exams.Revisions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].Number != 1 || !revs[0].CreatedAt.Equal(time.UnixMilli(createdAt)) ||
		revs[0].TeacherEmail != "first@example.test" || revs[0].Summary != "Imported" {
		t.Fatalf("revisions = %+v, want revision 1 Imported by the owner at the exam's creation", revs)
	}
	var unassigned int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempt WHERE revision IS NOT 1`).Scan(&unassigned); err != nil || unassigned != 0 {
		t.Errorf("attempts without revision 1 = %d, %v; want 0", unassigned, err)
	}
	rev, err := exams.Revision(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	live, err := exams.Questions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Questions) != 20 || len(live) != 20 {
		t.Fatalf("revision has %d questions, live %d; want 20", len(rev.Questions), len(live))
	}
	for i, q := range rev.Questions {
		if q.BodyHTML != live[i].BodyHTML || len(q.Options) != len(live[i].Options) {
			t.Fatalf("question %d = %q with %d options, want %q with %d", i+1, q.BodyHTML, len(q.Options), live[i].BodyHTML, len(live[i].Options))
		}
		for j, o := range q.Options {
			if o.BodyHTML != live[i].Options[j].BodyHTML || o.Correct != live[i].Options[j].Correct {
				t.Errorf("question %d option %d = %+v, want %+v", i+1, j+1, o, live[i].Options[j])
			}
		}
	}
	const given = "existing exams given revision 1"
	if !strings.Contains(logs.String(), given) || !strings.Contains(logs.String(), "count=1") ||
		strings.Contains(strings.ToLower(logs.String()), "example.test") {
		t.Errorf("log = %q, want %q with count=1 and no email", logs.String(), given)
	}

	logs.Reset()
	if err := backfillRevisions(ctx, exams, logger); err != nil {
		t.Fatalf("second backfillRevisions: %v", err)
	}
	if logs.Len() != 0 {
		t.Errorf("second call logged %q, want nothing", logs.String())
	}

	clock := time.UnixMilli(1_800_000_000_000)
	students := attempt.NewService(db, func() time.Time { return clock }, rand.Reader)
	if _, err := students.Start(ctx, "3VK583", "New student"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var revision, length int64
	if err := db.QueryRow(`SELECT revision, deadline_at - started_at FROM attempt WHERE student_name = 'New student'`).
		Scan(&revision, &length); err != nil || revision != 1 || length != 3600000 {
		t.Errorf("new attempt: revision %d, deadline_at - started_at = %d, %v; want 1, 3600000", revision, length, err)
	}
}

// TestUpgrade_Version7LiveShape upgrades a database with the shape of the
// live instance at the editor release (an open olympiad with 51 attempts and
// two closed exams) and archives the two closed exams.
func TestUpgrade_Version7LiveShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 7)
	const createdAt = 1_763_340_000_000
	for _, q := range []string{
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
			VALUES (1,'Olimpiade','questions_options','score',1,'k1',%d,1,1,'3VK583')`, createdAt),
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
			VALUES (2,'Schedule check','none','score',0,'k2',%d,0,1,'KSC7RP')`, createdAt+1),
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
			VALUES (3,'Schedule check','none','score',0,'k3',%d,0,1,'4HGBZ9')`, createdAt+2),
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (21, 2, 1, '<p>Q</p>\n'), (22, 3, 1, '<p>Q</p>\n')`,
		`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES
			(81, 21, 1, '<p>a</p>\n', 1), (82, 21, 2, '<p>b</p>\n', 0), (83, 22, 1, '<p>a</p>\n', 1), (84, 22, 2, '<p>b</p>\n', 0)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for q := 1; q <= 20; q++ {
		if _, err := old.Exec(`INSERT INTO question (id, exam_id, position, body_html) VALUES (?, 1, ?, ?)`,
			q, q, fmt.Sprintf("<p>Soal nomor %d: berapa %d + %d?</p>\n", q, q, q)); err != nil {
			t.Fatal(err)
		}
		for m := 1; m <= 4; m++ {
			if _, err := old.Exec(`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (?, ?, ?, ?, ?)`,
				(q-1)*4+m, q, m, fmt.Sprintf("<p>%d</p>\n", 2*q+m-1), m == 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := old.Exec(`INSERT INTO exam_revision
		(exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		SELECT id, 1, created_at, owner_id, 'Imported', title, shuffle, show_result, monitor, duration_minutes, media_key, '{}' FROM exam`); err != nil {
		t.Fatalf("revisions: %v", err)
	}
	for i := range 51 {
		res, err := old.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, status, started_at, deadline_at, finished_at)
			VALUES (1, ?, ?, ?, 'submitted', 10, 20, 15)`, fmt.Sprintf("Student %d", i), []byte{byte(i)}, i)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		id, _ := res.LastInsertId()
		for q := 1; q <= 20; q++ {
			if _, err := old.Exec(`INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, ?, ?)`,
				id, q, (q-1)*4+(i+q)%4+1); err != nil {
				t.Fatal(err)
			}
		}
		if i%10 == 0 {
			if _, err := old.Exec(`INSERT INTO attempt_event (attempt_id, kind, question, at) VALUES (?, 'copy', 1, 12)`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	dumpLater := func(db *sql.DB) string {
		t.Helper()
		return dumpQueries(t, db,
			`SELECT id, code, owner_id, is_open, duration_minutes, opens_at, closes_at FROM exam ORDER BY id`,
			`SELECT id, status, finished_at, revision FROM attempt ORDER BY id`,
			`SELECT exam_id, number, created_at, teacher_id, summary FROM exam_revision ORDER BY exam_id, number`,
		)
	}
	before, later, scores := dumpRows(t, old), dumpLater(old), attemptScores(t, old)
	old.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exams := newExams(t, db)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := newAdmin(ctx, db, exams, []string{"first@example.test"}, logger); err != nil {
		t.Fatal(err)
	}
	if err := assignExamCodes(ctx, exams, logger); err != nil {
		t.Fatalf("assignExamCodes: %v", err)
	}
	if err := backfillRevisions(ctx, exams, logger); err != nil {
		t.Fatalf("backfillRevisions: %v", err)
	}

	var version string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != "10" {
		t.Fatalf("user_version = %s, %v; want 10", version, err)
	}
	unchanged := func(when string) {
		t.Helper()
		if after := dumpRows(t, db); after != before {
			t.Errorf("%s: rows changed:\n%s\nwant\n%s", when, after, before)
		}
		if after := dumpLater(db); after != later {
			t.Errorf("%s: schedule, attempt and revision rows changed:\n%s\nwant\n%s", when, after, later)
		}
		if after := attemptScores(t, db); !slices.Equal(after, scores) {
			t.Errorf("%s: scores = %v, want %v", when, after, scores)
		}
	}
	unchanged("after the upgrade")
	if strings.Contains(logs.String(), "existing exams given") {
		t.Errorf("log = %q, want no backfill", logs.String())
	}

	owner := exam.Actor{TeacherID: 1}
	list, err := exams.List(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("List returned %d exams, want 3", len(list))
	}
	for _, e := range list {
		if e.Archived() {
			t.Errorf("exam %d archived after the upgrade", e.ID)
		}
		if e.ID == 1 && (!e.Open || e.DurationMinutes != 60 || e.AttemptCount != 51 || e.Revision != 1) {
			t.Errorf("olympiad after the upgrade: %+v", e)
		}
	}

	for _, id := range []int64{2, 3} {
		if err := exams.Archive(ctx, owner, id); err != nil {
			t.Fatalf("Archive(%d): %v", id, err)
		}
	}
	unchanged("after archiving exams 2 and 3")

	clock := time.UnixMilli(1_800_000_000_000)
	students := attempt.NewService(db, func() time.Time { return clock }, rand.Reader)
	if _, err := students.Start(ctx, "KSC7RP", "New student"); !errors.Is(err, attempt.ErrNoOpenExam) {
		t.Errorf("Start on archived KSC7RP = %v, want ErrNoOpenExam", err)
	}
	if _, err := students.Start(ctx, "3VK583", "New student"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var revision, length int64
	if err := db.QueryRow(`SELECT revision, deadline_at - started_at FROM attempt WHERE student_name = 'New student'`).
		Scan(&revision, &length); err != nil || revision != 1 || length != 3600000 {
		t.Errorf("new attempt: revision %d, deadline_at - started_at = %d, %v; want 1, 3600000", revision, length, err)
	}
}

// TestUpgrade_Version8LiveShape upgrades a database with the shape of the
// live instance at the archive release (an open olympiad with 51 attempts
// and two archived exams), then imports an exam with sections.
func TestUpgrade_Version8LiveShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 8)
	seedLiveShape(t, old)
	before, older, later, scores := dumpRows(t, old), dumpLiveOlder(t, old), dumpLiveLater(t, old), attemptScores(t, old)
	old.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exams := newExams(t, db)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := newAdmin(ctx, db, exams, []string{"first@example.test"}, logger); err != nil {
		t.Fatal(err)
	}
	if err := assignExamCodes(ctx, exams, logger); err != nil {
		t.Fatalf("assignExamCodes: %v", err)
	}
	if err := backfillRevisions(ctx, exams, logger); err != nil {
		t.Fatalf("backfillRevisions: %v", err)
	}

	var version string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != "10" {
		t.Fatalf("user_version = %s, %v; want 10", version, err)
	}
	if after := dumpRows(t, db); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	unchanged := func(when string) {
		t.Helper()
		if after := dumpLiveOlder(t, db); after != older {
			t.Errorf("%s: rows of the older exams changed:\n%s\nwant\n%s", when, after, older)
		}
		if after := dumpLiveLater(t, db); after != later {
			t.Errorf("%s: schedule, archive, attempt and revision rows changed:\n%s\nwant\n%s", when, after, later)
		}
		if after := attemptScores(t, db); !slices.Equal(after, scores) {
			t.Errorf("%s: scores = %v, want %v", when, after, scores)
		}
		var sectioned int
		if err := db.QueryRow(`SELECT COUNT(*) FROM question WHERE exam_id <= 3 AND (section_title IS NOT NULL OR section_html IS NOT NULL)`).
			Scan(&sectioned); err != nil || sectioned != 0 {
			t.Errorf("%s: older questions with a section column set = %d, %v; want 0", when, sectioned, err)
		}
	}
	unchanged("after the upgrade")
	if strings.Contains(logs.String(), "existing exams given") {
		t.Errorf("log = %q, want no backfill", logs.String())
	}

	owner := exam.Actor{TeacherID: 1}
	const sectionedText = "# Parts\n\n## Part A\nRead **carefully**.\n\n1. First\n- a *\n- b\n\n## Part B\n\n2. Second\n- c\n- d *\n"
	id, err := exams.Import(ctx, owner, []byte(sectionedText))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	qs, err := exams.Questions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].Section == nil || qs[0].Section.Title != "Part A" ||
		qs[1].Section == nil || qs[1].Section.Title != "Part B" {
		t.Errorf("imported questions = %+v, want Part A at 1 and Part B at 2", qs)
	}
	unchanged("after importing an exam with sections")

	clock := time.UnixMilli(1_800_000_000_000)
	students := attempt.NewService(db, func() time.Time { return clock }, rand.Reader)
	if _, err := students.Start(ctx, "3VK583", "New student"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var revision, length int64
	if err := db.QueryRow(`SELECT revision, deadline_at - started_at FROM attempt WHERE student_name = 'New student'`).
		Scan(&revision, &length); err != nil || revision != 1 || length != 3600000 {
		t.Errorf("new attempt: revision %d, deadline_at - started_at = %d, %v; want 1, 3600000", revision, length, err)
	}
}

// liveAttempts is the number of attempts seedLiveShape writes, ids 1 to 51.
const liveAttempts = 51

// seedLiveShape fills old with the shape of the live instance at the
// archive release: an open olympiad (exam 1, code 3VK583) of 20 questions
// with liveAttempts submitted attempts, and two archived exams.
func seedLiveShape(t *testing.T, old *sql.DB) {
	t.Helper()
	const createdAt = 1_763_340_000_000
	for _, q := range []string{
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code)
			VALUES (1,'Olimpiade','questions_options','score',1,'k1',%d,1,1,'3VK583')`, createdAt),
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code, archived_at)
			VALUES (2,'Schedule check','none','score',0,'k2',%d,0,1,'KSC7RP',%d)`, createdAt+1, createdAt+10),
		fmt.Sprintf(`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code, archived_at)
			VALUES (3,'Schedule check','none','score',0,'k3',%d,0,1,'4HGBZ9',%d)`, createdAt+2, createdAt+11),
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (21, 2, 1, '<p>Q</p>\n'), (22, 3, 1, '<p>Q</p>\n')`,
		`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES
			(81, 21, 1, '<p>a</p>\n', 1), (82, 21, 2, '<p>b</p>\n', 0), (83, 22, 1, '<p>a</p>\n', 1), (84, 22, 2, '<p>b</p>\n', 0)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for q := 1; q <= 20; q++ {
		if _, err := old.Exec(`INSERT INTO question (id, exam_id, position, body_html) VALUES (?, 1, ?, ?)`,
			q, q, fmt.Sprintf("<p>Soal nomor %d: berapa %d + %d?</p>\n", q, q, q)); err != nil {
			t.Fatal(err)
		}
		for m := 1; m <= 4; m++ {
			if _, err := old.Exec(`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (?, ?, ?, ?, ?)`,
				(q-1)*4+m, q, m, fmt.Sprintf("<p>%d</p>\n", 2*q+m-1), m == 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := old.Exec(`INSERT INTO exam_revision
		(exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		SELECT id, 1, created_at, owner_id, 'Imported', title, shuffle, show_result, monitor, duration_minutes, media_key, '{}' FROM exam`); err != nil {
		t.Fatalf("revisions: %v", err)
	}
	for i := range liveAttempts {
		res, err := old.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, status, started_at, deadline_at, finished_at)
			VALUES (1, ?, ?, ?, 'submitted', 10, 20, 15)`, fmt.Sprintf("Student %d", i), []byte{byte(i)}, i)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		id, _ := res.LastInsertId()
		for q := 1; q <= 20; q++ {
			if _, err := old.Exec(`INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, ?, ?)`,
				id, q, (q-1)*4+(i+q)%4+1); err != nil {
				t.Fatal(err)
			}
		}
		if i%10 == 0 {
			if _, err := old.Exec(`INSERT INTO attempt_event (attempt_id, kind, question, at) VALUES (?, 'copy', 1, 12)`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// dumpLiveOlder renders the rows of the three exams and the attempts of
// seedLiveShape, by id; an exam imported or an attempt started later has a
// higher id and is left out.
func dumpLiveOlder(t *testing.T, db *sql.DB) string {
	t.Helper()
	return dumpQueries(t, db,
		`SELECT id, title, shuffle, show_result, monitor, is_open, media_key, created_at FROM exam WHERE id <= 3 ORDER BY id`,
		`SELECT id, exam_id, position, body_html FROM question WHERE exam_id <= 3 ORDER BY id`,
		`SELECT o.id, o.question_id, o.position, o.body_html, o.is_correct FROM question_option o
			JOIN question q ON q.id = o.question_id WHERE q.exam_id <= 3 ORDER BY o.id`,
		fmt.Sprintf(`SELECT id, exam_id, student_name, hex(token_hash), resume_code, shuffle_seed, started_at, deadline_at FROM attempt WHERE id <= %d ORDER BY id`, liveAttempts),
		fmt.Sprintf(`SELECT attempt_id, question_id, option_id FROM answer WHERE attempt_id <= %d ORDER BY attempt_id, question_id`, liveAttempts),
		fmt.Sprintf(`SELECT id, attempt_id, kind, question, at FROM attempt_event WHERE attempt_id <= %d ORDER BY id`, liveAttempts),
	)
}

// dumpLiveLater renders the columns of seedLiveShape's exams, attempts and
// revisions that later releases added.
func dumpLiveLater(t *testing.T, db *sql.DB) string {
	t.Helper()
	return dumpQueries(t, db,
		`SELECT id, code, owner_id, is_open, duration_minutes, opens_at, closes_at, archived_at FROM exam WHERE id <= 3 ORDER BY id`,
		fmt.Sprintf(`SELECT id, status, finished_at, revision FROM attempt WHERE id <= %d ORDER BY id`, liveAttempts),
		`SELECT exam_id, number, created_at, teacher_id, summary FROM exam_revision WHERE exam_id <= 3 ORDER BY exam_id, number`,
	)
}

// TestUpgrade_Version9LiveShape upgrades a database with the shape of the
// live instance at the sections release, then imports a fill-in exam and
// starts an attempt on the olympiad.
func TestUpgrade_Version9LiveShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 9)
	seedLiveShape(t, old)
	before, older, later, scores := dumpRows(t, old), dumpLiveOlder(t, old), dumpLiveLater(t, old), attemptScores(t, old)
	old.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exams := newExams(t, db)
	if _, err := newAdmin(ctx, db, exams, []string{"first@example.test"}, discardLogger); err != nil {
		t.Fatal(err)
	}
	if err := assignExamCodes(ctx, exams, discardLogger); err != nil {
		t.Fatalf("assignExamCodes: %v", err)
	}
	if err := backfillRevisions(ctx, exams, discardLogger); err != nil {
		t.Fatalf("backfillRevisions: %v", err)
	}

	var version string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != "10" {
		t.Fatalf("user_version = %s, %v; want 10", version, err)
	}
	if after := dumpRows(t, db); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	var fillIn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM question WHERE accepted_answers IS NOT NULL`).Scan(&fillIn); err != nil || fillIn != 0 {
		t.Errorf("questions with accepted answers after the upgrade = %d, %v; want 0", fillIn, err)
	}
	unchanged := func(when string) {
		t.Helper()
		if after := dumpLiveOlder(t, db); after != older {
			t.Errorf("%s: rows of the older exams changed:\n%s\nwant\n%s", when, after, older)
		}
		if after := dumpLiveLater(t, db); after != later {
			t.Errorf("%s: schedule, archive, attempt and revision rows changed:\n%s\nwant\n%s", when, after, later)
		}
		if after := attemptScores(t, db); len(after) < len(scores) || !slices.Equal(after[:len(scores)], scores) {
			t.Errorf("%s: scores = %v, want %v first", when, after, scores)
		}
		var typed, fillIn int
		if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM typed_answer),
			(SELECT COUNT(*) FROM question WHERE exam_id <= 3 AND accepted_answers IS NOT NULL)`).
			Scan(&typed, &fillIn); err != nil || typed != 0 || fillIn != 0 {
			t.Errorf("%s: typed answers = %d, older questions with accepted answers = %d, %v; want 0, 0", when, typed, fillIn, err)
		}
	}
	unchanged("after the upgrade")

	clock := time.UnixMilli(1_800_000_000_000)
	students := attempt.NewService(db, func() time.Time { return clock }, rand.Reader)
	results, err := students.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results.Attempts) != len(scores) {
		t.Fatalf("List gives %d attempts, want %d", len(results.Attempts), len(scores))
	}
	for i, a := range results.Attempts {
		if a.ID != int64(i+1) || a.Score != scores[i] {
			t.Errorf("List attempt %d: id %d, score %d; want id %d, score %d", i, a.ID, a.Score, i+1, scores[i])
		}
	}

	owner := exam.Actor{TeacherID: 1}
	const fillInText = "# Isian\n\n1. Pilih\n- a *\n- b\n\n2. Ibu kota ....\n= Jakarta\n= DKI Jakarta\n"
	id, err := exams.Import(ctx, owner, []byte(fillInText))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	qs, err := exams.Questions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].FillIn() || !qs[1].FillIn() || !slices.Equal(qs[1].Answers, []string{"Jakarta", "DKI Jakarta"}) {
		t.Errorf("imported questions = %+v, want multiple choice then fill-in", qs)
	}
	unchanged("after importing a fill-in exam")

	if _, err := students.Start(ctx, "3VK583", "New student"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var revision int64
	if err := db.QueryRow(`SELECT revision FROM attempt WHERE student_name = 'New student'`).Scan(&revision); err != nil || revision != 1 {
		t.Errorf("new attempt revision = %d, %v; want 1", revision, err)
	}
	unchanged("after starting an attempt")
}

func TestBackfillRevisions_LogsUnconvertible(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "answerbox.db")
	old := openOldDatabase(t, path, 6)
	for _, q := range []string{
		`INSERT INTO teacher (id, email, created_at) VALUES (1,'first@example.test',0)`,
		`INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code) VALUES (1,'Live','none','score',1,'k1',5,1,1,'3VK583')`,
		`INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`,
		`INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	old.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exams := newExams(t, db)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := backfillRevisions(ctx, exams, logger); err != nil {
		t.Fatalf("backfillRevisions: %v", err)
	}
	const unconverted = "exam has no revision: its stored text could not be rebuilt"
	if !strings.Contains(logs.String(), unconverted) || !strings.Contains(logs.String(), "exam_id=1") ||
		!strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "Live") ||
		strings.Contains(logs.String(), "example.test") {
		t.Errorf("log = %q, want the error line for exam_id=1 without title or email", logs.String())
	}
	e, err := exams.Get(ctx, exam.Actor{TeacherID: 1}, 1)
	if err != nil || e.Title != "Live" || e.Revision != 0 {
		t.Errorf("Get = %+v, %v; want the exam without a revision", e, err)
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
			t.Errorf("cmd/answerbox links %s", strings.TrimSpace(line))
		}
	}
}

func TestCheckOldDatabase(t *testing.T) {
	refused := func(dir, found string) string {
		return dir + " holds " + found + " from before the rename to Answerbox: stop the app and rename go-exam.db to answerbox.db, go-exam.db-wal to answerbox.db-wal and go-exam.db-shm to answerbox.db-shm (each one that exists)"
	}
	tests := []struct {
		name    string
		missing bool
		files   []string
		found   string // empty: the check passes
	}{
		{name: "empty directory"},
		{name: "missing directory", missing: true},
		{name: "old main file", files: []string{"go-exam.db"}, found: "go-exam.db"},
		{
			name:  "old main file and sidecars",
			files: []string{"go-exam.db", "go-exam.db-wal", "go-exam.db-shm"},
			found: "go-exam.db, go-exam.db-wal, go-exam.db-shm",
		},
		{name: "new main file", files: []string{"answerbox.db"}},
		{name: "both main files", files: []string{"answerbox.db", "go-exam.db"}},
		{name: "orphan old wal", files: []string{"answerbox.db", "go-exam.db-wal"}, found: "go-exam.db-wal"},
		{name: "old wal beside new wal", files: []string{"answerbox.db", "answerbox.db-wal", "go-exam.db-wal"}},
		{name: "orphan old shm", files: []string{"answerbox.db", "go-exam.db-shm"}, found: "go-exam.db-shm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.missing {
				dir = filepath.Join(dir, "data")
			}
			for _, name := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := listDir(t, dir)

			err := checkOldDatabase(dir)

			if tt.found == "" {
				if err != nil {
					t.Errorf("checkOldDatabase = %v, want nil", err)
				}
			} else if err == nil || err.Error() != refused(dir, tt.found) {
				t.Errorf("checkOldDatabase = %v, want %q", err, refused(dir, tt.found))
			}
			if after := listDir(t, dir); !slices.Equal(after, before) {
				t.Errorf("directory after the check = %q, want %q", after, before)
			}
		})
	}
}

// listDir returns the names in dir, or nil when dir does not exist.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
