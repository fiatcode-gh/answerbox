// Command rebuildcheck reports whether the source text of every exam in a
// copy of the database can be rebuilt exactly from its stored HTML, as the
// startup step that gives existing exams their revision 1 does. It opens the
// copy read-only and immutable, never migrates it and writes nothing.
//
//	go run ./cmd/rebuildcheck -db /path/to/copy.db [-media /path/to/media]
//
// Exit status: 0 when every exam rebuilds exactly, 1 when any fails, 2 for
// usage or open errors.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// minVersion is the first schema version with every column the rebuild
// reads (duration_minutes comes with 0006).
const minVersion = 6

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rebuildcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database copy `FILE` (required)")
	mediaDir := flags.String("media", "", "image folder `DIR` (default: media beside the database file)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || flags.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: rebuildcheck -db FILE [-media DIR]")
		return 2
	}
	if *mediaDir == "" {
		*mediaDir = filepath.Join(filepath.Dir(*dbPath), "media")
	}
	results, err := check(*dbPath, *mediaDir)
	if err != nil {
		fmt.Fprintln(stderr, "rebuildcheck:", err)
		return 2
	}

	exact := 0
	for _, r := range results {
		if r.Err == nil {
			exact++
			fmt.Fprintf(stdout, "exam %d %q: %d questions, %d options: ok\n", r.ExamID, r.Title, r.Questions, r.Options)
			continue
		}
		fmt.Fprintf(stdout, "exam %d %q: FAILED %s\n", r.ExamID, r.Title, failure(r.Err))
	}
	fmt.Fprintf(stdout, "%d of %d exams rebuild exactly\n", exact, len(results))
	if exact != len(results) {
		return 1
	}
	return 0
}

// check opens the database copy at path read-only and immutable and
// rebuilds every exam in it with the images under mediaDir.
func check(path, mediaDir string) ([]exam.RebuildResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	// An immutable open ignores the write-ahead log, so a copy with one
	// would be read without its latest changes.
	if wal, err := os.Stat(abs + "-wal"); err == nil && wal.Size() > 0 {
		return nil, errors.New(`the copy has a write-ahead log; take it with sqlite3 ".backup"`)
	}
	// Never sqlite.Open: it migrates.
	db, err := sql.Open("sqlite", "file:"+abs+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx := context.Background()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if version < minVersion {
		return nil, fmt.Errorf("%s is at schema version %d; the check needs version %d or later", path, version, minVersion)
	}
	return exam.CheckRebuild(ctx, db, os.DirFS(mediaDir))
}

// failure names the block that could not be rebuilt and why.
func failure(err error) string {
	var re *examfile.RebuildError
	if !errors.As(err, &re) {
		return err.Error()
	}
	switch {
	case re.Question == 0:
		return "the exam: " + re.Reason
	case re.Option == 0:
		return fmt.Sprintf("question %d: %s", re.Question, re.Reason)
	default:
		return fmt.Sprintf("question %d, option %d: %s", re.Question, re.Option, re.Reason)
	}
}
