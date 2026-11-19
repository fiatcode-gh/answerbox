package attempt

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// summarySQL selects the columns scanSummary reads; callers append the WHERE
// clause. The score is not among them: scorer derives it.
const summarySQL = `SELECT a.id, a.student_name, a.status, a.started_at, a.deadline_at,
		a.finished_at, a.resume_code,
		(SELECT COUNT(*) FROM question WHERE exam_id = a.exam_id),
		(SELECT COUNT(*) FROM attempt_event WHERE attempt_id = a.id)
	FROM attempt a `

// scanSummary reads one summarySQL row; now (unix ms) gives the time left.
func scanSummary(row interface{ Scan(...any) error }, now int64) (Summary, error) {
	var sum Summary
	var started, deadline int64
	var finished sql.NullInt64
	var code sql.NullString
	err := row.Scan(&sum.ID, &sum.StudentName, &sum.Status, &started, &deadline,
		&finished, &code, &sum.Total, &sum.Flags)
	if err != nil {
		return Summary{}, err
	}
	sum.StartedAt = fromMillis(started)
	if sum.Status == InProgress {
		sum.Remaining = remaining(deadline, now)
		sum.ResumeCode = formatCode(code.String)
	} else {
		sum.FinishedAt = fromMillis(finished.Int64)
	}
	return sum, nil
}

// List returns every attempt of examID in start order with its derived
// score and, for an exam with sections, its score per section. An unknown
// exam yields an empty Attempts slice.
func (s *Service) List(ctx context.Context, examID int64) (Results, error) {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return Results{}, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, summarySQL+`WHERE a.exam_id = ? ORDER BY a.started_at, a.id`, examID)
	if err != nil {
		return Results{}, err
	}
	defer rows.Close()
	res := Results{Attempts: []Summary{}}
	for rows.Next() {
		sum, err := scanSummary(rows, now)
		if err != nil {
			return Results{}, err
		}
		res.Attempts = append(res.Attempts, sum)
	}
	if err := rows.Err(); err != nil {
		return Results{}, err
	}
	if err := rows.Close(); err != nil {
		return Results{}, err
	}
	questions, err := exam.LoadQuestions(ctx, tx, examID)
	if err != nil {
		return Results{}, err
	}
	res.Sections = sectionsOf(questions)
	if len(res.Attempts) > 0 {
		answers, err := loadAnswers(ctx, tx, "a.exam_id = ?", examID)
		if err != nil {
			return Results{}, err
		}
		sc := newScorer(questions)
		for i := range res.Attempts {
			a := &res.Attempts[i]
			a.Score, a.SectionScores = sc.score(answers[a.ID], len(res.Sections))
		}
	}
	return res, tx.Commit()
}

// Counts is how many attempts of an exam are in each status.
type Counts struct{ InProgress, Submitted, TimedOut int }

// Total is every attempt of the exam.
func (c Counts) Total() int { return c.InProgress + c.Submitted + c.TimedOut }

// Counts counts the attempts of examID by status. Like List it runs after
// the due sweep, so an overdue attempt counts as timed out. An unknown exam
// has none.
func (s *Service) Counts(ctx context.Context, examID int64) (Counts, error) {
	tx, _, err := s.begin(ctx)
	if err != nil {
		return Counts{}, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT status, COUNT(*) FROM attempt WHERE exam_id = ? GROUP BY status`, examID)
	if err != nil {
		return Counts{}, err
	}
	defer rows.Close()
	var c Counts
	for rows.Next() {
		var status Status
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return Counts{}, err
		}
		switch status {
		case InProgress:
			c.InProgress = n
		case Submitted:
			c.Submitted = n
		case TimedOut:
			c.TimedOut = n
		}
	}
	if err := rows.Err(); err != nil {
		return Counts{}, err
	}
	if err := rows.Close(); err != nil {
		return Counts{}, err
	}
	return c, tx.Commit()
}

// Detail returns one attempt of examID with every question in original file
// order, the answer key and the student's answers. An attempt that does not
// exist or belongs to another exam is ErrNotFound.
func (s *Service) Detail(ctx context.Context, examID, attemptID int64) (Detail, error) {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()

	sum, err := scanSummary(tx.QueryRowContext(ctx, summarySQL+`WHERE a.id = ? AND a.exam_id = ?`, attemptID, examID), now)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	questions, err := exam.LoadQuestions(ctx, tx, examID)
	if err != nil {
		return Detail{}, err
	}
	all, err := loadAnswers(ctx, tx, "a.id = ?", attemptID)
	if err != nil {
		return Detail{}, err
	}
	answers := all[attemptID]
	events, err := loadEvents(ctx, tx, attemptID)
	if err != nil {
		return Detail{}, err
	}
	var seed int64
	var mode examfile.Shuffle
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT a.shuffle_seed, e.shuffle, COALESCE(a.revision, 0) FROM attempt a
		JOIN exam e ON e.id = a.exam_id WHERE a.id = ?`, attemptID).Scan(&seed, &mode, &revision)
	if err != nil {
		return Detail{}, err
	}
	if len(events) > 0 {
		screen := screenNumbers(questions, mode, seed)
		for i := range events {
			events[i].Screen = screen[events[i].Question]
		}
	}

	sc := newScorer(questions)
	sum.Score, _ = sc.score(answers, 0)
	d := Detail{Summary: sum, Questions: make([]DetailQuestion, len(questions)), Events: events, Revision: revision}
	for i, q := range questions {
		g := answers[q.ID]
		answered := g.answered()
		dq := DetailQuestion{
			Position: q.Position, BodyHTML: q.BodyHTML, Answered: answered, Section: q.Section,
			FillIn: q.FillIn(), Typed: g.text, Accepted: q.Answers,
			Auto: sc.auto(q, g.text), Right: sc.right(q, g), Override: g.override,
		}
		for _, o := range q.Options {
			dq.Options = append(dq.Options, DetailOption{
				Position: o.Position, BodyHTML: o.BodyHTML, Picked: answered && g.option == o.ID, Correct: o.Correct,
			})
		}
		d.Questions[i] = dq
	}
	return d, tx.Commit()
}

// Delete removes one attempt of examID together with its answers. An attempt
// that does not exist or belongs to another exam is ErrNotFound.
func (s *Service) Delete(ctx context.Context, examID, attemptID int64) error {
	tx, _, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM attempt WHERE id = ? AND exam_id = ?`, attemptID, examID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// Rename overwrites the student name of one attempt of examID, under the
// rules of Start: the name is trimmed and must be 1 to 100 characters, else
// ErrInvalidName. Nothing else about the attempt changes and no history is
// kept. An attempt that does not exist or belongs to another exam is
// ErrNotFound.
func (s *Service) Rename(ctx context.Context, examID, attemptID int64, name string) error {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return ErrInvalidName
	}
	tx, _, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE attempt SET student_name = ? WHERE id = ? AND exam_id = ?`, name, attemptID, examID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// Override sets, replaces or removes the teacher's mark on the typed
// answer of one attempt of examID to the fill-in question at file position
// position. Only the latest mark is kept, with who and when.
//
// An unknown attempt or position is ErrNotFound, an attempt in progress
// ErrInProgress, a multiple-choice or unanswered question ErrNotOverridable
// and a mark other than MarkRight, MarkWrong or MarkNone ErrInvalidMark.
// Removing a mark that is not there succeeds and changes nothing.
func (s *Service) Override(ctx context.Context, examID, attemptID int64, position int, teacherID int64, mark Mark) error {
	var correct, by, at any // NULL removes the mark
	switch mark {
	case MarkRight, MarkWrong:
		correct, by = mark == MarkRight, teacherID
	case MarkNone:
	default:
		return ErrInvalidMark
	}
	tx, now, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if correct != nil {
		at = now
	}

	var status Status
	err = tx.QueryRowContext(ctx, `SELECT status FROM attempt WHERE id = ? AND exam_id = ?`, attemptID, examID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	refuse := func(refusal error) error {
		// Commit so a finalisation by this call's sweep is kept.
		if err := tx.Commit(); err != nil {
			return err
		}
		return refusal
	}
	var questionID int64
	var fillIn bool
	err = tx.QueryRowContext(ctx, `SELECT id, accepted_answers IS NOT NULL FROM question WHERE exam_id = ? AND position = ?`,
		examID, position).Scan(&questionID, &fillIn)
	if errors.Is(err, sql.ErrNoRows) {
		return refuse(ErrNotFound)
	}
	if err != nil {
		return err
	}
	if status == InProgress {
		return refuse(ErrInProgress)
	}
	if !fillIn {
		return refuse(ErrNotOverridable)
	}
	res, err := tx.ExecContext(ctx, `UPDATE typed_answer SET override_correct = ?, override_by = ?, override_at = ?
		WHERE attempt_id = ? AND question_id = ?`, correct, by, at, attemptID, questionID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return refuse(ErrNotOverridable)
	}
	return tx.Commit()
}
