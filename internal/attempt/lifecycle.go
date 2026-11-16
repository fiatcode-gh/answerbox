package attempt

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

const maxNameRunes = 100

// Start begins an attempt on the open exam with that code and returns its
// cookie token. The code is read as exam.NormalizeCode accepts it; a
// malformed, unknown or closed code is ErrNoOpenExam.
func (s *Service) Start(ctx context.Context, code, name string) (string, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return "", ErrInvalidName
	}
	examCode, ok := exam.NormalizeCode(code)
	if !ok {
		return "", ErrNoOpenExam
	}
	tx, now, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var examID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM exam WHERE code = ? AND is_open = 1`, examCode).Scan(&examID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoOpenExam
	}
	if err != nil {
		return "", err
	}

	var seed [8]byte
	if _, err := io.ReadFull(s.random, seed[:]); err != nil {
		return "", err
	}
	token, hash, err := s.newToken()
	if err != nil {
		return "", err
	}
	resumeCode, err := s.newCode(ctx, tx)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempt
		(exam_id, student_name, token_hash, resume_code, shuffle_seed, started_at, deadline_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		examID, name, hash, resumeCode, int64(binary.LittleEndian.Uint64(seed[:])), now, now+Duration.Milliseconds())
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func validName(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameRunes {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

// View returns the attempt a token resolves to: the questions in attempt
// order while in progress, the result the exam allows once finished.
func (s *Service) View(ctx context.Context, token string) (View, error) {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer tx.Rollback()

	var (
		v              View
		id, examID     int64
		seed, deadline int64
		shuffle        examfile.Shuffle
		show           examfile.ShowResult
	)
	err = tx.QueryRowContext(ctx, `SELECT a.id, a.exam_id, a.student_name, a.status, a.shuffle_seed, a.deadline_at,
			e.title, e.shuffle, e.show_result, e.monitor
		FROM attempt a JOIN exam e ON e.id = a.exam_id WHERE a.token_hash = ?`, hashToken(token)).
		Scan(&id, &examID, &v.StudentName, &v.Status, &seed, &deadline, &v.ExamTitle, &shuffle, &show, &v.Monitor)
	if errors.Is(err, sql.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, err
	}
	questions, err := exam.LoadQuestions(ctx, tx, examID)
	if err != nil {
		return View{}, err
	}
	picked, err := loadAnswers(ctx, tx, id)
	if err != nil {
		return View{}, err
	}
	questions = layout(questions, shuffle, seed)

	if v.Status == InProgress {
		v.Remaining = remaining(deadline, now)
		v.Questions = viewQuestions(questions, picked)
	} else {
		v.Result = result(questions, picked, show)
	}
	return v, tx.Commit()
}

// loadAnswers returns the attempt's picks as question ID → option ID.
func loadAnswers(ctx context.Context, tx *sql.Tx, attemptID int64) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT question_id, option_id FROM answer WHERE attempt_id = ?`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	picked := map[int64]int64{}
	for rows.Next() {
		var q, o int64
		if err := rows.Scan(&q, &o); err != nil {
			return nil, err
		}
		picked[q] = o
	}
	return picked, rows.Err()
}

func letter(i int) string { return string(rune('A' + i)) }

// viewQuestions renders laid-out questions for the student, without the key.
func viewQuestions(questions []exam.Question, picked map[int64]int64) []ViewQuestion {
	out := make([]ViewQuestion, len(questions))
	for i, q := range questions {
		vq := ViewQuestion{ID: q.ID, Number: i + 1, BodyHTML: q.BodyHTML}
		for j, o := range q.Options {
			vq.Options = append(vq.Options, ViewOption{
				ID: o.ID, Letter: letter(j), BodyHTML: o.BodyHTML, Picked: picked[q.ID] == o.ID,
			})
		}
		out[i] = vq
	}
	return out
}

// result is what a finished attempt may show under the exam's show_result.
func result(questions []exam.Question, picked map[int64]int64, show examfile.ShowResult) *Result {
	r := &Result{ShowResult: show}
	if show == examfile.ShowNone {
		return r
	}
	r.Total = len(questions)
	for _, q := range questions {
		for _, o := range q.Options {
			if o.Correct && picked[q.ID] == o.ID {
				r.Score++
			}
		}
	}
	if show != examfile.ShowReview {
		return r
	}
	r.Review = make([]ReviewQuestion, len(questions))
	for i, q := range questions {
		pick, answered := picked[q.ID]
		rq := ReviewQuestion{Number: i + 1, BodyHTML: q.BodyHTML, Answered: answered}
		for j, o := range q.Options {
			rq.Options = append(rq.Options, ReviewOption{
				Letter: letter(j), BodyHTML: o.BodyHTML, Picked: answered && pick == o.ID, Correct: o.Correct,
			})
		}
		r.Review[i] = rq
	}
	return r
}

// row is the stored state of the attempt a token resolves to.
type row struct {
	id, examID int64
	status     Status
	deadline   int64
}

// lookup resolves a token inside tx; an unknown token is ErrNotFound.
func lookup(ctx context.Context, tx *sql.Tx, token string) (row, error) {
	var r row
	err := tx.QueryRowContext(ctx,
		`SELECT id, exam_id, status, deadline_at FROM attempt WHERE token_hash = ?`, hashToken(token)).
		Scan(&r.id, &r.examID, &r.status, &r.deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, ErrNotFound
	}
	return r, err
}

// SaveAnswer stores the student's pick for one question and returns the time
// left. Past the deadline plus Grace the sweep has already finished the
// attempt, so nothing is stored.
func (s *Service) SaveAnswer(ctx context.Context, token string, questionID, optionID int64) (time.Duration, error) {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	a, err := lookup(ctx, tx, token)
	if err != nil {
		return 0, err
	}
	if a.status != InProgress {
		// Commit so a finalisation by this call's sweep is kept.
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, ErrFinished
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM question_option o
		JOIN question q ON q.id = o.question_id
		WHERE o.id = ? AND q.id = ? AND q.exam_id = ?)`, optionID, questionID, a.examID).Scan(&valid)
	if err != nil {
		return 0, err
	}
	if !valid {
		return 0, ErrInvalidChoice
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, ?, ?)
		ON CONFLICT (attempt_id, question_id) DO UPDATE SET option_id = excluded.option_id`,
		a.id, questionID, optionID)
	if err != nil {
		return 0, err
	}
	return remaining(a.deadline, now), tx.Commit()
}

// Submit finishes an in-progress attempt: submitted before the deadline,
// timed out (finished at the deadline) from it on. A finished attempt is left
// as it is.
func (s *Service) Submit(ctx context.Context, token string) error {
	tx, now, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := lookup(ctx, tx, token)
	if err != nil {
		return err
	}
	if a.status == InProgress {
		status, finished := Submitted, now
		if now >= a.deadline {
			status, finished = TimedOut, a.deadline
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE attempt SET status = ?, finished_at = ?, resume_code = NULL WHERE id = ?`,
			status, finished, a.id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Sweep times out every attempt past its deadline plus Grace.
func (s *Service) Sweep(ctx context.Context) error {
	tx, _, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return tx.Commit()
}
