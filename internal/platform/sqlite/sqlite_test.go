package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
	"github.com/fiatcode-gh/go-exam/internal/shortcode"
)

func openFile(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func pragma(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func mustExec(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// ownerTeacher inserts the teacher that hand-written exam rows belong to.
const ownerTeacher = `INSERT INTO teacher (id, email, created_at) VALUES (1, 'owner@example.test', 0)`

// openVersion2 creates a database file at path as the monitor release left
// it (migrations 0001 and 0002, user_version 2) and returns a raw handle.
func openVersion2(t *testing.T, path string) *sql.DB {
	t.Helper()
	return openBuilt(t, path, 2)
}

// openVersion4 creates a database file at path as the teachers release left
// it (migrations 0001 to 0004, user_version 4) and returns a raw handle.
func openVersion4(t *testing.T, path string) *sql.DB {
	t.Helper()
	return openBuilt(t, path, 4)
}

// openVersion5 creates a database file at path as the exam-code release left
// it (migrations 0001 to 0005, user_version 5) and returns a raw handle.
func openVersion5(t *testing.T, path string) *sql.DB {
	t.Helper()
	return openBuilt(t, path, 5)
}

// openVersion6 creates a database file at path as the schedule release left
// it (migrations 0001 to 0006, user_version 6) and returns a raw handle.
func openVersion6(t *testing.T, path string) *sql.DB {
	t.Helper()
	return openBuilt(t, path, 6)
}

// openBuilt applies the first version migrations by hand to a new file at
// path, sets user_version and returns a raw handle.
func openBuilt(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	old, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names[:version] {
		schema, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, old, string(schema))
	}
	mustExec(t, old, fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return old
}

func TestOpen_WAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	db := openFile(t, path)

	for name, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "5000",
		"synchronous":  "1", // NORMAL
		"temp_store":   "2", // MEMORY
	} {
		if got := pragma(t, db, name); got != want {
			t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
		}
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}

	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('t','none','score','k1',0,1,'AAAAAA')`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	mustExec(t, tx, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('u','none','score','k2',0,1,'BBBBBB')`)

	// A reader on another handle (as sqlite3 .backup would be) is not blocked by the open write transaction.
	reader := openFile(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM exam`).Scan(&n); err != nil {
		t.Fatalf("reader SELECT during write tx: %v", err)
	}
	if n != 1 {
		t.Errorf("reader sees %d exams, want 1 (committed only)", n)
	}
}

func TestOpen_MigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Errorf("user_version = %s, want 7", got)
	}
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('t','none','score','k1',0,1,'AAAAAA')`)
	db.Close()

	db = openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Errorf("user_version after reopen = %s, want 7", got)
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM exam`).Scan(&title); err != nil || title != "t" {
		t.Fatalf("after reopen: title=%q err=%v", title, err)
	}
}

// TestMigrate_0002KeepsVersion1Data opens a database as the first release
// left it (schema 0001 with an exam, an attempt and an answer) and checks that
// the monitor migration keeps every row, turns the monitor on for the
// existing exam and leaves the freeze triggers working.
func TestMigrate_0002KeepsVersion1Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	schema, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, old, string(schema))
	mustExec(t, old, `PRAGMA user_version = 1`)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at) VALUES (1,'Old','none','score',1,'k1',5)`)
	mustExec(t, old, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`)
	mustExec(t, old, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`)
	mustExec(t, old, `INSERT INTO attempt (id, exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at) VALUES (1,1,'Ayu',x'01','ABCDEFGH',7,10,20)`)
	mustExec(t, old, `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (1,1,2)`)
	old.Close()

	db := openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Fatalf("user_version = %s, want 7", got)
	}
	var (
		title, name, code string
		monitor, option   int
	)
	err = db.QueryRow(`SELECT e.title, e.monitor, a.student_name, a.resume_code, an.option_id
		FROM exam e JOIN attempt a ON a.exam_id = e.id JOIN answer an ON an.attempt_id = a.id`).
		Scan(&title, &monitor, &name, &code, &option)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Old" || monitor != 1 || name != "Ayu" || code != "ABCDEFGH" || option != 2 {
		t.Fatalf("after migration: %q monitor=%d %q %q option=%d", title, monitor, name, code, option)
	}
	if _, err := db.Exec(`UPDATE question_option SET is_correct = 0 WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "exam is frozen") {
		t.Fatalf("freeze trigger after migration: err = %v", err)
	}
	mustExec(t, db, `INSERT INTO attempt_event (attempt_id, kind, question, at) VALUES (1,'left_page',1,15)`)
	if _, err := db.Exec(`UPDATE exam SET monitor = 2 WHERE id = 1`); err == nil {
		t.Fatal("monitor = 2 accepted")
	}
	mustExec(t, db, `DELETE FROM attempt WHERE id = 1`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempt_event`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("events after attempt delete = %d, %v; want 0", n, err)
	}
}

// TestMigrate_0003KeepsVersion2Data opens a database as the monitor release
// left it and checks that the teacher migration keeps every exam row, drops
// the ownerless sessions and makes sessions belong to a teacher.
func TestMigrate_0003KeepsVersion2Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	old := openVersion2(t, path)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor) VALUES (1,'Old','none','score',1,'k1',5,0)`)
	mustExec(t, old, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`)
	mustExec(t, old, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`)
	mustExec(t, old, `INSERT INTO attempt (id, exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at) VALUES (1,1,'Ayu',x'01','ABCDEFGH',7,10,20)`)
	mustExec(t, old, `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (1,1,2)`)
	mustExec(t, old, `INSERT INTO attempt_event (id, attempt_id, kind, question, at) VALUES (1,1,'copy',1,15)`)
	mustExec(t, old, `INSERT INTO admin_session (token_hash, csrf_token, expires_at) VALUES (x'02','c',99)`)
	old.Close()

	db := openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Fatalf("user_version = %s, want 7", got)
	}
	var (
		title, name, code, kind  string
		open, monitor, option, n int
	)
	err := db.QueryRow(`SELECT e.title, e.is_open, e.monitor, a.student_name, a.resume_code, an.option_id, ev.kind
		FROM exam e JOIN attempt a ON a.exam_id = e.id JOIN answer an ON an.attempt_id = a.id
		JOIN attempt_event ev ON ev.attempt_id = a.id`).
		Scan(&title, &open, &monitor, &name, &code, &option, &kind)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Old" || open != 1 || monitor != 0 || name != "Ayu" || code != "ABCDEFGH" || option != 2 || kind != "copy" {
		t.Fatalf("after migration: %q open=%d monitor=%d %q %q option=%d %q", title, open, monitor, name, code, option, kind)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_session`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("admin_session rows = %d, %v; want 0", n, err)
	}
	if _, err := db.Exec(`INSERT INTO admin_session (token_hash, csrf_token, expires_at) VALUES (x'03','c',99)`); err == nil {
		t.Fatal("session without a teacher accepted")
	}
	if _, err := db.Exec(`INSERT INTO admin_session (token_hash, teacher_id, csrf_token, expires_at) VALUES (x'03',42,'c',99)`); err == nil {
		t.Fatal("session for an unknown teacher accepted")
	}

	for _, bad := range []struct{ name, sql string }{
		{"upper case email", `INSERT INTO teacher (email, created_at) VALUES ('Ada@Example.Test', 0)`},
		{"empty email", `INSERT INTO teacher (email, created_at) VALUES ('', 0)`},
		{"allowed = 2", `INSERT INTO teacher (email, allowed, created_at) VALUES ('b@example.test', 2, 0)`},
	} {
		if _, err := db.Exec(bad.sql); err == nil {
			t.Errorf("%s accepted", bad.name)
		}
	}
	id := sqlitetest.Teacher(t, db, "ada@example.test")
	if _, err := db.Exec(`INSERT INTO teacher (email, created_at) VALUES ('ada@example.test', 0)`); err == nil {
		t.Error("repeated email accepted")
	}
	var allowed int
	if err := db.QueryRow(`SELECT allowed FROM teacher WHERE id = ?`, id).Scan(&allowed); err != nil || allowed != 1 {
		t.Fatalf("new teacher allowed = %d, %v; want 1", allowed, err)
	}
	mustExec(t, db, `INSERT INTO admin_session (token_hash, teacher_id, csrf_token, expires_at) VALUES (x'03',?,'c',99)`, id)
	mustExec(t, db, `DELETE FROM teacher WHERE id = ?`, id)
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_session`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions after teacher delete = %d, %v; want 0", n, err)
	}
}

// TestMigrate_0004KeepsExamsAndRequiresOwner opens a version-2 database with
// an open exam and an attempt and checks that the ownership migration keeps
// the exam as it was (owner still unset until startup assigns one) and that
// from then on an exam cannot be stored or left without an owner.
func TestMigrate_0004KeepsExamsAndRequiresOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	old := openVersion2(t, path)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor) VALUES (1,'Old','none','score',1,'k1',5,0)`)
	mustExec(t, old, `INSERT INTO attempt (id, exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at) VALUES (1,1,'Ayu',x'01','ABCDEFGH',7,10,20)`)
	old.Close()

	db := openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Fatalf("user_version = %s, want 7", got)
	}
	var (
		title, student string
		open           int
		owner          sql.NullInt64
	)
	err := db.QueryRow(`SELECT e.title, e.is_open, e.owner_id, a.student_name FROM exam e JOIN attempt a ON a.exam_id = e.id`).
		Scan(&title, &open, &owner, &student)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Old" || open != 1 || owner.Valid || student != "Ayu" {
		t.Fatalf("after migration: %q open=%d owner=%v %q", title, open, owner, student)
	}

	const needsOwner = "exam needs an owner"
	_, err = db.Exec(`INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, code) VALUES (2,'New','none','score','k2',0,'AAAAAA')`)
	if err == nil || !strings.Contains(err.Error(), needsOwner) {
		t.Fatalf("insert without owner: err = %v, want %q", err, needsOwner)
	}
	id := sqlitetest.Teacher(t, db, "ada@example.test")
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (2,'New','none','score','k2',0,?,'AAAAAA')`, id)
	_, err = db.Exec(`UPDATE exam SET owner_id = NULL WHERE id = 2`)
	if err == nil || !strings.Contains(err.Error(), needsOwner) {
		t.Fatalf("clearing the owner: err = %v, want %q", err, needsOwner)
	}
	mustExec(t, db, `UPDATE exam SET owner_id = ? WHERE id = 1`, id)
	if _, err := db.Exec(`UPDATE exam SET owner_id = 42 WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("unknown owner: err = %v, want a foreign key error", err)
	}
}

func TestSchema_SeveralOpenExams(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, owner_id, code) VALUES (1,'a','none','score',1,'k1',0,1,'AAAAAA')`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, owner_id, code) VALUES (2,'b','none','score',1,'k2',0,1,'BBBBBB')`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'exam_one_open'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("exam_one_open index count = %d, %v; want 0", n, err)
	}
}

// TestMigrate_0005KeepsExamsAndAddsCodes opens a version-4 database with an
// open exam, an attempt and an answer and checks that the exam-code migration
// keeps every row (the code still unset until startup gives one) and that
// from then on an exam cannot be stored without a code, a code is unique and
// a set code never changes.
func TestMigrate_0005KeepsExamsAndAddsCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	old := openVersion4(t, path)
	mustExec(t, old, ownerTeacher)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id) VALUES (1,'Old','none','score',1,'k1',5,0,1)`)
	mustExec(t, old, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`)
	mustExec(t, old, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`)
	mustExec(t, old, `INSERT INTO attempt (id, exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at) VALUES (1,1,'Ayu',x'01','ABCDEFGH',7,10,20)`)
	mustExec(t, old, `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (1,1,2)`)
	old.Close()

	db := openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Fatalf("user_version = %s, want 7", got)
	}
	var (
		title, student string
		open, option   int
		owner          int64
		code           sql.NullString
	)
	err := db.QueryRow(`SELECT e.title, e.is_open, e.owner_id, e.code, a.student_name, an.option_id
		FROM exam e JOIN attempt a ON a.exam_id = e.id JOIN answer an ON an.attempt_id = a.id`).
		Scan(&title, &open, &owner, &code, &student, &option)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Old" || open != 1 || owner != 1 || code.Valid || student != "Ayu" || option != 2 {
		t.Fatalf("after migration: %q open=%d owner=%d code=%v %q option=%d", title, open, owner, code, student, option)
	}

	const needsCode = "exam needs a code"
	_, err = db.Exec(`INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id) VALUES ('New','none','score','k2',0,1)`)
	if err == nil || !strings.Contains(err.Error(), needsCode) {
		t.Fatalf("insert without code: err = %v, want %q", err, needsCode)
	}
	mustExec(t, db, `UPDATE exam SET code = 'ABCDEF' WHERE id = 1`)
	_, err = db.Exec(`INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('New','none','score','k3',0,1,'ABCDEF')`)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("repeated code: err = %v, want a UNIQUE constraint error", err)
	}
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, is_open, media_key, created_at, owner_id, code) VALUES ('New','none','score',1,'k4',0,1,'GHJKMN')`)
	const fixed = "exam code is fixed"
	for _, q := range []string{
		`UPDATE exam SET code = 'ZZZZZZ' WHERE id = 1`,
		`UPDATE exam SET code = NULL WHERE id = 1`,
	} {
		if _, err := db.Exec(q); err == nil || !strings.Contains(err.Error(), fixed) {
			t.Errorf("%s: err = %v, want %q", q, err, fixed)
		}
	}
	mustExec(t, db, `UPDATE exam SET title = 'x' WHERE id = 1`)
}

// TestMigrate_0006KeepsExamsAt60MinutesWithoutSchedule opens a version-5
// database with an open and a closed exam, an attempt and an answer and checks
// that the schedule migration keeps every row, gives both exams 60 minutes and
// no schedule, and leaves the button state as it was.
func TestMigrate_0006KeepsExamsAt60MinutesWithoutSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	old := openVersion5(t, path)
	mustExec(t, old, ownerTeacher)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code) VALUES (1,'Live','none','score',1,'k1',5,0,1,'3VK583')`)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code) VALUES (2,'Draft','none','score',0,'k2',6,0,1,'ABCDEF')`)
	mustExec(t, old, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`)
	mustExec(t, old, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`)
	mustExec(t, old, `INSERT INTO attempt (id, exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at) VALUES (1,1,'Ayu',x'01','ABCDEFGH',7,10,20)`)
	mustExec(t, old, `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (1,1,2)`)
	old.Close()

	db := openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Fatalf("user_version = %s, want 7", got)
	}
	for id, wantOpen := range map[int64]int{1: 1, 2: 0} {
		var open, minutes, noOpens, noCloses int
		err := db.QueryRow(`SELECT is_open, duration_minutes, opens_at IS NULL, closes_at IS NULL FROM exam WHERE id = ?`, id).
			Scan(&open, &minutes, &noOpens, &noCloses)
		if err != nil {
			t.Fatal(err)
		}
		if open != wantOpen || minutes != 60 || noOpens != 1 || noCloses != 1 {
			t.Errorf("exam %d: is_open=%d duration_minutes=%d opens_at null=%d closes_at null=%d; want %d, 60, 1, 1",
				id, open, minutes, noOpens, noCloses, wantOpen)
		}
	}
	var started, deadline, option int64
	err := db.QueryRow(`SELECT a.started_at, a.deadline_at, an.option_id FROM attempt a JOIN answer an ON an.attempt_id = a.id WHERE a.id = 1`).
		Scan(&started, &deadline, &option)
	if err != nil {
		t.Fatal(err)
	}
	if started != 10 || deadline != 20 || option != 2 {
		t.Errorf("attempt after migration: started_at=%d deadline_at=%d option=%d; want 10, 20, 2", started, deadline, option)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'exam_duration_frozen'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("exam_duration_frozen trigger count = %d, %v; want 1", n, err)
	}
}

// wantErr fails the test unless err contains want.
func wantErr(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: err = %v, want %q", what, err, want)
	}
}

func TestSchema_DurationLimitsAndFreeze(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	var minutes int
	if err := db.QueryRow(`SELECT duration_minutes FROM exam WHERE id = 1`).Scan(&minutes); err != nil || minutes != 60 {
		t.Fatalf("default duration_minutes = %d, %v; want 60", minutes, err)
	}

	for _, bad := range []int{4, 241} {
		_, err := db.Exec(`INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code, duration_minutes) VALUES ('b','none','score','k2',0,1,'BBBBBB',?)`, bad)
		wantErr(t, fmt.Sprintf("insert duration_minutes = %d", bad), err, "CHECK")
		_, err = db.Exec(`UPDATE exam SET duration_minutes = ? WHERE id = 1`, bad)
		wantErr(t, fmt.Sprintf("update duration_minutes = %d", bad), err, "CHECK")
	}
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code, duration_minutes) VALUES (2,'b','none','score','k2',0,1,'BBBBBB',5)`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code, duration_minutes) VALUES (3,'c','none','score','k3',0,1,'CCCCCC',240)`)
	mustExec(t, db, `UPDATE exam SET duration_minutes = 5 WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET duration_minutes = 240 WHERE id = 1`)

	mustExec(t, db, `INSERT INTO attempt (id, exam_id, student_name, token_hash, shuffle_seed, started_at, deadline_at) VALUES (1,1,'s',x'01',0,0,0)`)
	_, err := db.Exec(`UPDATE exam SET duration_minutes = 30 WHERE id = 1`)
	wantErr(t, "length change with attempt", err, "exam is frozen")
	mustExec(t, db, `UPDATE exam SET duration_minutes = 240, title = 'x' WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET title = 'y' WHERE id = 1`)

	mustExec(t, db, `DELETE FROM attempt WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET duration_minutes = 30 WHERE id = 1`)
}

func TestSchema_ScheduleChecks(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, owner_id, code) VALUES (1,'closed','none','score',0,'k1',0,1,'AAAAAA')`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, owner_id, code) VALUES (2,'open','none','score',1,'k2',0,1,'BBBBBB')`)

	const setTimes = `UPDATE exam SET opens_at = ?, closes_at = ? WHERE id = 1`
	for _, c := range []struct {
		opens, closes any
		ok            bool
	}{
		{100, nil, true},
		{nil, 100, true},
		{100, 100, false},
		{100, 99, false},
		{100, 101, true},
	} {
		_, err := db.Exec(setTimes, c.opens, c.closes)
		what := fmt.Sprintf("closed exam opens_at=%v closes_at=%v", c.opens, c.closes)
		if c.ok {
			if err != nil {
				t.Errorf("%s: %v", what, err)
			}
		} else {
			wantErr(t, what, err, "CHECK")
		}
	}
	_, err := db.Exec(`UPDATE exam SET is_open = 1 WHERE id = 1`)
	wantErr(t, "is_open = 1 on a scheduled exam", err, "CHECK")
	mustExec(t, db, `UPDATE exam SET is_open = 1, opens_at = NULL, closes_at = NULL WHERE id = 1`)

	_, err = db.Exec(`UPDATE exam SET opens_at = 100 WHERE id = 2`)
	wantErr(t, "opens_at on an open exam", err, "CHECK")
	_, err = db.Exec(`UPDATE exam SET closes_at = 100 WHERE id = 2`)
	wantErr(t, "closes_at on an open exam", err, "CHECK")
}

// TestSchema_CodeFormat keeps the schema's code check in step with
// shortcode.Alphabet.
func TestSchema_CodeFormat(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	insert := func(key, code string) error {
		_, err := db.Exec(`INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('t','none','score',?,0,1,?)`, key, code)
		return err
	}
	symbols := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range len(symbols) {
		c := symbols[i]
		err := insert("k"+string(c), "AAAAA"+string(c))
		if want := strings.IndexByte(shortcode.Alphabet, c) >= 0; want != (err == nil) {
			t.Errorf("code AAAAA%c: err = %v, want accepted = %v", c, err, want)
		}
	}
	for _, bad := range []string{"abcdef", "ABCDE", "ABCDEFG", "ABC-DE", ""} {
		if err := insert("bad"+bad, bad); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("code %q: err = %v, want a CHECK constraint error", bad, err)
		}
	}
}

func TestSchema_FreezeTriggers(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	mustExec(t, db, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`)
	mustExec(t, db, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1)`)

	ops := []struct{ name, sql string }{
		{"insert question", `INSERT INTO question (id, exam_id, position, body_html) VALUES (2,1,2,'q2')`},
		{"update question", `UPDATE question SET body_html = 'q1b' WHERE id = 1`},
		{"insert option", `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (2,1,2,'o2',0)`},
		{"update option", `UPDATE question_option SET is_correct = 0 WHERE id = 1`},
		{"delete option", `DELETE FROM question_option WHERE id = 2`},
		{"delete question", `DELETE FROM question WHERE id = 2`},
	}
	runUnfrozen := func() {
		t.Helper()
		for _, op := range ops {
			if _, err := db.Exec(op.sql); err != nil {
				t.Errorf("%s without attempt: %v", op.name, err)
			}
		}
	}

	runUnfrozen()
	// Restore the rows the unfrozen run deleted so every frozen op targets an existing row.
	mustExec(t, db, `INSERT INTO question (id, exam_id, position, body_html) VALUES (2,1,2,'q2')`)
	mustExec(t, db, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (2,1,2,'o2',0)`)

	mustExec(t, db, `INSERT INTO attempt (id, exam_id, student_name, token_hash, shuffle_seed, started_at, deadline_at) VALUES (1,1,'s',x'01',0,0,0)`)
	frozenOps := []struct{ name, sql string }{
		{"insert question", `INSERT INTO question (id, exam_id, position, body_html) VALUES (3,1,3,'q3')`},
		{"update question", `UPDATE question SET body_html = 'q1c' WHERE id = 1`},
		{"delete question", `DELETE FROM question WHERE id = 2`},
		{"insert option", `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (3,1,3,'o3',0)`},
		{"update option", `UPDATE question_option SET is_correct = 0 WHERE id = 1`},
		{"delete option", `DELETE FROM question_option WHERE id = 2`},
	}
	for _, op := range frozenOps {
		_, err := db.Exec(op.sql)
		if err == nil || !strings.Contains(err.Error(), "exam is frozen") {
			t.Errorf("%s with attempt: err = %v, want 'exam is frozen'", op.name, err)
		}
	}

	mustExec(t, db, `DELETE FROM attempt WHERE id = 1`)
	mustExec(t, db, `DELETE FROM question_option WHERE id = 2`)
	mustExec(t, db, `DELETE FROM question WHERE id = 2`)
	runUnfrozen()
}

// tableColumns returns the columns of every table in db, by table name.
func tableColumns(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT m.name, c.name FROM sqlite_master m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name, c.cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		columns[table] = append(columns[table], column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

// dumpRows renders every row of the given tables and columns, in a fixed
// order, so two dumps of the same data are equal.
func dumpRows(t *testing.T, db *sql.DB, columns map[string][]string) string {
	t.Helper()
	tables := make([]string, 0, len(columns))
	for table := range columns {
		tables = append(tables, table)
	}
	slices.Sort(tables)
	var b strings.Builder
	for _, table := range tables {
		list := strings.Join(columns[table], ", ")
		rows, err := db.Query(fmt.Sprintf(`SELECT %s FROM %s ORDER BY %s`, list, table, list))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns[table]))
			targets := make([]any, len(values))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s %v\n", table, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return b.String()
}

// addRevision stores the next revision of examID with the exam's current
// content settings, as the edit path will.
func addRevision(db *sql.DB, examID, number int64) error {
	_, err := db.Exec(`INSERT INTO exam_revision
		(exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		SELECT id, ?, 0, owner_id, 'edit', title, shuffle, show_result, monitor, duration_minutes, media_key, '{}' FROM exam WHERE id = ?`,
		number, examID)
	return err
}

// insertAttempt starts an attempt on examID without naming a revision and
// returns its id.
func insertAttempt(t *testing.T, db *sql.DB, examID int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, started_at, deadline_at)
		VALUES (?, 's', randomblob(16), 0, 0, 0)`, examID)
	if err != nil {
		t.Fatalf("insert attempt on exam %d: %v", examID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// attemptRevision reads attempt id's revision; NULL reads as 0.
func attemptRevision(t *testing.T, db *sql.DB, id int64) int64 {
	t.Helper()
	var rev sql.NullInt64
	if err := db.QueryRow(`SELECT revision FROM attempt WHERE id = ?`, id).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev.Int64
}

// TestMigrate_0007KeepsVersion6Data opens a database as the schedule release
// left it and checks that the revision migration keeps every row as it was,
// adds no revision and leaves every attempt without one until the startup
// backfill.
func TestMigrate_0007KeepsVersion6Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go-exam.db")
	old := openVersion6(t, path)
	mustExec(t, old, ownerTeacher)
	mustExec(t, old, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, monitor, owner_id, code, duration_minutes) VALUES (1,'Live','questions','review',1,'k1',5,1,1,'3VK583',45)`)
	mustExec(t, old, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1'),(2,1,2,'q2')`)
	mustExec(t, old, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0),(3,2,1,'o3',0),(4,2,2,'o4',1)`)
	for id := 1; id <= 3; id++ {
		mustExec(t, old, `INSERT INTO attempt (id, exam_id, student_name, token_hash, shuffle_seed, status, started_at, deadline_at, finished_at)
			VALUES (?, 1, ?, ?, ?, 'submitted', 10, 20, 15)`, id, fmt.Sprintf("s%d", id), []byte{byte(id)}, id*7)
		mustExec(t, old, `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, 1, ?), (?, 2, 4)`, id, 1+id%2, id)
	}
	mustExec(t, old, `INSERT INTO attempt_event (id, attempt_id, kind, question, at) VALUES (1, 2, 'copy', 1, 12)`)
	columns := tableColumns(t, old)
	before := dumpRows(t, old, columns)
	old.Close()

	db := openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "7" {
		t.Fatalf("user_version = %s, want 7", got)
	}
	if after := dumpRows(t, db, columns); after != before {
		t.Errorf("rows changed by the migration:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	var revisions, withRevision int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM exam_revision), (SELECT COUNT(*) FROM attempt WHERE revision IS NOT NULL)`).
		Scan(&revisions, &withRevision); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 || withRevision != 0 {
		t.Errorf("after migration: %d revisions, %d attempts with a revision; want 0, 0", revisions, withRevision)
	}
	_, err := db.Exec(`INSERT INTO question_option (question_id, position, body_html, is_correct) VALUES (1,3,'o5',0)`)
	wantErr(t, "insert option on an exam with attempts", err, "exam is frozen")
	if id := insertAttempt(t, db, 1); attemptRevision(t, db, id) != 0 {
		t.Errorf("new attempt revision = %d, want NULL", attemptRevision(t, db, id))
	}
}

func TestSchema_RevisionAppendOnlyAndSequence(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	const outOfSequence = "revision number is out of sequence"

	if err := addRevision(db, 1, 1); err != nil {
		t.Fatalf("revision 1: %v", err)
	}
	wantErr(t, "revision 3 before 2", addRevision(db, 1, 3), outOfSequence)
	if err := addRevision(db, 1, 2); err != nil {
		t.Fatalf("revision 2: %v", err)
	}
	wantErr(t, "repeated revision 2", addRevision(db, 1, 2), outOfSequence)
	_, err := db.Exec(`INSERT OR REPLACE INTO exam_revision
		(exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		VALUES (1, 2, 0, 1, 'again', 'a', 'none', 'score', 1, 60, 'k1', '{}')`)
	wantErr(t, "INSERT OR REPLACE of revision 2", err, outOfSequence)

	const appendOnly = "revisions are append-only"
	_, err = db.Exec(`UPDATE exam_revision SET summary = 'changed' WHERE exam_id = 1 AND number = 1`)
	wantErr(t, "update revision", err, appendOnly)
	_, err = db.Exec(`DELETE FROM exam_revision WHERE exam_id = 1 AND number = 2`)
	wantErr(t, "delete revision", err, appendOnly)

	_, err = db.Exec(`INSERT INTO exam_revision
		(exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		VALUES (1, 3, 0, 1, 'bad', 'a', 'none', 'score', 1, 60, 'k1', '{')`)
	wantErr(t, "revision with invalid JSON content", err, "CHECK")

	_, err = db.Exec(`DELETE FROM exam WHERE id = 1`)
	wantErr(t, "delete exam with revisions", err, "FOREIGN KEY")

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM exam_revision WHERE exam_id = 1`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("revisions = %d, %v; want 2", n, err)
	}
}

func TestSchema_RevisionFreeze(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	attempt := insertAttempt(t, db, 1)

	// Revision 1 on an exam that already has attempts is the startup backfill.
	if err := addRevision(db, 1, 1); err != nil {
		t.Fatalf("revision 1 with an attempt: %v", err)
	}
	wantErr(t, "revision 2 with an attempt", addRevision(db, 1, 2), "exam is frozen")
	if err := addRevision(db, 1, 3); err == nil ||
		!strings.Contains(err.Error(), "exam is frozen") && !strings.Contains(err.Error(), "revision number is out of sequence") {
		t.Errorf("revision 3 out of sequence with an attempt: err = %v, want the frozen or out-of-sequence error", err)
	}

	mustExec(t, db, `DELETE FROM attempt WHERE id = ?`, attempt)
	if err := addRevision(db, 1, 2); err != nil {
		t.Fatalf("revision 2 after the attempt is gone: %v", err)
	}
}

func TestSchema_ExamContentMatchesRevision(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO teacher (id, email, created_at) VALUES (2, 'other@example.test', 0)`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	mustExec(t, db, `UPDATE exam SET title = 'b' WHERE id = 1`)

	if err := addRevision(db, 1, 1); err != nil {
		t.Fatal(err)
	}
	const mismatch = "exam content must match its latest revision"
	for _, set := range []string{
		`title = 'c'`,
		`shuffle = 'questions'`,
		`show_result = 'review'`,
		`monitor = 0`,
		`duration_minutes = 30`,
		`media_key = 'k2'`,
	} {
		_, err := db.Exec(`UPDATE exam SET ` + set + ` WHERE id = 1`)
		wantErr(t, "update "+set, err, mismatch)
	}
	mustExec(t, db, `UPDATE exam SET title = 'b', shuffle = 'none', show_result = 'score', monitor = 1, duration_minutes = 60, media_key = 'k1' WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET is_open = 1 WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET is_open = 0, opens_at = 100, closes_at = 200 WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET owner_id = 2 WHERE id = 1`)

	mustExec(t, db, `INSERT INTO exam_revision
		(exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		VALUES (1, 2, 0, 2, 'new settings', 'c', 'questions', 'review', 0, 30, 'k2', '{}')`)
	mustExec(t, db, `UPDATE exam SET title = 'c', shuffle = 'questions', show_result = 'review', monitor = 0, duration_minutes = 30, media_key = 'k2' WHERE id = 1`)
}

func TestSchema_AttemptRevision(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (2,'b','none','score','k2',0,1,'BBBBBB')`)

	unrevised := insertAttempt(t, db, 1)
	if got := attemptRevision(t, db, unrevised); got != 0 {
		t.Errorf("attempt on an exam without revision: revision = %d, want NULL", got)
	}

	for n := int64(1); n <= 2; n++ {
		if err := addRevision(db, 2, n); err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, started_at, deadline_at) VALUES (2, 's', x'AB', 0, 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	lastID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	var current int64
	if err := db.QueryRow(`SELECT id FROM attempt WHERE token_hash = x'AB'`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if lastID != current {
		t.Errorf("LastInsertId = %d, want the attempt id %d", lastID, current)
	}
	if got := attemptRevision(t, db, current); got != 2 {
		t.Errorf("attempt without revision on an exam at revision 2: revision = %d, want 2", got)
	}

	const explicit = `INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, started_at, deadline_at, revision) VALUES (2, 's', randomblob(16), 0, 0, 0, ?)`
	_, err = db.Exec(explicit, 1)
	wantErr(t, "attempt on an old revision", err, "attempt revision must be the exam's current revision")
	mustExec(t, db, explicit, 2)

	const fixed = "attempt revision is fixed"
	_, err = db.Exec(`UPDATE attempt SET revision = 1 WHERE id = ?`, current)
	wantErr(t, "change a set revision", err, fixed)
	_, err = db.Exec(`UPDATE attempt SET revision = NULL WHERE id = ?`, current)
	wantErr(t, "clear a set revision", err, fixed)
	mustExec(t, db, `UPDATE attempt SET student_name = 'renamed' WHERE id = ?`, current)
	mustExec(t, db, `UPDATE attempt SET status = 'submitted', finished_at = 1, resume_code = NULL WHERE id = ?`, current)

	if err := addRevision(db, 1, 1); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE attempt SET revision = 2 WHERE id = ?`, unrevised)
	wantErr(t, "fill a revision other than the current one", err, fixed)
	mustExec(t, db, `UPDATE attempt SET revision = 1 WHERE id = ?`, unrevised)
	if got := attemptRevision(t, db, unrevised); got != 1 {
		t.Errorf("filled revision = %d, want 1", got)
	}
}

// TestSchema_PreviousBinaryStatements runs the statements of the release
// before revisions (a2135f3) against the current schema: a rollback to that
// binary keeps working, except for replacing an exam that has a revision.
func TestSchema_PreviousBinaryStatements(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (1,'a','none','score','k1',0,1,'AAAAAA')`)
	mustExec(t, db, `INSERT INTO question (id, exam_id, position, body_html) VALUES (1,1,1,'q1')`)
	mustExec(t, db, `INSERT INTO question_option (id, question_id, position, body_html, is_correct) VALUES (1,1,1,'o1',1),(2,1,2,'o2',0)`)
	if err := addRevision(db, 1, 1); err != nil {
		t.Fatal(err)
	}
	const inScope = `(? OR exam.owner_id = ?)`

	mustExec(t, db, `UPDATE exam SET is_open = 1, opens_at = NULL, closes_at = NULL WHERE id = ? AND `+inScope, 1, false, 1)

	const start = `INSERT INTO attempt
		(exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	res, err := db.Exec(start, 1, "Ayu", []byte{1}, "ABCDEFGH", 7, 10, 20)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ayu, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if got := attemptRevision(t, db, ayu); got != 1 {
		t.Errorf("started attempt revision = %d, want 1", got)
	}
	const upsert = `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, ?, ?)
		ON CONFLICT (attempt_id, question_id) DO UPDATE SET option_id = excluded.option_id`
	mustExec(t, db, upsert, ayu, 1, 1)
	mustExec(t, db, upsert, ayu, 1, 2)
	mustExec(t, db, `UPDATE attempt SET token_hash = ?, resume_code = ? WHERE id = ?`, []byte{2}, "HGFEDCBA", ayu)
	mustExec(t, db, `UPDATE attempt SET status = ?, finished_at = ?, resume_code = NULL WHERE id = ?`, "submitted", 15, ayu)
	mustExec(t, db, start, 1, "Budi", []byte{3}, "JKMNPQRS", 8, 10, 20)
	res, err = db.Exec(`UPDATE attempt SET status = 'timed_out', finished_at = deadline_at, resume_code = NULL
	WHERE status = 'in_progress' AND deadline_at + ? < ?`, 5, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Errorf("sweep timed out %d attempts, want 1", n)
	}

	mustExec(t, db, `UPDATE exam SET is_open = 0, opens_at = NULL, closes_at = NULL WHERE id = ? AND `+inScope, 1, false, 1)
	mustExec(t, db, `UPDATE exam SET is_open = 0, opens_at = ?, closes_at = ? WHERE id = ? AND `+inScope, 100, 200, 1, false, 1)

	res, err = db.Exec(`INSERT INTO exam (title, shuffle, show_result, monitor, duration_minutes, is_open, media_key, created_at, owner_id, code) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		"Imported", "none", "score", true, 60, "k2", 0, 1, "BBBBBB")
	if err != nil {
		t.Fatalf("import exam: %v", err)
	}
	imported, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(`INSERT INTO question (exam_id, position, body_html) VALUES (?, ?, ?)`, imported, 1, "q")
	if err != nil {
		t.Fatalf("import question: %v", err)
	}
	qid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO question_option (question_id, position, body_html, is_correct) VALUES (?, ?, ?, ?)`, qid, 1, "o", true)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM exam_revision WHERE exam_id = ?`, imported).Scan(&n); err != nil || n != 0 {
		t.Errorf("revisions of the imported exam = %d, %v; want 0", n, err)
	}
	res, err = db.Exec(start, imported, "Citra", []byte{4}, "TVWXYZ23", 9, 10, 20)
	if err != nil {
		t.Fatalf("start on the imported exam: %v", err)
	}
	citra, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if got := attemptRevision(t, db, citra); got != 0 {
		t.Errorf("attempt on the imported exam: revision = %d, want NULL", got)
	}

	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES (3,'r','none','score','k3',0,1,'CCCCCC')`)
	mustExec(t, db, `INSERT INTO question (id, exam_id, position, body_html) VALUES (3,3,1,'q3')`)
	if err := addRevision(db, 3, 1); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, tx, `DELETE FROM question WHERE exam_id = ?`, 3)
	_, err = tx.Exec(`UPDATE exam SET title = ?, shuffle = ?, show_result = ?, monitor = ?, duration_minutes = ?, media_key = ? WHERE id = ?`,
		"r", "none", "score", true, 60, "k4", 3)
	wantErr(t, "replace an exam with a revision", err, "exam content must match its latest revision")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM question WHERE exam_id = 3`).Scan(&n); err != nil || n != 1 {
		t.Errorf("questions after the refused replace = %d, %v; want 1", n, err)
	}
}
