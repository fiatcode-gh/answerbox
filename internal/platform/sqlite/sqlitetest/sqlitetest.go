// Package sqlitetest opens throwaway migrated databases for tests.
package sqlitetest

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/platform/sqlite"
)

// Open returns a fresh migrated database in t.TempDir(), closed on cleanup.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlitetest: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Teacher puts email (already lower case) on the teacher list and returns
// its id.
func Teacher(t testing.TB, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO teacher (email, created_at) VALUES (?, 0)`, email)
	if err != nil {
		t.Fatalf("sqlitetest: add teacher: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("sqlitetest: add teacher: %v", err)
	}
	return id
}
