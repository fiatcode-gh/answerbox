// Package sqlite opens the application database: one connection, WAL mode,
// embedded migrations.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

const dsnParams = "_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=temp_store(MEMORY)" +
	"&_txlock=immediate"

// Open opens (creating if needed) the database file at path with a single
// connection and applies all pending migrations.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?"+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: %s: %w", path, err)
	}
	return db, nil
}

// migrate applies every embedded NNNN_*.sql file whose number is above the
// database's user_version, each in its own transaction.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		num, _, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(num)
		if err != nil || len(num) != 4 {
			return fmt.Errorf("migration %s: name must start with NNNN_", base)
		}
		if version <= current {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := apply(ctx, db, version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
		current = version
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, version int, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}
