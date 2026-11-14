package attempt

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// summarySQL selects the columns scanSummary reads, with the derived score;
// callers append the WHERE clause.
const summarySQL = `SELECT a.id, a.student_name, a.status, a.started_at, a.deadline_at,
		a.finished_at, a.resume_code,
		(SELECT COUNT(*) FROM answer an JOIN question_option o ON o.id = an.option_id
			WHERE an.attempt_id = a.id AND o.is_correct = 1),
		(SELECT COUNT(*) FROM question WHERE exam_id = a.exam_id)
	FROM attempt a `

// scanSummary reads one summarySQL row; now (unix ms) gives the time left.
func scanSummary(row interface{ Scan(...any) error }, now int64) (Summary, error) {
	var sum Summary
	var started, deadline int64
	var finished sql.NullInt64
	var code sql.NullString
	err := row.Scan(&sum.ID, &sum.StudentName, &sum.Status, &started, &deadline,
		&finished, &code, &sum.Score, &sum.Total)
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

// List returns every attempt of examID in start order with its derived score.
// An unknown exam yields an empty slice.
func (s *Service) List(ctx context.Context, examID int64) ([]Summary, error) {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, summarySQL+`WHERE a.exam_id = ? ORDER BY a.started_at, a.id`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		sum, err := scanSummary(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Detail returns one attempt of examID with every question in original file
// order, the answer key and the student's picks. An attempt that does not
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
	picked, err := loadAnswers(ctx, tx, attemptID)
	if err != nil {
		return Detail{}, err
	}

	d := Detail{Summary: sum, Questions: make([]DetailQuestion, len(questions))}
	for i, q := range questions {
		pick, answered := picked[q.ID]
		dq := DetailQuestion{Position: q.Position, BodyHTML: q.BodyHTML, Answered: answered}
		for _, o := range q.Options {
			dq.Options = append(dq.Options, DetailOption{
				Position: o.Position, BodyHTML: o.BodyHTML, Picked: answered && pick == o.ID, Correct: o.Correct,
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
