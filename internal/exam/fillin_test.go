package exam_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fiatcode-gh/go-exam/internal/exam"
)

// mixedExam has a multiple-choice and a fill-in question.
const mixedExam = "# Mixed\nduration: 30\n\n1. Pick one\n- a *\n- b\n\n2. Ibu kota ....\n= Jakarta\n= DKI Jakarta\n"

// sectionedMixedExam has a multiple-choice and a fill-in question in two
// sections.
const sectionedMixedExam = "# Parts\nshuffle: none\n\n## Part A\nRead **carefully**.\n\n1. Pick\n- a *\n- b\n\n## Part B\n\n2. Hitung 2 + 3\n= 5\n= lima\n"

// choiceExam has two multiple-choice questions.
const choiceExam = "# Choice\n\n1. Pick one\n- a *\n- b\n\n2. Pick again\n- x\n- y *\n"

var jakarta = []string{"Jakarta", "DKI Jakarta"}

// answersOf returns the accepted answers of every question, in order.
func answersOf(qs []exam.Question) [][]string {
	out := make([][]string, len(qs))
	for i, q := range qs {
		out[i] = q.Answers
	}
	return out
}

// storedAnswers returns the accepted_answers column of every question of
// examID in order; "NULL" for NULL.
func (h *harness) storedAnswers(t *testing.T, examID int64) []string {
	t.Helper()
	rows, err := h.db.Query(`SELECT accepted_answers FROM question WHERE exam_id = ? ORDER BY position`, examID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if !s.Valid {
			s.String = "NULL"
		}
		out = append(out, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// revisionJSON returns the questions of revision number of examID as stored:
// each one's JSON keys and its decoded content.
func (h *harness) revisionJSON(t *testing.T, examID int64, number int) []map[string]json.RawMessage {
	t.Helper()
	var doc string
	if err := h.db.QueryRow(`SELECT content FROM exam_revision WHERE exam_id = ? AND number = ?`, examID, number).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	var content struct {
		Questions []map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(doc), &content); err != nil {
		t.Fatalf("revision %d of exam %d: %v", number, examID, err)
	}
	return content.Questions
}

// requireAnswersMatchRevision fails unless the live questions and the latest
// revision of examID have the same accepted answers.
func requireAnswersMatchRevision(t *testing.T, h *harness, examID int64) {
	t.Helper()
	requireLiveMatchesRevision(t, h, examID)
	rev := h.mustRevision(t, examID, h.mustGet(t, examID).Revision)
	if got, want := answersOf(rev.Questions), answersOf(h.mustQuestions(t, examID)); !reflect.DeepEqual(got, want) {
		t.Errorf("revision %d answers = %q, live answers = %q", rev.Number, got, want)
	}
}

func TestImport_StoresAcceptedAnswers(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(mixedExam))

	qs := h.mustQuestions(t, id)
	if len(qs) != 2 {
		t.Fatalf("questions = %+v, want 2", qs)
	}
	if qs[0].Answers != nil || qs[0].FillIn() || len(qs[0].Options) != 2 {
		t.Errorf("question 1 = %+v, want multiple choice with two options", qs[0])
	}
	if !reflect.DeepEqual(qs[1].Answers, jakarta) || !qs[1].FillIn() || qs[1].Options != nil {
		t.Errorf("question 2 = %+v, want fill-in with answers %q", qs[1], jakarta)
	}
	if got, want := h.storedAnswers(t, id), []string{"NULL", `["Jakarta","DKI Jakarta"]`}; !reflect.DeepEqual(got, want) {
		t.Errorf("accepted_answers = %q, want %q", got, want)
	}
	var options int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM question_option WHERE question_id = ?`, qs[1].ID).Scan(&options); err != nil || options != 0 {
		t.Errorf("options of the fill-in question = %d, %v; want 0", options, err)
	}
}

func TestRevision_FillInContent(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(mixedExam))

	qs := h.revisionJSON(t, id, 1)
	if len(qs) != 2 {
		t.Fatalf("revision questions = %d, want 2", len(qs))
	}
	if _, ok := qs[0]["answers"]; ok {
		t.Errorf("multiple-choice question has an answers key: %s", qs[0]["answers"])
	}
	if got, want := string(qs[1]["answers"]), `["Jakarta","DKI Jakarta"]`; got != want {
		t.Errorf("fill-in answers = %s, want %s", got, want)
	}
	if got := string(qs[1]["options"]); got != "[]" {
		t.Errorf("fill-in options = %s, want []", got)
	}
	rev := h.mustRevision(t, id, 1)
	if got, want := answersOf(rev.Questions), [][]string{nil, jakarta}; !reflect.DeepEqual(got, want) {
		t.Errorf("Revision answers = %q, want %q", got, want)
	}
	if !rev.Questions[1].FillIn() || rev.Questions[0].FillIn() {
		t.Errorf("Revision FillIn = %v, %v; want false, true", rev.Questions[0].FillIn(), rev.Questions[1].FillIn())
	}
	requireAnswersMatchRevision(t, h, id)

	choice := h.mustImport(t, []byte(choiceExam))
	for i, q := range h.revisionJSON(t, choice, 1) {
		if _, ok := q["answers"]; ok {
			t.Errorf("multiple-choice exam: question %d has an answers key", i+1)
		}
	}
	rev = h.mustRevision(t, choice, 1)
	if got := answersOf(rev.Questions); !reflect.DeepEqual(got, [][]string{nil, nil}) {
		t.Errorf("multiple-choice Revision answers = %q, want none", got)
	}
	requireAnswersMatchRevision(t, h, choice)
}

func TestDownload_FillInRoundTrip(t *testing.T) {
	for name, text := range map[string]string{"mixed": mixedExam, "sectioned": sectionedMixedExam} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			id := h.mustImport(t, []byte(text))
			f := h.mustDownload(t, h.owner, id)
			for _, a := range h.mustQuestions(t, id)[1].Answers {
				if !strings.Contains(string(f.Data), "\n= "+a+"\n") {
					t.Errorf("download lacks the line %q:\n%s", "= "+a, f.Data)
				}
			}
			again := h.mustImport(t, f.Data)
			requireEqualExams(t, h, id, again)
			if got, want := answersOf(h.mustQuestions(t, again)), answersOf(h.mustQuestions(t, id)); !reflect.DeepEqual(got, want) {
				t.Errorf("re-imported answers = %q, want %q", got, want)
			}
			if second := h.mustDownload(t, h.owner, again); string(second.Data) != string(f.Data) {
				t.Errorf("download of the re-import =\n%s\nwant\n%s", second.Data, f.Data)
			}
		})
	}
}

func TestCopy_KeepsAcceptedAnswers(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(sectionedMixedExam))
	copyID := h.mustCopy(t, h.owner, id)

	requireSameQuestions(t, h, id, copyID)
	if got, want := answersOf(h.mustQuestions(t, copyID)), answersOf(h.mustQuestions(t, id)); !reflect.DeepEqual(got, want) {
		t.Errorf("copy answers = %q, want %q", got, want)
	}
	if got, want := h.storedAnswers(t, copyID), h.storedAnswers(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("copy accepted_answers = %q, want %q", got, want)
	}
	requireAnswersMatchRevision(t, h, copyID)
}

func TestReplace_FillInFollowsTheFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(mixedExam))

	if err := h.svc.Replace(ctx, h.owner, id, []byte(choiceExam)); err != nil {
		t.Fatalf("Replace with multiple choice: %v", err)
	}
	if got := h.storedAnswers(t, id); !reflect.DeepEqual(got, []string{"NULL", "NULL"}) {
		t.Errorf("after multiple choice: accepted_answers = %q, want none", got)
	}
	if got := answersOf(h.mustQuestions(t, id)); !reflect.DeepEqual(got, [][]string{nil, nil}) {
		t.Errorf("after multiple choice: answers = %q, want none", got)
	}
	requireAnswersMatchRevision(t, h, id)

	if err := h.svc.Replace(ctx, h.owner, id, []byte(mixedExam)); err != nil {
		t.Fatalf("Replace with fill-in: %v", err)
	}
	if got, want := h.storedAnswers(t, id), []string{"NULL", `["Jakarta","DKI Jakarta"]`}; !reflect.DeepEqual(got, want) {
		t.Errorf("after fill-in: accepted_answers = %q, want %q", got, want)
	}
	if got, want := answersOf(h.mustQuestions(t, id)), [][]string{nil, jakarta}; !reflect.DeepEqual(got, want) {
		t.Errorf("after fill-in: answers = %q, want %q", got, want)
	}
	requireAnswersMatchRevision(t, h, id)

	for number, want := range map[int][][]string{1: {nil, jakarta}, 2: {nil, nil}, 3: {nil, jakarta}} {
		if got := answersOf(h.mustRevision(t, id, number).Questions); !reflect.DeepEqual(got, want) {
			t.Errorf("revision %d answers = %q, want %q", number, got, want)
		}
	}
}

func TestFillIn_FrozenOnceAttemptExists(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(mixedExam))
	h.insertAttempt(t, id, "Ayu")

	if err := h.svc.Replace(context.Background(), h.owner, id, []byte(choiceExam)); !errors.Is(err, exam.ErrFrozen) {
		t.Errorf("Replace = %v, want ErrFrozen", err)
	}
	_, err := h.db.Exec(`UPDATE question SET accepted_answers = '["x"]' WHERE exam_id = ? AND position = 2`, id)
	if err == nil || !strings.Contains(err.Error(), "exam is frozen") {
		t.Errorf("direct update = %v, want 'exam is frozen'", err)
	}
	if got, want := answersOf(h.mustQuestions(t, id)), [][]string{nil, jakarta}; !reflect.DeepEqual(got, want) {
		t.Errorf("answers = %q, want %q", got, want)
	}
}

// TestDownload_StoredAnswerLikeLine proves that a question text line that
// reads like an accepted answer stays text: the stored source is the one an
// older release stored for such a line at column 0, and Download, a
// re-import and Copy keep it a multiple-choice question.
func TestDownload_StoredAnswerLikeLine(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte("# T\n\n1. Hitung:\n        2 + 3\n        = 5\n- a *\n- b\n"))
	const source = "Hitung:\n2 + 3\n= 5"

	requireChoice := func(t *testing.T, what string, examID int64) {
		t.Helper()
		qs := h.mustQuestions(t, examID)
		if len(qs) != 1 || qs[0].FillIn() || len(qs[0].Options) != 2 {
			t.Errorf("%s: questions = %+v, want one multiple-choice question with two options", what, qs)
		}
		var got string
		if err := json.Unmarshal(h.revisionJSON(t, examID, h.mustGet(t, examID).Revision)[0]["source"], &got); err != nil || got != source {
			t.Errorf("%s: stored source = %q, %v; want %q", what, got, err, source)
		}
		if _, ok := h.revisionJSON(t, examID, h.mustGet(t, examID).Revision)[0]["answers"]; ok {
			t.Errorf("%s: revision has an answers key", what)
		}
	}
	requireChoice(t, "import", id)

	again := h.mustImport(t, h.mustDownload(t, h.owner, id).Data)
	requireChoice(t, "re-import", again)
	requireEqualExams(t, h, id, again)

	copyID := h.mustCopy(t, h.owner, id)
	requireChoice(t, "copy", copyID)
	requireSameQuestions(t, h, id, copyID)
}
