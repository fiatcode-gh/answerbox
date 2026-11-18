// Package exam owns the exam catalog: importing and replacing exam files,
// opening and closing exams, the image files stored for them and the
// numbered, append-only revisions that record every change of content.
package exam

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"io"
	"os"
	"sync"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

var (
	ErrNotFound   = errors.New("exam: not found")
	ErrFrozen     = errors.New("exam: content is frozen because attempts exist")
	ErrNoRevision = errors.New("exam: has no revision yet")
	ErrStale      = errors.New("exam: changed since the form was opened")
)

// Actor is the signed-in teacher a catalog call acts for. An admin may act
// on every exam; anyone else only on exams they own.
type Actor struct {
	TeacherID int64
	Admin     bool
}

// inScope is the one ownership condition: the exam row is visible to the
// actor bound by scopeArgs. An exam outside it is reported as ErrNotFound.
const inScope = `(? OR exam.owner_id = ?)`

func scopeArgs(actor Actor) []any { return []any{actor.Admin, actor.TeacherID} }

// scoped returns the arguments for `id = ? AND ` + inScope.
func scoped(examID int64, actor Actor) []any {
	return append([]any{examID}, scopeArgs(actor)...)
}

// Exam is a catalog entry with derived counts.
type Exam struct {
	ID         int64
	Title      string
	Shuffle    examfile.Shuffle
	ShowResult examfile.ShowResult
	Monitor    bool
	// Open reports whether students can start now: AvailabilityAt is OpenNow
	// at the moment the exam was read.
	Open bool
	// Schedule is the exam's open and close time; zero when it has none.
	Schedule Schedule
	// DurationMinutes is the exam length in whole minutes.
	DurationMinutes int
	// Code is what students type to enter the exam. It is empty only for an
	// exam from before migration 0005, until startup gives it one.
	Code          string
	CreatedAt     time.Time
	QuestionCount int
	AttemptCount  int
	OwnerID       int64
	OwnerEmail    string
	// Revision is the number of the current revision; 0 when the exam has
	// none yet.
	Revision int
}

// Revision is one stored revision of an exam's content. Questions is nil in
// the list Revisions returns; question and option IDs are always 0.
type Revision struct {
	Number          int
	CreatedAt       time.Time
	TeacherEmail    string
	Summary         string
	Title           string
	Shuffle         examfile.Shuffle
	ShowResult      examfile.ShowResult
	Monitor         bool
	DurationMinutes int
	Questions       []Question
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
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Service manages exams in the database and their images under media.
type Service struct {
	db     *sql.DB
	media  *os.Root
	now    func() time.Time
	random io.Reader

	// imageSaves serialises editor saves that carry an image, from the
	// media folder check to the end of cleanup, so a failed save never
	// removes a file another save's revision uses.
	imageSaves sync.Mutex
}

// NewService returns a Service. media is a root opened on <data>/media.
func NewService(db *sql.DB, media *os.Root, now func() time.Time, random io.Reader) *Service {
	return &Service{db: db, media: media, now: now, random: random}
}
