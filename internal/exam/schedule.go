package exam

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrScheduleEmpty = errors.New("exam: schedule has neither an open nor a close time")
	ErrScheduleOrder = errors.New("exam: close time is not after the open time")
	ErrSchedulePast  = errors.New("exam: close time is not in the future")
)

// Schedule is when an exam opens and closes by itself. A zero time is not
// set. Students can start inside [OpensAt, ClosesAt).
type Schedule struct{ OpensAt, ClosesAt time.Time }

// Set reports whether at least one time is set.
func (s Schedule) Set() bool { return !s.OpensAt.IsZero() || !s.ClosesAt.IsZero() }

// Availability is whether students can start an exam at a moment.
type Availability int

const (
	Closed     Availability = iota // closed by the button, or the close time has passed
	NotYetOpen                     // scheduled, open time still ahead
	OpenNow
)

// AvailabilityAt is the only place that decides whether students can start.
// buttonOpen is the state the Open and Close buttons left; it counts only
// while the exam has no schedule.
func AvailabilityAt(buttonOpen bool, s Schedule, now time.Time) Availability {
	if !s.Set() {
		if buttonOpen {
			return OpenNow
		}
		return Closed
	}
	if !s.ClosesAt.IsZero() && !now.Before(s.ClosesAt) {
		return Closed
	}
	if !s.OpensAt.IsZero() && now.Before(s.OpensAt) {
		return NotYetOpen
	}
	return OpenNow
}

// SetSchedule gives examID in actor's scope a schedule. From then on the two
// times alone decide whether the exam is open; the button state is cleared.
func (s *Service) SetSchedule(ctx context.Context, actor Actor, examID int64, sched Schedule) error {
	switch {
	case !sched.Set():
		return ErrScheduleEmpty
	case !sched.OpensAt.IsZero() && !sched.ClosesAt.IsZero() && !sched.ClosesAt.After(sched.OpensAt):
		return ErrScheduleOrder
	case !sched.ClosesAt.IsZero() && !sched.ClosesAt.After(s.now()):
		return ErrSchedulePast
	}
	args := append([]any{toMillis(sched.OpensAt), toMillis(sched.ClosesAt)}, scoped(examID, actor)...)
	res, err := s.db.ExecContext(ctx, `UPDATE exam SET is_open = 0, opens_at = ?, closes_at = ? WHERE id = ? AND `+inScope, args...)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// toMillis stores t as unix ms; a zero time is NULL.
func toMillis(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// fromMillis reads a stored time; NULL is the zero time.
func fromMillis(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ms.Int64).UTC()
}
