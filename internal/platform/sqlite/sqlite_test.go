package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Errorf("user_version = %s, want 6", got)
	}
	mustExec(t, db, ownerTeacher)
	mustExec(t, db, `INSERT INTO exam (title, shuffle, show_result, media_key, created_at, owner_id, code) VALUES ('t','none','score','k1',0,1,'AAAAAA')`)
	db.Close()

	db = openFile(t, path)
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Errorf("user_version after reopen = %s, want 6", got)
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
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Fatalf("user_version = %s, want 6", got)
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
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Fatalf("user_version = %s, want 6", got)
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
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Fatalf("user_version = %s, want 6", got)
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
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Fatalf("user_version = %s, want 6", got)
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
	if got := pragma(t, db, "user_version"); got != "6" {
		t.Fatalf("user_version = %s, want 6", got)
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
