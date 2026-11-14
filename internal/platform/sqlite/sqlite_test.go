package sqlite_test

import (
	"context"
	"database/sql"
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

	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at) VALUES ('t','none','score','k1',0)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	mustExec(t, tx, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at) VALUES ('u','none','score','k2',0)`)

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
	if got := pragma(t, db, "user_version"); got != "1" {
		t.Errorf("user_version = %s, want 1", got)
	}
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at) VALUES ('t','none','score','k1',0)`)
	db.Close()

	db = openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "1" {
		t.Errorf("user_version after reopen = %s, want 1", got)
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM exam`).Scan(&title); err != nil || title != "t" {
		t.Fatalf("after reopen: title=%q err=%v", title, err)
	}
}

func TestSchema_OneOpenExam(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, is_open, media_key, created_at) VALUES (1,'a','none','score',1,'k1',0)`)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at) VALUES (2,'b','none','score','k2',0)`)
	_, err := db.Exec(`UPDATE exam SET is_open = 1 WHERE id = 2`)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("second open exam: err = %v, want UNIQUE constraint error", err)
	}
	mustExec(t, db, `UPDATE exam SET is_open = 0 WHERE id = 1`)
	mustExec(t, db, `UPDATE exam SET is_open = 1 WHERE id = 2`)
}

func TestSchema_FreezeTriggers(t *testing.T) {
	db := sqlitetest.Open(t)
	mustExec(t, db, `INSERT INTO exam (id, title, shuffle, show_result, media_key, created_at) VALUES (1,'a','none','score','k1',0)`)
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
