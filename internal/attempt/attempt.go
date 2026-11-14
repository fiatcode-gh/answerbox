// Package attempt owns the student attempt rules: starting, the derived
// question order, saving answers, the deadline, submitting, resuming with a
// code, and the teacher's result list.
package attempt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

const (
	Duration = 60 * time.Minute
	Grace    = 3 * time.Second
)

// Status is the lifecycle state of an attempt.
type Status string

const (
	InProgress Status = "in_progress"
	Submitted  Status = "submitted"
	TimedOut   Status = "timed_out"
)

var (
	ErrNoOpenExam    = errors.New("attempt: no exam is open")
	ErrInvalidName   = errors.New("attempt: name must be 1 to 100 characters")
	ErrNotFound      = errors.New("attempt: not found")
	ErrFinished      = errors.New("attempt: already finished")
	ErrInvalidChoice = errors.New("attempt: question or option does not belong to this exam")
	ErrBadCode       = errors.New("attempt: resume code is not valid")
)

// View is what the student sees. It carries no answer key while the attempt
// is in progress.
type View struct {
	ExamTitle, StudentName string
	Status                 Status
	Remaining              time.Duration  // 0 once finished
	Questions              []ViewQuestion // attempt order; nil once finished
	Result                 *Result        // nil while in progress
}

// ViewQuestion is a question in attempt order. Number is 1-based display order.
type ViewQuestion struct {
	ID       int64
	Number   int
	BodyHTML template.HTML
	Options  []ViewOption
}

// ViewOption is an option in display order. Letter is "A".. by display order.
type ViewOption struct {
	ID       int64
	Letter   string
	BodyHTML template.HTML
	Picked   bool
}

// Result is shown once the attempt is finished, as the exam's show_result
// allows.
type Result struct {
	ShowResult   examfile.ShowResult
	Score, Total int              // 0, 0 under ShowNone
	Review       []ReviewQuestion // attempt order; nil unless ShowReview
}

// ReviewQuestion is a finished question in attempt order.
type ReviewQuestion struct {
	Number   int
	BodyHTML template.HTML
	Answered bool
	Options  []ReviewOption
}

// ReviewOption is a finished option in display order.
type ReviewOption struct {
	Letter          string
	BodyHTML        template.HTML
	Picked, Correct bool
}

// Summary is one row of the teacher's result list.
type Summary struct {
	ID                    int64
	StudentName           string
	Status                Status
	StartedAt, FinishedAt time.Time // FinishedAt zero while in progress
	Score, Total          int
	Remaining             time.Duration // in progress only
	ResumeCode            string        // in progress only, "ABCD-EFGH"
}

// Detail is one attempt as the teacher sees it: the summary and every
// question in original file order with the key and the student's picks.
type Detail struct {
	Summary
	Questions []DetailQuestion
}

// DetailQuestion is a question in original file order.
type DetailQuestion struct {
	Position int
	BodyHTML template.HTML
	Answered bool
	Options  []DetailOption
}

// DetailOption is an option in original file order.
type DetailOption struct {
	Position        int
	BodyHTML        template.HTML
	Picked, Correct bool
}

// Service applies the attempt rules to the database.
type Service struct {
	db     *sql.DB
	now    func() time.Time
	random io.Reader
}

// NewService returns a Service. now and random are time.Now and
// crypto/rand.Reader in production.
func NewService(db *sql.DB, now func() time.Time, random io.Reader) *Service {
	return &Service{db: db, now: now, random: random}
}

// sweepSQL finalises every attempt whose grace period has passed. It is the
// only path that times out an abandoned attempt.
const sweepSQL = `UPDATE attempt SET status = 'timed_out', finished_at = deadline_at, resume_code = NULL
	WHERE status = 'in_progress' AND deadline_at + ? < ?`

// begin opens the transaction every public method runs in, applies the due
// sweep and returns the time (unix ms) the whole call uses.
func (s *Service) begin(ctx context.Context) (*sql.Tx, int64, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, sweepSQL, Grace.Milliseconds(), now); err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	return tx, now, nil
}

// newToken returns a fresh cookie token and the hash stored for it.
func (s *Service) newToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(s.random, b); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// remaining is max(0, deadline − now) for unix-ms times.
func remaining(deadline, now int64) time.Duration {
	return time.Duration(max(0, deadline-now)) * time.Millisecond
}

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
