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

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// Grace is how long after the deadline a save or submit still counts.
const Grace = 3 * time.Second

// Status is the lifecycle state of an attempt.
type Status string

const (
	InProgress Status = "in_progress"
	Submitted  Status = "submitted"
	TimedOut   Status = "timed_out"
)

// Mark is a teacher's decision on a typed answer.
type Mark string

const (
	MarkRight Mark = "right"
	MarkWrong Mark = "wrong"
	MarkNone  Mark = "none" // remove the override
)

var (
	ErrNoOpenExam     = errors.New("attempt: no open exam has that code")
	ErrInvalidName    = errors.New("attempt: name must be 1 to 100 characters")
	ErrNotFound       = errors.New("attempt: not found")
	ErrFinished       = errors.New("attempt: already finished")
	ErrInvalidChoice  = errors.New("attempt: question or option does not belong to this exam")
	ErrBadCode        = errors.New("attempt: resume code is not valid")
	ErrInvalidAnswer  = errors.New("attempt: typed answer is not valid for this question")
	ErrInProgress     = errors.New("attempt: still in progress")
	ErrNotOverridable = errors.New("attempt: not an answered fill-in question")
	ErrInvalidMark    = errors.New("attempt: mark must be right, wrong or none")
)

// NotYetOpenError refuses a start on an exam whose open time is still ahead.
// Entry is the exam the code names, with that open time.
type NotYetOpenError struct{ Entry exam.Entry }

func (e *NotYetOpenError) Error() string { return "attempt: exam is not open yet" }

// View is what the student sees. It carries no answer key while the attempt
// is in progress.
type View struct {
	ExamTitle, StudentName string
	Status                 Status
	Monitor                bool           // the exam's anti-cheat monitor is on
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
	Section  *exam.Section // the section that starts here; nil when none
	FillIn   bool          // a fill-in question: no options, a typed answer
	Typed    string        // the saved typed answer; "" for none
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

// ReviewQuestion is a finished question in attempt order. A fill-in question
// is answered when Typed is not empty.
type ReviewQuestion struct {
	Number   int
	BodyHTML template.HTML
	Answered bool
	Options  []ReviewOption
	Section  *exam.Section // the section that starts here; nil when none
	FillIn   bool
	Typed    string   // the typed answer; "" for none
	Right    bool     // as scored
	Accepted []string // the accepted answers of a fill-in question
}

// ReviewOption is a finished option in display order.
type ReviewOption struct {
	Letter          string
	BodyHTML        template.HTML
	Picked, Correct bool
}

// Results is the teacher's result list of one exam.
type Results struct {
	Sections []ResultSection // the exam's sections in order; nil without sections
	Attempts []Summary       // start order; empty, not nil, without attempts
}

// ResultSection is one section of an exam in the result list.
type ResultSection struct {
	Title string
	Total int // questions in the section
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
	Flags                 int           // recorded monitor events
	// SectionScores holds the correct answers per section, parallel to
	// Results.Sections; nil without sections. Only List fills it.
	SectionScores []int
}

// Detail is one attempt as the teacher sees it: the summary and every
// question in original file order with the key and the student's picks.
type Detail struct {
	Summary
	Questions []DetailQuestion
	Events    []Event // monitor events in time order
	Revision  int     // the exam revision the attempt was started on; 0 when the exam had none
}

// DetailQuestion is a question in original file order. A fill-in question is
// answered when Typed is not empty.
type DetailQuestion struct {
	Position int
	BodyHTML template.HTML
	Answered bool
	Options  []DetailOption
	Section  *exam.Section // the section that starts here; nil when none
	FillIn   bool
	Typed    string    // the typed answer; "" for none
	Accepted []string  // the accepted answers of a fill-in question
	Auto     bool      // the typed answer matches an accepted answer
	Right    bool      // as scored
	Override *Override // the teacher's mark on the typed answer; nil for none
}

// Override is a teacher's mark on a typed answer.
type Override struct {
	Correct      bool
	TeacherEmail string
	At           time.Time
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
// sweep and returns the time (unix ms) the whole call uses. The time is read
// once the transaction holds the only connection, so a call that waited for
// it is judged by when it runs, not by when it arrived.
func (s *Service) begin(ctx context.Context) (*sql.Tx, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	now := s.now().UnixMilli()
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
