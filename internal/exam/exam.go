// Package exam owns the exam catalog: importing and replacing exam files,
// opening and closing exams, and the image files stored for them.
package exam

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"io"
	"os"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

var (
	ErrNotFound = errors.New("exam: not found")
	ErrFrozen   = errors.New("exam: content is frozen because attempts exist")
)

// Exam is a catalog entry with derived counts.
type Exam struct {
	ID            int64
	Title         string
	Shuffle       examfile.Shuffle
	ShowResult    examfile.ShowResult
	Open          bool
	CreatedAt     time.Time
	QuestionCount int
	AttemptCount  int
}

// Question is a stored question in original file order, with its answer key.
type Question struct {
	ID       int64
	Position int
	BodyHTML template.HTML
	Options  []Option
}

// Option is a stored answer option in original file order.
type Option struct {
	ID       int64
	Position int
	BodyHTML template.HTML
	Correct  bool
}

// Queryer is satisfied by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Service manages exams in the database and their images under media.
type Service struct {
	db     *sql.DB
	media  *os.Root
	now    func() time.Time
	random io.Reader
}

// NewService returns a Service. media is a root opened on <data>/media.
func NewService(db *sql.DB, media *os.Root, now func() time.Time, random io.Reader) *Service {
	return &Service{db: db, media: media, now: now, random: random}
}
