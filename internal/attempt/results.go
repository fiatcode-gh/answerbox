package attempt

import (
	"context"
	"database/sql"
)

// List returns every attempt of examID in start order with its derived score.
// An unknown exam yields an empty slice.
func (s *Service) List(ctx context.Context, examID int64) ([]Summary, error) {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.student_name, a.status, a.started_at, a.deadline_at,
			a.finished_at, a.resume_code,
			(SELECT COUNT(*) FROM answer an JOIN question_option o ON o.id = an.option_id
				WHERE an.attempt_id = a.id AND o.is_correct = 1),
			(SELECT COUNT(*) FROM question WHERE exam_id = a.exam_id)
		FROM attempt a WHERE a.exam_id = ? ORDER BY a.started_at, a.id`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var sum Summary
		var started, deadline int64
		var finished sql.NullInt64
		var code sql.NullString
		err := rows.Scan(&sum.ID, &sum.StudentName, &sum.Status, &started, &deadline,
			&finished, &code, &sum.Score, &sum.Total)
		if err != nil {
			return nil, err
		}
		sum.StartedAt = fromMillis(started)
		if sum.Status == InProgress {
			sum.Remaining = remaining(deadline, now)
			sum.ResumeCode = formatCode(code.String)
		} else {
			sum.FinishedAt = fromMillis(finished.Int64)
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
