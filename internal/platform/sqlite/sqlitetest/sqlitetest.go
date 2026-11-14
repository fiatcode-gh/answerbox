// Package sqlitetest opens throwaway migrated databases for tests.
package sqlitetest

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite"
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
