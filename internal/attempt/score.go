package attempt

import (
	"context"
	"database/sql"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// given is one stored answer: a picked option, or a typed text with the
// teacher's override of it.
type given struct {
	option   int64
	text     string
	override *Override
}

// answered reports whether g is a stored pick or typed answer.
func (g given) answered() bool { return g.option != 0 || g.text != "" }

// loadAnswers returns the stored answers of the attempts where selects
// ("a.id = ?" or "a.exam_id = ?"), by attempt id, then question id.
func loadAnswers(ctx context.Context, tx *sql.Tx, where string, arg int64) (map[int64]map[int64]given, error) {
	out := map[int64]map[int64]given{}
	put := func(attemptID, questionID int64, g given) {
		if out[attemptID] == nil {
			out[attemptID] = map[int64]given{}
		}
		out[attemptID][questionID] = g
	}

	rows, err := tx.QueryContext(ctx, `SELECT an.attempt_id, an.question_id, an.option_id
		FROM answer an JOIN attempt a ON a.id = an.attempt_id WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a, q, o int64
		if err := rows.Scan(&a, &q, &o); err != nil {
			rows.Close()
			return nil, err
		}
		put(a, q, given{option: o})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT ta.attempt_id, ta.question_id, ta.text, ta.override_correct, t.email, ta.override_at
		FROM typed_answer ta JOIN attempt a ON a.id = ta.attempt_id
		LEFT JOIN teacher t ON t.id = ta.override_by WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, q int64
		var text string
		var correct sql.NullBool
		var email sql.NullString
		var at sql.NullInt64
		if err := rows.Scan(&a, &q, &text, &correct, &email, &at); err != nil {
			return nil, err
		}
		g := given{text: text}
		if correct.Valid {
			g.override = &Override{Correct: correct.Bool, TeacherEmail: email.String, At: fromMillis(at.Int64)}
		}
		put(a, q, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, rows.Close()
}

// scorer applies the one score rule to the questions of one exam.
type scorer struct {
	questions []exam.Question
	accepted  map[int64][]answerKey // fill-in questions, by id
}

func newScorer(questions []exam.Question) scorer {
	s := scorer{questions: questions, accepted: map[int64][]answerKey{}}
	for _, q := range questions {
		for _, a := range q.Answers {
			s.accepted[q.ID] = append(s.accepted[q.ID], keyOf(a))
		}
	}
	return s
}

// auto reports whether text matches an accepted answer of q.
func (s scorer) auto(q exam.Question, text string) bool {
	k := keyOf(text)
	for _, a := range s.accepted[q.ID] {
		if k.matches(a) {
			return true
		}
	}
	return false
}

// right is the one place that decides whether an answer is right: a typed
// answer by the teacher's override when there is one, else by matching; a
// pick when it is the correct option.
func (s scorer) right(q exam.Question, g given) bool {
	if q.FillIn() {
		if g.text == "" {
			return false
		}
		if g.override != nil {
			return g.override.Correct
		}
		return s.auto(q, g.text)
	}
	for _, o := range q.Options {
		if o.Correct {
			return g.option == o.ID
		}
	}
	return false
}

// score counts the right answers in total and per section, in file order;
// bySection is nil when sections is 0. A right answer before the first
// section counts in the total only.
func (s scorer) score(answers map[int64]given, sections int) (total int, bySection []int) {
	if sections > 0 {
		bySection = make([]int, sections)
	}
	section := 0
	for _, q := range s.questions {
		if q.Section != nil {
			section++
		}
		if !s.right(q, answers[q.ID]) {
			continue
		}
		total++
		if section > 0 && section <= sections {
			bySection[section-1]++
		}
	}
	return total, bySection
}

// sectionsOf returns the sections of questions (in file order) with their
// question counts; nil without sections. A section runs from its first
// question to the next section's first question.
func sectionsOf(questions []exam.Question) []ResultSection {
	var out []ResultSection
	for _, q := range questions {
		if q.Section != nil {
			out = append(out, ResultSection{Title: q.Section.Title})
		}
		if len(out) > 0 {
			out[len(out)-1].Total++
		}
	}
	return out
}
