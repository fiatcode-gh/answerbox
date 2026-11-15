package exam

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"time"
)

// Open opens examID and closes any other open exam. Opening an open exam is
// a no-op.
func (s *Service) Open(ctx context.Context, examID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE exam SET is_open = 0 WHERE is_open = 1 AND id <> ?`, examID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE exam SET is_open = 1 WHERE id = ?`, examID)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes examID. Attempts are not touched.
func (s *Service) Close(ctx context.Context, examID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE exam SET is_open = 0 WHERE id = ?`, examID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const examColumns = `SELECT id, title, shuffle, show_result, monitor, is_open, created_at,
	(SELECT COUNT(*) FROM question WHERE exam_id = exam.id),
	(SELECT COUNT(*) FROM attempt WHERE exam_id = exam.id)
	FROM exam`

func scanExam(row interface{ Scan(...any) error }) (Exam, error) {
	var e Exam
	var created int64
	err := row.Scan(&e.ID, &e.Title, &e.Shuffle, &e.ShowResult, &e.Monitor, &e.Open, &created, &e.QuestionCount, &e.AttemptCount)
	e.CreatedAt = time.UnixMilli(created).UTC()
	return e, err
}

// List returns all exams, newest first.
func (s *Service) List(ctx context.Context) ([]Exam, error) {
	rows, err := s.db.QueryContext(ctx, examColumns+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exam
	for rows.Next() {
		e, err := scanExam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one exam.
func (s *Service) Get(ctx context.Context, examID int64) (Exam, error) {
	e, err := scanExam(s.db.QueryRowContext(ctx, examColumns+` WHERE id = ?`, examID))
	if errors.Is(err, sql.ErrNoRows) {
		return Exam{}, ErrNotFound
	}
	return e, err
}

// Current returns the open exam, if any.
func (s *Service) Current(ctx context.Context) (Exam, bool, error) {
	e, err := scanExam(s.db.QueryRowContext(ctx, examColumns+` WHERE is_open = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Exam{}, false, nil
	}
	if err != nil {
		return Exam{}, false, err
	}
	return e, true, nil
}

// Questions returns the exam's questions in original order, with the key.
func (s *Service) Questions(ctx context.Context, examID int64) ([]Question, error) {
	return LoadQuestions(ctx, s.db, examID)
}

// LoadQuestions reads an exam's questions and options in original order
// through q, which may be an open transaction.
func LoadQuestions(ctx context.Context, q Queryer, examID int64) ([]Question, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, position, body_html FROM question WHERE exam_id = ? ORDER BY position`, examID)
	if err != nil {
		return nil, err
	}
	var questions []Question
	index := map[int64]int{}
	for rows.Next() {
		var qq Question
		if err := rows.Scan(&qq.ID, &qq.Position, &qq.BodyHTML); err != nil {
			rows.Close()
			return nil, err
		}
		index[qq.ID] = len(questions)
		questions = append(questions, qq)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = q.QueryContext(ctx,
		`SELECT o.question_id, o.id, o.position, o.body_html, o.is_correct
		FROM question_option o JOIN question qq ON qq.id = o.question_id
		WHERE qq.exam_id = ? ORDER BY o.question_id, o.position`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var qid int64
		var o Option
		if err := rows.Scan(&qid, &o.ID, &o.Position, &o.BodyHTML, &o.Correct); err != nil {
			return nil, err
		}
		i := index[qid]
		questions[i].Options = append(questions[i].Options, o)
	}
	return questions, rows.Err()
}

// MediaFS serves the media root.
func (s *Service) MediaFS() fs.FS { return s.media.FS() }
