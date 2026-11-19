package exam

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/shortcode"
)

const (
	codeLen   = 6
	codeTries = 5
)

// newCode returns an exam code no exam holds.
func (s *Service) newCode(ctx context.Context, tx *sql.Tx) (string, error) {
	for range codeTries {
		code, err := shortcode.Random(s.random, codeLen)
		if err != nil {
			return "", err
		}
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM exam WHERE code = ?)`, code).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("exam: could not generate a unique exam code")
}

// NormalizeCode reads an exam code as a student types it: any case, with or
// without spaces or a dash. It reports whether the result is well formed.
func NormalizeCode(input string) (string, bool) {
	return shortcode.Normalize(input, codeLen)
}

// Entry is what a student's exam code leads to: an exam students can start
// now or one whose open time is still ahead.
type Entry struct {
	ExamID          int64
	Title, Code     string
	DurationMinutes int
	Availability    Availability // OpenNow or NotYetOpen, never Closed
	OpensAt         time.Time    // the open time still ahead; zero when OpenNow
}

// LoadEntry resolves a normalized code through q (which may be an open
// transaction) at the moment now. An unknown code and a Closed exam are the
// same: ok false and a zero Entry, so no caller can show a closed exam.
func LoadEntry(ctx context.Context, q Queryer, code string, now time.Time) (Entry, bool, error) {
	var e Entry
	var buttonOpen bool
	var opensAt, closesAt sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT id, title, code, duration_minutes, is_open, opens_at, closes_at FROM exam WHERE code = ?`, code).
		Scan(&e.ExamID, &e.Title, &e.Code, &e.DurationMinutes, &buttonOpen, &opensAt, &closesAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	sched := Schedule{OpensAt: fromMillis(opensAt), ClosesAt: fromMillis(closesAt)}
	e.Availability = AvailabilityAt(buttonOpen, sched, now)
	switch e.Availability {
	case Closed:
		return Entry{}, false, nil
	case NotYetOpen:
		e.OpensAt = sched.OpensAt
	}
	return e, true, nil
}

// EntryByCode resolves the code in input, typed as NormalizeCode accepts, at
// the service's current time. A malformed code is reported like an unknown
// one: ok is false.
func (s *Service) EntryByCode(ctx context.Context, input string) (Entry, bool, error) {
	code, ok := NormalizeCode(input)
	if !ok {
		return Entry{}, false, nil
	}
	return LoadEntry(ctx, s.db, code, s.now())
}

// AssignMissingCodes gives every exam without a code a new one and returns
// how many it assigned. Exams that have a code are never touched.
func (s *Service) AssignMissingCodes(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ids, err := missingCodeIDs(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		code, err := s.newCode(ctx, tx)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE exam SET code = ? WHERE id = ?`, code, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

// missingCodeIDs reads the ids of exams without a code. The rows are closed
// before it returns, so the single connection is free for the next statement.
func missingCodeIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM exam WHERE code IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
