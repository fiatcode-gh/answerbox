package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
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
	old, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations/0001_init.sql", "migrations/0002_monitor.sql"} {
		schema, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, old, string(schema))
	}
	mustExec(t, old, `PRAGMA user_version = 2`)
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
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id) VALUES ('t','none','score','k1',0,1)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	mustExec(t, tx, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id) VALUES ('u','none','score','k2',0,1)`)

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
	if got := pragma(t, db, "user_version"); got != "4" {
		t.Errorf("user_version = %s, want 4", got)
	}
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id) VALUES ('t','none','score','k1',0,1)`)
	db.Close()

	db = openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "4" {
		t.Errorf("user_version after reopen = %s, want 4", got)
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
	if got := pragma(t, db, "user_version"); got != "4" {
		t.Fatalf("user_version = %s, want 4", got)
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
	if got := pragma(t, db, "user_version"); got != "4" {
		t.Fatalf("user_version = %s, want 4", got)
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
	if got := pragma(t, db, "user_version"); got != "4" {
		t.Fatalf("user_version = %s, want 4", got)
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
	_, err = db.Exec(`INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at) VALUES (2,'New','none','score','k2',0)`)
	if err == nil || !strings.Contains(err.Error(), needsOwner) {
		t.Fatalf("insert without owner: err = %v, want %q", err, needsOwner)
	}
	id := sqlitetest.Teacher(t, db, "ada@example.test")
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id) VALUES (2,'New','none','score','k2',0,?)`, id)
	_, err = db.Exec(`UPDATE exam SET owner_id = NULL WHERE id = 2`)
	if err == nil || !strings.Contains(err.Error(), needsOwner) {
		t.Fatalf("clearing the owner: err = %v, want %q", err, needsOwner)
	}
	mustExec(t, db, `UPDATE exam SET owner_id = ? WHERE id = 1`, id)
	if _, err := db.Exec(`UPDATE exam SET owner_id = 42 WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("unknown owner: err = %v, want a foreign key error", err)
	}
}

func TestSchema_OneOpenExam(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at, owner_id) VALUES (1,'a','none','score',1,'k1',0,1)`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id) VALUES (2,'b','none','score','k2',0,1)`)
	_, err := db.Exec(`UPDATE exam SET is_open = 1 WHERE id = 2`)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("second open exam: err = %v, want UNIQUE constraint error", err)
	}
	mustExec(t, db, `UPDATE exam SET is_open = 0 WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET is_open = 1 WHERE id = 2`)
}

func TestSchema_FreezeTriggers(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at, owner_id) VALUES (1,'a','none','score','k1',0,1)`)
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
