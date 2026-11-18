package attempt

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// correctAnswersSQL is the one score rule: the answers that picked a
// correct option. The total score and the section scores both count it.
const correctAnswersSQL = "answer an JOIN question_option o ON o.id = an.option_id AND o.is_correct = 1"

// summarySQL selects the columns scanSummary reads, with the derived score;
// callers append the WHERE clause.
const summarySQL = `SELECT a.id, a.student_name, a.status, a.started_at, a.deadline_at,
		a.finished_at, a.resume_code,
		(SELECT COUNT(*) FROM ` + correctAnswersSQL + ` WHERE an.attempt_id = a.id),
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
		&finished, &code, &sum.Score, &sum.Total, &sum.Flags)
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
	if res.Sections, err = loadSections(ctx, tx, examID); err != nil {
		return Results{}, err
	}
	if res.Sections != nil && len(res.Attempts) > 0 {
		if err := fillSectionScores(ctx, tx, examID, res); err != nil {
			return Results{}, err
		}
	}
	return res, tx.Commit()
}

// loadSections returns the sections of examID in order with their question
// counts; nil without sections. A section runs from its first question to
// the next section's first question.
func loadSections(ctx context.Context, tx *sql.Tx, examID int64) ([]ResultSection, error) {
	rows, err := tx.QueryContext(ctx, `SELECT position, section_title,
			(SELECT COUNT(*) FROM question WHERE exam_id = ?)
		FROM question WHERE exam_id = ? AND section_title IS NOT NULL ORDER BY position`, examID, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sections []ResultSection
	var starts []int
	var questions int
	for rows.Next() {
		var sec ResultSection
		var start int
		if err := rows.Scan(&start, &sec.Title, &questions); err != nil {
			return nil, err
		}
		sections = append(sections, sec)
		starts = append(starts, start)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range sections {
		next := questions + 1
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		sections[i].Total = next - starts[i]
	}
	return sections, rows.Close()
}

// fillSectionScores gives every attempt in res its correct answers per
// section, counted by the same rule as the total score.
func fillSectionScores(ctx context.Context, tx *sql.Tx, examID int64, res Results) error {
	index := make(map[int64]int, len(res.Attempts))
	for i := range res.Attempts {
		res.Attempts[i].SectionScores = make([]int, len(res.Sections))
		index[res.Attempts[i].ID] = i
	}
	// The second column is the 1-based section number of the question: the
	// count of section starts at or before its position.
	rows, err := tx.QueryContext(ctx, `SELECT an.attempt_id,
			(SELECT COUNT(*) FROM question s WHERE s.exam_id = q.exam_id
				AND s.section_title IS NOT NULL AND s.position <= q.position),
			COUNT(*)
		FROM `+correctAnswersSQL+`
		JOIN question q ON q.id = o.question_id
		WHERE q.exam_id = ?
		GROUP BY 1, 2`, examID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var attemptID int64
		var section, correct int
		if err := rows.Scan(&attemptID, &section, &correct); err != nil {
			return err
		}
		i, ok := index[attemptID]
		if !ok || section < 1 || section > len(res.Sections) {
			continue // a question before the first section; not produced by the app
		}
		res.Attempts[i].SectionScores[section-1] = correct
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
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

	d := Detail{Summary: sum, Questions: make([]DetailQuestion, len(questions)), Events: events, Revision: revision}
	for i, q := range questions {
		pick, answered := picked[q.ID]
		dq := DetailQuestion{Position: q.Position, BodyHTML: q.BodyHTML, Answered: answered, Section: q.Section}
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
