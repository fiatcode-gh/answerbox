package attempt

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MaxEvents is the most monitor events stored per attempt; later ones are
// dropped.
const MaxEvents = 500

// EventKind is a suspicious action the exam page reports.
type EventKind string

const (
	LeftPage   EventKind = "left_page" // hidden or unfocused for more than 3 s
	Copy       EventKind = "copy"      // copy or cut, blocked
	Paste      EventKind = "paste"     // blocked
	RightClick EventKind = "right_click"
	Resize     EventKind = "resize"
)

// ErrInvalidEvent is an event kind the monitor does not know.
var ErrInvalidEvent = errors.New("attempt: unknown event kind")

// Event is one recorded monitor event. Question is the 1-based file position
// of the question the event names, 0 when unknown. Screen is the number
// that question had on the student's screen, 0 when unknown; only Detail
// fills it.
type Event struct {
	At       time.Time
	Kind     EventKind
	Question int
	Screen   int
}

func (k EventKind) valid() bool {
	switch k {
	case LeftPage, Copy, Paste, RightClick, Resize:
		return true
	}
	return false
}

// RecordEvent stores a monitor event for the attempt the token resolves to,
// at the server's time. questionID is the question the event names; 0,
// or a question outside the exam, is stored as unknown. It reports false
// without error when the exam's monitor is off or the attempt already has
// MaxEvents events. Answers, timing and score are never touched.
func (s *Service) RecordEvent(ctx context.Context, token string, kind EventKind, questionID int64) (bool, error) {
	if !kind.valid() {
		return false, ErrInvalidEvent
	}
	tx, now, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	a, err := lookup(ctx, tx, token)
	if err != nil {
		return false, err
	}
	if a.status != InProgress {
		// Commit so a finalisation by this call's sweep is kept.
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, ErrFinished
	}
	var monitor bool
	var count int
	err = tx.QueryRowContext(ctx, `SELECT e.monitor, (SELECT COUNT(*) FROM attempt_event WHERE attempt_id = ?)
		FROM exam e WHERE e.id = ?`, a.id, a.examID).Scan(&monitor, &count)
	if err != nil {
		return false, err
	}
	if !monitor || count >= MaxEvents {
		return false, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempt_event (attempt_id, kind, question, at)
		VALUES (?, ?, (SELECT position FROM question WHERE id = ? AND exam_id = ?), ?)`,
		a.id, string(kind), questionID, a.examID, now)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// loadEvents returns an attempt's events in time order.
func loadEvents(ctx context.Context, tx *sql.Tx, attemptID int64) ([]Event, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT at, kind, question FROM attempt_event WHERE attempt_id = ? ORDER BY at, id`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var at int64
		var e Event
		var q sql.NullInt64
		if err := rows.Scan(&at, &e.Kind, &q); err != nil {
			return nil, err
		}
		e.At, e.Question = fromMillis(at), int(q.Int64)
		out = append(out, e)
	}
	return out, rows.Err()
}
