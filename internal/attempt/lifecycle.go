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

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

const maxNameRunes = 100

// Start begins an attempt on the exam with that code and returns its cookie
// token. The code is read as exam.NormalizeCode accepts it; a malformed,
// unknown or closed code is ErrNoOpenExam, and an exam whose open time is
// still ahead is a *NotYetOpenError. Whether the exam is open is decided by
// exam.LoadEntry at the start transaction's time.
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

	entry, found, err := exam.LoadEntry(ctx, tx, examCode, fromMillis(now))
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoOpenExam
	}
	if entry.Availability == exam.NotYetOpen {
		return "", &NotYetOpenError{Entry: entry}
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
		entry.ExamID, name, hash, resumeCode, int64(binary.LittleEndian.Uint64(seed[:])), now, now+int64(entry.DurationMinutes)*60000)
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
	answers, err := loadAnswers(ctx, tx, "a.id = ?", id)
	if err != nil {
		return View{}, err
	}
	sc := newScorer(questions)
	questions = layout(questions, shuffle, seed)

	if v.Status == InProgress {
		v.Remaining = remaining(deadline, now)
		v.Questions = viewQuestions(questions, answers[id])
	} else {
		v.Result = result(sc, questions, answers[id], show)
	}
	return v, tx.Commit()
}

func letter(i int) string { return string(rune('A' + i)) }

// viewQuestions renders laid-out questions for the student, without the key.
func viewQuestions(questions []exam.Question, answers map[int64]given) []ViewQuestion {
	out := make([]ViewQuestion, len(questions))
	for i, q := range questions {
		g := answers[q.ID]
		vq := ViewQuestion{ID: q.ID, Number: i + 1, BodyHTML: q.BodyHTML, Section: q.Section, FillIn: q.FillIn(), Typed: g.text}
		for j, o := range q.Options {
			vq.Options = append(vq.Options, ViewOption{
				ID: o.ID, Letter: letter(j), BodyHTML: o.BodyHTML, Picked: g.option == o.ID,
			})
		}
		out[i] = vq
	}
	return out
}

// result is what a finished attempt may show under the exam's show_result;
// questions are laid out in attempt order and sc holds them in file order.
func result(sc scorer, questions []exam.Question, answers map[int64]given, show examfile.ShowResult) *Result {
	r := &Result{ShowResult: show}
	if show == examfile.ShowNone {
		return r
	}
	r.Total = len(questions)
	r.Score, _ = sc.score(answers, 0)
	if show != examfile.ShowReview {
		return r
	}
	r.Review = make([]ReviewQuestion, len(questions))
	for i, q := range questions {
		g := answers[q.ID]
		answered := g.answered()
		rq := ReviewQuestion{
			Number: i + 1, BodyHTML: q.BodyHTML, Answered: answered, Section: q.Section,
			FillIn: q.FillIn(), Typed: g.text, Right: sc.right(q, g), Accepted: q.Answers,
		}
		for j, o := range q.Options {
			rq.Options = append(rq.Options, ReviewOption{
				Letter: letter(j), BodyHTML: o.BodyHTML, Picked: answered && g.option == o.ID, Correct: o.Correct,
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

const maxAnswerRunes = 200

// SaveText stores the student's typed answer to a fill-in question and
// returns the time left; an answer that is empty after trimming removes
// the stored one, so the question is unanswered again.
func (s *Service) SaveText(ctx context.Context, token string, questionID int64, text string) (time.Duration, error) {
	if !utf8.ValidString(text) || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return 0, ErrInvalidAnswer
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > maxAnswerRunes {
		return 0, ErrInvalidAnswer
	}
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
	var fillIn bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM question
		WHERE id = ? AND exam_id = ? AND accepted_answers IS NOT NULL)`, questionID, a.examID).Scan(&fillIn)
	if err != nil {
		return 0, err
	}
	if !fillIn {
		return 0, ErrInvalidAnswer
	}
	if text == "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM typed_answer WHERE attempt_id = ? AND question_id = ?`, a.id, questionID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO typed_answer (attempt_id, question_id, text) VALUES (?, ?, ?)
			ON CONFLICT (attempt_id, question_id) DO UPDATE SET text = excluded.text`, a.id, questionID, text)
	}
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
