package exam

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"time"
)

// Open opens examID for actor now and removes its schedule. No other exam is
// touched; opening an open exam is a no-op. An archived exam is ErrArchived.
func (s *Service) Open(ctx context.Context, actor Actor, examID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireUnarchived(ctx, tx, actor, examID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE exam SET is_open = 1, opens_at = NULL, closes_at = NULL WHERE id = ? AND `+inScope, scoped(examID, actor)...)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes examID for actor and removes its schedule. Attempts are not
// touched.
func (s *Service) Close(ctx context.Context, actor Actor, examID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE exam SET is_open = 0, opens_at = NULL, closes_at = NULL WHERE id = ? AND `+inScope, scoped(examID, actor)...)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// AssignUnowned gives every exam without an owner to ownerID and returns how
// many it assigned. Exams that have an owner are never moved.
func (s *Service) AssignUnowned(ctx context.Context, ownerID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE exam SET owner_id = ? WHERE owner_id IS NULL`, ownerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// examColumns reads an exam row. The owner columns are COALESCEd because an
// exam from before migration 0004 has no owner until startup assigns one;
// the code likewise, for an exam from before migration 0005.
const examColumns = `SELECT id, title, shuffle, show_result, monitor, is_open, created_at,
	(SELECT COUNT(*) FROM question WHERE exam_id = exam.id),
	(SELECT COUNT(*) FROM attempt WHERE exam_id = exam.id),
	COALESCE(exam.owner_id, 0),
	COALESCE((SELECT email FROM teacher WHERE id = exam.owner_id), ''),
	COALESCE(exam.code, ''),
	duration_minutes, opens_at, closes_at,
	(SELECT COALESCE(MAX(number), 0) FROM exam_revision WHERE exam_id = exam.id),
	archived_at
	FROM exam`

// scanExam reads one examColumns row; Open is decided for the moment now.
func scanExam(row interface{ Scan(...any) error }, now time.Time) (Exam, error) {
	var e Exam
	var created int64
	var buttonOpen bool
	var opensAt, closesAt, archivedAt sql.NullInt64
	err := row.Scan(&e.ID, &e.Title, &e.Shuffle, &e.ShowResult, &e.Monitor, &buttonOpen, &created,
		&e.QuestionCount, &e.AttemptCount, &e.OwnerID, &e.OwnerEmail, &e.Code, &e.DurationMinutes, &opensAt, &closesAt, &e.Revision, &archivedAt)
	e.CreatedAt = time.UnixMilli(created).UTC()
	e.Schedule = Schedule{OpensAt: fromMillis(opensAt), ClosesAt: fromMillis(closesAt)}
	e.ArchivedAt = fromMillis(archivedAt)
	e.Open = AvailabilityAt(buttonOpen, e.Schedule, now) == OpenNow
	return e, err
}

// List returns the exams in actor's scope, newest first.
func (s *Service) List(ctx context.Context, actor Actor) ([]Exam, error) {
	rows, err := s.db.QueryContext(ctx, examColumns+` WHERE `+inScope+` ORDER BY created_at DESC, id DESC`, scopeArgs(actor)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	var out []Exam
	for rows.Next() {
		e, err := scanExam(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one exam in actor's scope.
func (s *Service) Get(ctx context.Context, actor Actor, examID int64) (Exam, error) {
	e, err := scanExam(s.db.QueryRowContext(ctx, examColumns+` WHERE id = ? AND `+inScope, scoped(examID, actor)...), s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return Exam{}, ErrNotFound
	}
	return e, err
}

// Questions returns the exam's questions in original order, with the key.
// Teacher-side callers resolve the exam through Get first, so the actor's
// scope has already been applied.
func (s *Service) Questions(ctx context.Context, examID int64) ([]Question, error) {
	return LoadQuestions(ctx, s.db, examID)
}

// questionColumns reads a question row with its section and accepted
// answers; the variant before sections reads a database from before
// migration 0009 (and so before fill-in) the same way, as a multiple-choice
// question without a section.
const (
	questionColumns               = `SELECT id, position, body_html, section_title, COALESCE(section_html, ''), accepted_answers FROM question`
	questionColumnsBeforeSections = `SELECT id, position, body_html, NULL, '', NULL FROM question`
)

// LoadQuestions reads an exam's questions and options in original order
// through q, which may be an open transaction.
func LoadQuestions(ctx context.Context, q Queryer, examID int64) ([]Question, error) {
	return loadQuestions(ctx, q, examID, questionColumns)
}

// loadQuestionsBeforeSections is LoadQuestions without the section and
// accepted-answer columns, for the rebuild of exams that may sit in a copy
// at schema 6, 7, 8 or 9.
func loadQuestionsBeforeSections(ctx context.Context, q Queryer, examID int64) ([]Question, error) {
	return loadQuestions(ctx, q, examID, questionColumnsBeforeSections)
}

func loadQuestions(ctx context.Context, q Queryer, examID int64, columns string) ([]Question, error) {
	rows, err := q.QueryContext(ctx, columns+` WHERE exam_id = ? ORDER BY position`, examID)
	if err != nil {
		return nil, err
	}
	var questions []Question
	index := map[int64]int{}
	for rows.Next() {
		var (
			qq           Question
			sectionTitle sql.NullString
			sectionHTML  string
			answers      sql.NullString
		)
		if err := rows.Scan(&qq.ID, &qq.Position, &qq.BodyHTML, &sectionTitle, &sectionHTML, &answers); err != nil {
			rows.Close()
			return nil, err
		}
		if sectionTitle.Valid {
			qq.Section = &Section{Title: sectionTitle.String, BodyHTML: template.HTML(sectionHTML)}
		}
		if answers.Valid {
			if err := json.Unmarshal([]byte(answers.String), &qq.Answers); err != nil {
				rows.Close()
				return nil, fmt.Errorf("exam: question %d: accepted answers: %w", qq.ID, err)
			}
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
