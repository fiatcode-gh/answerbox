package attempt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/attempt"
	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

var t0 = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

// defaultLength is the length of an exam file without a duration header.
const defaultLength = 60 * time.Minute

var deadline0 = t0.Add(defaultLength)

// withDuration adds a duration header to an exam text from examText.
func withDuration(text string, minutes int) string {
	return strings.Replace(text, "\n", fmt.Sprintf("\nduration: %d\n", minutes), 1)
}

type harness struct {
	db    *sql.DB
	exams *exam.Service
	svc   *attempt.Service
	clock time.Time
	owner exam.Actor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := sqlitetest.Open(t)
	media, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })
	h := &harness{db: db, clock: t0}
	now := func() time.Time { return h.clock }
	h.exams = exam.NewService(db, media, now, rand.NewChaCha8([32]byte{9}))
	h.owner = exam.Actor{TeacherID: sqlitetest.Teacher(t, db, "owner@example.test")}
	h.svc = attempt.NewService(db, now, rand.NewChaCha8([32]byte{4, 2}))
	return h
}

// examText builds an exam of n questions with m options each; option 1 of
// every question is the correct one.
func examText(shuffle string, show examfile.ShowResult, n, m int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Exam %s %s\nshuffle: %s\nshow_result: %s\n", shuffle, show, shuffle, show)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "\n%d. Question %d\n", i, i)
		for j := 1; j <= m; j++ {
			mark := ""
			if j == 1 {
				mark = " *"
			}
			fmt.Fprintf(&b, "- Option %d.%d%s\n", i, j, mark)
		}
	}
	return b.String()
}

func (h *harness) importExam(t *testing.T, text string) int64 {
	t.Helper()
	id, err := h.exams.Import(context.Background(), h.owner, []byte(text))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return id
}

func (h *harness) openExam(t *testing.T, text string) int64 {
	t.Helper()
	id := h.importExam(t, text)
	if err := h.exams.Open(context.Background(), h.owner, id); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return id
}

func (h *harness) questions(t *testing.T, examID int64) []exam.Question {
	t.Helper()
	qs, err := h.exams.Questions(context.Background(), examID)
	if err != nil {
		t.Fatalf("Questions: %v", err)
	}
	return qs
}

// code returns the exam code of examID.
func (h *harness) code(t *testing.T, examID int64) string {
	t.Helper()
	var code string
	if err := h.db.QueryRow(`SELECT code FROM exam WHERE id = ?`, examID).Scan(&code); err != nil {
		t.Fatalf("code of exam %d: %v", examID, err)
	}
	return code
}

// openCode returns the code of the single open exam; the test fails when
// the number of open exams is not 1.
func (h *harness) openCode(t *testing.T) string {
	t.Helper()
	var ids []int64
	rows, err := h.db.Query(`SELECT id FROM exam WHERE is_open = 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("open exams = %v, want exactly one", ids)
	}
	return h.code(t, ids[0])
}

// start begins an attempt on the single open exam.
func (h *harness) start(t *testing.T, name string) string {
	t.Helper()
	return h.startWith(t, h.openCode(t), name)
}

// startIn begins an attempt on examID with that exam's code.
func (h *harness) startIn(t *testing.T, examID int64, name string) string {
	t.Helper()
	return h.startWith(t, h.code(t, examID), name)
}

func (h *harness) startWith(t *testing.T, code, name string) string {
	t.Helper()
	token, err := h.svc.Start(context.Background(), code, name)
	if err != nil {
		t.Fatalf("Start(%q, %q): %v", code, name, err)
	}
	return token
}

// attemptRows counts every attempt row, whatever its exam.
func (h *harness) attemptRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attempt`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// otherCode is a well-formed exam code that differs from code.
func otherCode(code string) string {
	if code == "ABCDEF" {
		return "GHJKMN"
	}
	return "ABCDEF"
}

func (h *harness) save(t *testing.T, token string, questionID, optionID int64) time.Duration {
	t.Helper()
	rem, err := h.svc.SaveAnswer(context.Background(), token, questionID, optionID)
	if err != nil {
		t.Fatalf("SaveAnswer(%d, %d): %v", questionID, optionID, err)
	}
	return rem
}

func (h *harness) submit(t *testing.T, token string) {
	t.Helper()
	if err := h.svc.Submit(context.Background(), token); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

func (h *harness) view(t *testing.T, token string) attempt.View {
	t.Helper()
	v, err := h.svc.View(context.Background(), token)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	return v
}

func (h *harness) list(t *testing.T, examID int64) []attempt.Summary {
	t.Helper()
	rows, err := h.svc.List(context.Background(), examID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return rows
}

// only returns the single attempt of examID.
func (h *harness) only(t *testing.T, examID int64) attempt.Summary {
	t.Helper()
	rows := h.list(t, examID)
	if len(rows) != 1 {
		t.Fatalf("List: %d rows, want 1", len(rows))
	}
	return rows[0]
}

// storedAnswers reads an attempt's answers straight from the database.
func (h *harness) storedAnswers(t *testing.T, attemptID int64) map[int64]int64 {
	t.Helper()
	rows, err := h.db.Query(`SELECT question_id, option_id FROM answer WHERE attempt_id = ?`, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var q, o int64
		if err := rows.Scan(&q, &o); err != nil {
			t.Fatal(err)
		}
		out[q] = o
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func correctOption(q exam.Question) int64 {
	for _, o := range q.Options {
		if o.Correct {
			return o.ID
		}
	}
	panic("no correct option")
}

func wrongOption(q exam.Question) int64 {
	for _, o := range q.Options {
		if !o.Correct {
			return o.ID
		}
	}
	panic("no wrong option")
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

// order is the displayed question IDs, each followed by its option IDs.
func order(v attempt.View) [][]int64 {
	var out [][]int64
	for _, q := range v.Questions {
		row := []int64{q.ID}
		for _, o := range q.Options {
			row = append(row, o.ID)
		}
		out = append(out, row)
	}
	return out
}

func TestStart_RefusesCodeWithoutOpenExam(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	refused := func(label, code string) {
		t.Helper()
		before := h.attemptRows(t)
		_, err := h.svc.Start(ctx, code, "Ayu")
		wantErr(t, label, err, attempt.ErrNoOpenExam)
		if got := h.attemptRows(t); got != before {
			t.Fatalf("%s: attempt rows %d → %d", label, before, got)
		}
	}
	refused("no exams", "ABCDEF")

	id := h.importExam(t, examText("none", examfile.ShowScore, 2, 2))
	code := h.code(t, id)
	refused("closed exam's code", code)

	if err := h.exams.Open(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	refused("another well-formed code", otherCode(code))
	refused("empty code", "")
	refused("short code", "ABC")
	refused("code plus one symbol", code+"A")
	token := h.startIn(t, id, "Ayu")

	if err := h.exams.Close(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	refused("after Close", code)

	q := h.questions(t, id)[0]
	h.save(t, token, q.ID, correctOption(q))
	if got := h.only(t, id).Score; got != 1 {
		t.Fatalf("score after save on closed exam = %d, want 1", got)
	}
}

func TestStart_EntersTheExamNamedByTheCode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := exam.Actor{TeacherID: sqlitetest.Teacher(t, h.db, "other@example.test")}
	first := h.openExam(t, "# First exam\n"+strings.SplitN(examText("none", examfile.ShowScore, 2, 2), "\n", 2)[1])
	second, err := h.exams.Import(ctx, other, []byte("# Second exam\n"+strings.SplitN(examText("none", examfile.ShowScore, 3, 2), "\n", 2)[1]))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.exams.Open(ctx, other, second); err != nil {
		t.Fatal(err)
	}

	ayu := h.startIn(t, first, "Ayu")
	if v := h.view(t, ayu); v.ExamTitle != "First exam" || len(v.Questions) != 2 {
		t.Errorf("first exam's code: title %q, %d questions; want \"First exam\", 2", v.ExamTitle, len(v.Questions))
	}
	budi := h.startIn(t, second, "Budi")
	if v := h.view(t, budi); v.ExamTitle != "Second exam" || len(v.Questions) != 3 {
		t.Errorf("second exam's code: title %q, %d questions; want \"Second exam\", 3", v.ExamTitle, len(v.Questions))
	}
	if s := h.only(t, first); s.StudentName != "Ayu" {
		t.Errorf("first exam's attempt is %q, want Ayu", s.StudentName)
	}
	if s := h.only(t, second); s.StudentName != "Budi" {
		t.Errorf("second exam's attempt is %q, want Budi", s.StudentName)
	}

	if err := h.exams.Close(ctx, h.owner, first); err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Start(ctx, h.code(t, first), "Citra")
	wantErr(t, "closed exam's code", err, attempt.ErrNoOpenExam)
	h.startIn(t, second, "Dewi")
	q := h.questions(t, first)[0]
	h.save(t, ayu, q.ID, correctOption(q))
	if got := h.only(t, first).Score; got != 1 {
		t.Errorf("score of the first exam's attempt after Close = %d, want 1", got)
	}
}

func TestStart_NameValidation(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))

	h.start(t, "  Ayu  ")
	if got := h.only(t, id).StudentName; got != "Ayu" {
		t.Fatalf("stored name = %q, want %q", got, "Ayu")
	}

	invalid := map[string]string{
		"empty":           "",
		"whitespace only": " \t\n ",
		"101 runes":       strings.Repeat("a", 101),
		"control char":    "Ay\x07u",
		"invalid UTF-8":   "Ay\xffu",
	}
	for label, name := range invalid {
		_, err := h.svc.Start(context.Background(), h.code(t, id), name)
		wantErr(t, label, err, attempt.ErrInvalidName)
	}
	if got := len(h.list(t, id)); got != 1 {
		t.Fatalf("attempts after invalid names = %d, want 1", got)
	}

	long := strings.Repeat("日", 100)
	h.start(t, long)
	h.start(t, "Ayu")
	rows := h.list(t, id)
	if len(rows) != 3 {
		t.Fatalf("attempts = %d, want 3", len(rows))
	}
	if rows[1].StudentName != long {
		t.Fatalf("100-rune name stored as %q", rows[1].StudentName)
	}
	if rows[2].StudentName != "Ayu" || rows[2].ID == rows[0].ID {
		t.Fatalf("second Ayu = %+v, want a separate attempt from %+v", rows[2], rows[0])
	}
}

func TestStart_CodeAsTyped(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
	code := h.code(t, id)
	lower := strings.ToLower(code)
	for i, typed := range []string{lower, lower[:3] + "-" + lower[3:], "  " + code[:2] + " " + code[2:4] + " " + code[4:] + "  "} {
		h.startWith(t, typed, "Ayu")
		if got := len(h.list(t, id)); got != i+1 {
			t.Fatalf("after Start(%q): %d attempts, want %d", typed, got, i+1)
		}
	}
}

func TestStart_InvalidNameBeforeCode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	closed := h.importExam(t, examText("none", examfile.ShowScore, 1, 2))
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
	for label, code := range map[string]string{
		"closed exam's code": h.code(t, closed),
		"empty code":         "",
		"malformed code":     "ABC",
		"unknown code":       otherCode(h.code(t, id)),
		"right code":         h.code(t, id),
	} {
		_, err := h.svc.Start(ctx, code, "")
		wantErr(t, label, err, attempt.ErrInvalidName)
	}
	if got := h.attemptRows(t); got != 0 {
		t.Fatalf("invalid names created %d attempts", got)
	}
}

func TestScore_UnansweredCountsWrong(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("questions, options", examfile.ShowScore, 4, 3))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")

	h.save(t, token, qs[0].ID, correctOption(qs[0]))
	h.save(t, token, qs[1].ID, correctOption(qs[1]))
	h.save(t, token, qs[2].ID, wrongOption(qs[2]))
	if s := h.only(t, id); s.Score != 2 || s.Total != 4 {
		t.Fatalf("List score = %d/%d, want 2/4", s.Score, s.Total)
	}

	h.save(t, token, qs[2].ID, correctOption(qs[2]))
	if s := h.only(t, id); s.Score != 3 {
		t.Fatalf("List score after fixing an answer = %d, want 3", s.Score)
	}
	h.save(t, token, qs[0].ID, wrongOption(qs[0]))
	if s := h.only(t, id); s.Score != 2 {
		t.Fatalf("List score after breaking an answer = %d, want 2", s.Score)
	}

	h.submit(t, token)
	v := h.view(t, token)
	if v.Result == nil || v.Result.Score != 2 || v.Result.Total != 4 {
		t.Fatalf("View result = %+v, want 2/4", v.Result)
	}
	if s := h.only(t, id); s.Score != 2 || s.Total != 4 {
		t.Fatalf("List score after submit = %d/%d, want 2/4", s.Score, s.Total)
	}
}

func TestSaveAnswer_Deadline(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 4, 2))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	attemptID := h.only(t, id).ID

	h.clock = deadline0.Add(-10 * time.Second)
	if rem := h.save(t, token, qs[0].ID, correctOption(qs[0])); rem != 10*time.Second {
		t.Fatalf("remaining 10 s before deadline = %v", rem)
	}
	h.clock = deadline0
	if rem := h.save(t, token, qs[1].ID, correctOption(qs[1])); rem != 0 {
		t.Fatalf("remaining at deadline = %v, want 0", rem)
	}
	h.clock = deadline0.Add(attempt.Grace)
	if rem := h.save(t, token, qs[2].ID, correctOption(qs[2])); rem != 0 {
		t.Fatalf("remaining at deadline + grace = %v, want 0", rem)
	}

	h.clock = deadline0.Add(attempt.Grace + time.Millisecond)
	_, err := h.svc.SaveAnswer(context.Background(), token, qs[3].ID, correctOption(qs[3]))
	wantErr(t, "SaveAnswer after grace", err, attempt.ErrFinished)

	stored := h.storedAnswers(t, attemptID)
	if len(stored) != 3 {
		t.Fatalf("stored answers = %v, want questions 1-3", stored)
	}
	if _, ok := stored[qs[3].ID]; ok {
		t.Fatal("late answer was stored")
	}
	s := h.only(t, id)
	if s.Status != attempt.TimedOut || !s.FinishedAt.Equal(deadline0) {
		t.Fatalf("status %s finished %v, want timed_out at %v", s.Status, s.FinishedAt, deadline0)
	}
}

func TestStart_DeadlineUsesExamDuration(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, withDuration(examText("none", examfile.ShowScore, 2, 2), 5))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	if rem := h.view(t, token).Remaining; rem != 5*time.Minute {
		t.Fatalf("Remaining at start = %v, want 5m", rem)
	}

	h.clock = t0.Add(5*time.Minute + 2*time.Second)
	if rem := h.save(t, token, qs[0].ID, correctOption(qs[0])); rem != 0 {
		t.Fatalf("remaining 2 s after the 5-minute deadline = %v, want 0", rem)
	}
	h.clock = t0.Add(5*time.Minute + 3*time.Second + time.Millisecond)
	_, err := h.svc.SaveAnswer(context.Background(), token, qs[1].ID, correctOption(qs[1]))
	wantErr(t, "SaveAnswer after the 5-minute deadline and grace", err, attempt.ErrFinished)
	s := h.only(t, id)
	if want := t0.Add(5 * time.Minute); s.Status != attempt.TimedOut || !s.FinishedAt.Equal(want) {
		t.Fatalf("status %s finished %v, want timed_out at %v", s.Status, s.FinishedAt, want)
	}
}

func TestStart_TwoExamsEachUseTheirOwnLength(t *testing.T) {
	h := newHarness(t)
	short := h.openExam(t, withDuration(examText("none", examfile.ShowScore, 1, 2), 5))
	long := h.openExam(t, withDuration(examText("none", examfile.ShowScore, 1, 2), 240))
	shortToken := h.startIn(t, short, "Ayu")
	longToken := h.startIn(t, long, "Budi")
	if rem := h.view(t, shortToken).Remaining; rem != 5*time.Minute {
		t.Errorf("short exam Remaining = %v, want 5m", rem)
	}
	if rem := h.view(t, longToken).Remaining; rem != 240*time.Minute {
		t.Errorf("long exam Remaining = %v, want 240m", rem)
	}

	h.clock = t0.Add(6 * time.Minute)
	if v := h.view(t, shortToken); v.Status != attempt.TimedOut {
		t.Errorf("short exam status after 6 minutes = %s, want timed_out", v.Status)
	}
	if v := h.view(t, longToken); v.Status != attempt.InProgress || v.Remaining != 234*time.Minute {
		t.Errorf("long exam after 6 minutes: %s, %v; want in_progress, 234m", v.Status, v.Remaining)
	}
}

func TestSubmit_StatusByDeadline(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	q := h.questions(t, id)[0]
	early := h.start(t, "Early")
	onTime := h.start(t, "AtDeadline")

	h.clock = deadline0.Add(-time.Millisecond)
	h.submit(t, early)
	s := h.list(t, id)[0]
	if s.Status != attempt.Submitted || !s.FinishedAt.Equal(h.clock) {
		t.Fatalf("early submit: %s at %v, want submitted at %v", s.Status, s.FinishedAt, h.clock)
	}

	h.clock = deadline0
	h.submit(t, onTime)
	s = h.list(t, id)[1]
	if s.Status != attempt.TimedOut || !s.FinishedAt.Equal(deadline0) {
		t.Fatalf("submit at deadline: %s at %v, want timed_out at %v", s.Status, s.FinishedAt, deadline0)
	}

	h.clock = deadline0.Add(time.Second)
	h.submit(t, early)
	s = h.list(t, id)[0]
	if s.Status != attempt.Submitted || !s.FinishedAt.Equal(deadline0.Add(-time.Millisecond)) {
		t.Fatalf("second submit changed the attempt: %s at %v", s.Status, s.FinishedAt)
	}

	_, err := h.svc.SaveAnswer(context.Background(), early, q.ID, correctOption(q))
	wantErr(t, "SaveAnswer after submit", err, attempt.ErrFinished)
	if got := h.storedAnswers(t, s.ID); len(got) != 0 {
		t.Fatalf("answers after submit = %v, want none", got)
	}
}

func TestSweep_AbandonedTimesOut(t *testing.T) {
	check := func(t *testing.T, h *harness, id int64) {
		t.Helper()
		s := h.only(t, id)
		if s.Status != attempt.TimedOut || !s.FinishedAt.Equal(deadline0) || s.ResumeCode != "" {
			t.Fatalf("after sweep: %+v, want timed_out at %v with no code", s, deadline0)
		}
	}

	t.Run("explicit Sweep", func(t *testing.T) {
		h := newHarness(t)
		id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
		h.start(t, "Ayu")

		h.clock = deadline0.Add(attempt.Grace)
		if err := h.svc.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := h.db.QueryRow(`SELECT status FROM attempt`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != string(attempt.InProgress) {
			t.Fatalf("status at deadline + grace = %s, want in_progress", status)
		}

		h.clock = deadline0.Add(attempt.Grace + time.Millisecond)
		if err := h.svc.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := h.db.QueryRow(`SELECT status FROM attempt`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != string(attempt.TimedOut) {
			t.Fatalf("status after Sweep = %s, want timed_out", status)
		}
		check(t, h, id)
	})

	t.Run("List alone", func(t *testing.T) {
		h := newHarness(t)
		id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
		h.start(t, "Ayu")
		h.clock = deadline0.Add(attempt.Grace + time.Millisecond)
		check(t, h, id)
	})
}

func TestView_OrderStableAcrossReloadAndResume(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("questions, options", examfile.ShowReview, 6, 4))
	token := h.start(t, "Ayu")

	first := order(h.view(t, token))
	if len(first) != 6 {
		t.Fatalf("view has %d questions, want 6", len(first))
	}
	if again := order(h.view(t, token)); !reflect.DeepEqual(again, first) {
		t.Fatalf("reload order %v, want %v", again, first)
	}

	h.save(t, token, first[2][0], first[2][3])
	v := h.view(t, token)
	if got := order(v); !reflect.DeepEqual(got, first) {
		t.Fatalf("order after save %v, want %v", got, first)
	}
	if !v.Questions[2].Options[2].Picked {
		t.Fatal("saved option not picked")
	}

	newToken, err := h.svc.Redeem(context.Background(), h.only(t, id).ResumeCode)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if got := order(h.view(t, newToken)); !reflect.DeepEqual(got, first) {
		t.Fatalf("order after resume %v, want %v", got, first)
	}

	other := h.start(t, "Budi")
	if got := order(h.view(t, other)); reflect.DeepEqual(got, first) {
		t.Fatal("two attempts got the same order")
	}
}

func TestView_NumbersAndLetters(t *testing.T) {
	h := newHarness(t)
	h.openExam(t, examText("questions, options", examfile.ShowScore, 3, 3))
	v := h.view(t, h.start(t, "Ayu"))
	if v.Status != attempt.InProgress || v.Remaining != defaultLength || v.Result != nil {
		t.Fatalf("in-progress view = %+v", v)
	}
	if v.StudentName != "Ayu" || !strings.HasPrefix(v.ExamTitle, "Exam ") {
		t.Fatalf("view header = %q / %q", v.StudentName, v.ExamTitle)
	}
	for i, q := range v.Questions {
		if q.Number != i+1 {
			t.Fatalf("question %d has Number %d", i, q.Number)
		}
		for j, o := range q.Options {
			if want := string(rune('A' + j)); o.Letter != want {
				t.Fatalf("option %d of question %d has Letter %q, want %q", j, i, o.Letter, want)
			}
		}
	}
}

func TestRedeem_MovesAttemptAndRevokesOldToken(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	q := h.questions(t, id)[1]
	old := h.start(t, "Ayu")
	h.save(t, old, q.ID, q.Options[1].ID)

	h.clock = t0.Add(10 * time.Minute)
	token, err := h.svc.Redeem(context.Background(), h.only(t, id).ResumeCode)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if token == old {
		t.Fatal("Redeem returned the old token")
	}
	v := h.view(t, token)
	if v.Remaining != 50*time.Minute {
		t.Fatalf("remaining after resume = %v, want 50m", v.Remaining)
	}
	if !v.Questions[1].Options[1].Picked {
		t.Fatal("new token does not see the saved answer")
	}

	_, err = h.svc.View(context.Background(), old)
	wantErr(t, "View with old token", err, attempt.ErrNotFound)
	_, err = h.svc.SaveAnswer(context.Background(), old, q.ID, q.Options[0].ID)
	wantErr(t, "SaveAnswer with old token", err, attempt.ErrNotFound)
	err = h.svc.Submit(context.Background(), old)
	wantErr(t, "Submit with old token", err, attempt.ErrNotFound)
	if s := h.only(t, id); s.Status != attempt.InProgress {
		t.Fatalf("old token changed the attempt: %s", s.Status)
	}
}

func TestRedeem_SingleUse(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
	h.start(t, "Ayu")

	code := h.only(t, id).ResumeCode
	if _, err := h.svc.Redeem(context.Background(), code); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	_, err := h.svc.Redeem(context.Background(), code)
	wantErr(t, "Redeem with a used code", err, attempt.ErrBadCode)

	next := h.only(t, id).ResumeCode
	if next == code {
		t.Fatal("code was not replaced")
	}
	messy := " " + strings.ToLower(next[:4]) + " - " + strings.ToLower(next[5:]) + " "
	if _, err := h.svc.Redeem(context.Background(), messy); err != nil {
		t.Fatalf("Redeem(%q): %v", messy, err)
	}
}

func TestRedeem_RejectsFinishedAndUnknown(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 1, 2))
	submitted := h.start(t, "Submitted")
	h.start(t, "Abandoned")
	rows := h.list(t, id)
	submittedCode, abandonedCode := rows[0].ResumeCode, rows[1].ResumeCode

	h.submit(t, submitted)
	_, err := h.svc.Redeem(context.Background(), submittedCode)
	wantErr(t, "Redeem a submitted attempt", err, attempt.ErrBadCode)

	h.clock = deadline0.Add(attempt.Grace + time.Millisecond)
	_, err = h.svc.Redeem(context.Background(), abandonedCode)
	wantErr(t, "Redeem a timed-out attempt", err, attempt.ErrBadCode)

	for _, code := range []string{"2222-2222", "ABCD", "ABCD-EFGH-J", "IIII-OOOO", ""} {
		_, err = h.svc.Redeem(context.Background(), code)
		wantErr(t, fmt.Sprintf("Redeem(%q)", code), err, attempt.ErrBadCode)
	}
}

func TestSaveAnswer_RejectsForeignChoice(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 2, 2))
	otherID := h.importExam(t, examText("none", examfile.ShowScore, 1, 2))
	qs, other := h.questions(t, id), h.questions(t, otherID)[0]
	a := h.start(t, "A")
	b := h.start(t, "B")
	rows := h.list(t, id)

	foreign := map[string][2]int64{
		"option of another question": {qs[0].ID, qs[1].Options[0].ID},
		"question of another exam":   {other.ID, other.Options[0].ID},
		"unknown option":             {qs[0].ID, 999999},
	}
	for label, c := range foreign {
		_, err := h.svc.SaveAnswer(context.Background(), a, c[0], c[1])
		wantErr(t, label, err, attempt.ErrInvalidChoice)
	}
	if got := h.storedAnswers(t, rows[0].ID); len(got) != 0 {
		t.Fatalf("answers after rejected saves = %v, want none", got)
	}

	h.save(t, b, qs[0].ID, qs[0].Options[0].ID)
	h.save(t, a, qs[0].ID, qs[0].Options[1].ID)
	if got := h.storedAnswers(t, rows[1].ID); got[qs[0].ID] != qs[0].Options[0].ID || len(got) != 1 {
		t.Fatalf("B's answers = %v, want only its own", got)
	}
}

func TestView_ResultByShowResult(t *testing.T) {
	for _, show := range []examfile.ShowResult{examfile.ShowNone, examfile.ShowScore, examfile.ShowReview} {
		t.Run(string(show), func(t *testing.T) {
			h := newHarness(t)
			id := h.openExam(t, examText("questions, options", show, 3, 3))
			correct := map[int64]bool{}
			for _, q := range h.questions(t, id) {
				correct[correctOption(q)] = true
			}
			token := h.start(t, "Ayu")

			live := h.view(t, token)
			if live.Result != nil {
				t.Fatalf("in-progress Result = %+v, want nil", live.Result)
			}
			// Displayed question 1: wrong; question 2: correct; question 3:
			// unanswered.
			picks := map[int64]int64{}
			for i, q := range live.Questions[:2] {
				for _, o := range q.Options {
					if correct[o.ID] == (i == 1) {
						picks[q.ID] = o.ID
						h.save(t, token, q.ID, o.ID)
						break
					}
				}
			}
			h.submit(t, token)

			v := h.view(t, token)
			if v.Status != attempt.Submitted || v.Questions != nil || v.Remaining != 0 || v.Result == nil {
				t.Fatalf("finished view = %+v", v)
			}
			r := v.Result
			if r.ShowResult != show {
				t.Fatalf("ShowResult = %s, want %s", r.ShowResult, show)
			}
			switch show {
			case examfile.ShowNone:
				if r.Score != 0 || r.Total != 0 || r.Review != nil {
					t.Fatalf("none result = %+v", r)
				}
				return
			case examfile.ShowScore:
				if r.Score != 1 || r.Total != 3 || r.Review != nil {
					t.Fatalf("score result = %+v", r)
				}
				return
			}
			if r.Score != 1 || r.Total != 3 || len(r.Review) != 3 {
				t.Fatalf("review result = %+v", r)
			}
			for i, rq := range r.Review {
				lq := live.Questions[i]
				if rq.Number != i+1 || rq.BodyHTML != lq.BodyHTML || len(rq.Options) != len(lq.Options) {
					t.Fatalf("review question %d = %+v, want display order of %+v", i, rq, lq)
				}
				picked, hasPick := picks[lq.ID]
				if rq.Answered != hasPick {
					t.Fatalf("review question %d Answered = %v, want %v", i, rq.Answered, hasPick)
				}
				correct := 0
				for j, ro := range rq.Options {
					lo := lq.Options[j]
					if ro.Letter != lo.Letter || ro.BodyHTML != lo.BodyHTML {
						t.Fatalf("review option %d.%d = %+v, want %+v", i, j, ro, lo)
					}
					if ro.Picked != (hasPick && picked == lo.ID) {
						t.Fatalf("review option %d.%d Picked = %v", i, j, ro.Picked)
					}
					if ro.Correct {
						correct++
					}
				}
				if correct != 1 {
					t.Fatalf("review question %d has %d correct options, want 1", i, correct)
				}
			}
		})
	}
}

func TestList_Fields(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, examText("none", examfile.ShowScore, 3, 2))
	h.start(t, "First")
	h.clock = t0.Add(time.Minute)
	second := h.start(t, "Second")
	h.clock = t0.Add(2 * time.Minute)
	h.start(t, "Third")
	h.clock = t0.Add(3 * time.Minute)
	h.submit(t, second)
	h.clock = t0.Add(5 * time.Minute)

	rows := h.list(t, id)
	var names []string
	for _, s := range rows {
		names = append(names, s.StudentName)
	}
	if want := []string{"First", "Second", "Third"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}

	codeFormat := regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`)
	first, sub, third := rows[0], rows[1], rows[2]
	if first.Status != attempt.InProgress || !first.StartedAt.Equal(t0) || !first.FinishedAt.IsZero() ||
		first.Remaining != 55*time.Minute || !codeFormat.MatchString(first.ResumeCode) || first.Total != 3 {
		t.Fatalf("in-progress row = %+v", first)
	}
	if third.Remaining != 57*time.Minute || !codeFormat.MatchString(third.ResumeCode) || third.ResumeCode == first.ResumeCode {
		t.Fatalf("second in-progress row = %+v", third)
	}
	if sub.Status != attempt.Submitted || !sub.StartedAt.Equal(t0.Add(time.Minute)) ||
		!sub.FinishedAt.Equal(t0.Add(3*time.Minute)) || sub.Remaining != 0 || sub.ResumeCode != "" || sub.Total != 3 {
		t.Fatalf("submitted row = %+v", sub)
	}

	if got := h.list(t, 9999); got == nil || len(got) != 0 {
		t.Fatalf("List(unknown) = %#v, want empty slice", got)
	}
}

// scheduledExam imports a closed exam and gives it sched.
func (h *harness) scheduledExam(t *testing.T, text string, sched exam.Schedule) int64 {
	t.Helper()
	id := h.importExam(t, text)
	if err := h.exams.SetSchedule(context.Background(), h.owner, id, sched); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	return id
}

func TestStart_FollowsSchedule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sched := exam.Schedule{OpensAt: t0.Add(time.Hour), ClosesAt: t0.Add(2 * time.Hour)}
	id := h.scheduledExam(t, examText("none", examfile.ShowScore, 1, 2), sched)
	code := h.code(t, id)

	h.clock = sched.OpensAt.Add(-time.Millisecond)
	_, err := h.svc.Start(ctx, code, "Ayu")
	var early *attempt.NotYetOpenError
	if !errors.As(err, &early) {
		t.Fatalf("Start before the open time: err = %v, want *NotYetOpenError", err)
	}
	if early.Entry.Title != "Exam none score" || !early.Entry.OpensAt.Equal(sched.OpensAt) || early.Entry.DurationMinutes != 60 {
		t.Errorf("NotYetOpenError.Entry = %+v", early.Entry)
	}
	if got := h.attemptRows(t); got != 0 {
		t.Fatalf("attempt rows after an early start = %d, want 0", got)
	}

	h.clock = sched.OpensAt
	h.startWith(t, code, "Ayu")
	h.clock = sched.ClosesAt.Add(-time.Millisecond)
	h.startWith(t, code, "Budi")

	h.clock = sched.ClosesAt
	_, err = h.svc.Start(ctx, code, "Citra")
	wantErr(t, "Start at the close time", err, attempt.ErrNoOpenExam)
	if got := h.attemptRows(t); got != 2 {
		t.Fatalf("attempt rows = %d, want 2", got)
	}
}

func TestStart_CloseTimeLeavesRunningAttempts(t *testing.T) {
	h := newHarness(t)
	sched := exam.Schedule{OpensAt: t0.Add(time.Hour), ClosesAt: t0.Add(2 * time.Hour)}
	id := h.scheduledExam(t, examText("none", examfile.ShowScore, 1, 2), sched)
	q := h.questions(t, id)[0]

	started := sched.ClosesAt.Add(-time.Minute)
	h.clock = started
	token := h.startIn(t, id, "Ayu")

	h.clock = sched.ClosesAt.Add(10 * time.Minute)
	if rem := h.save(t, token, q.ID, correctOption(q)); rem != defaultLength-11*time.Minute {
		t.Errorf("Remaining after the close time = %v, want %v", rem, defaultLength-11*time.Minute)
	}
	_, err := h.svc.Start(context.Background(), h.code(t, id), "Budi")
	wantErr(t, "Start after the close time", err, attempt.ErrNoOpenExam)
	h.submit(t, token)
	if s := h.only(t, id); s.Status != attempt.Submitted || s.Score != 1 || !s.StartedAt.Equal(started) {
		t.Errorf("attempt after the close time: %+v", s)
	}
}

func TestStart_ButtonOverridesSchedule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sched := exam.Schedule{OpensAt: t0.Add(time.Hour), ClosesAt: t0.Add(2 * time.Hour)}

	early := h.scheduledExam(t, examText("none", examfile.ShowScore, 1, 2), sched)
	if err := h.exams.Open(ctx, h.owner, early); err != nil {
		t.Fatal(err)
	}
	h.startIn(t, early, "Ayu")

	inside := h.scheduledExam(t, examText("none", examfile.ShowScore, 1, 2), sched)
	h.clock = sched.OpensAt.Add(time.Minute)
	if err := h.exams.Close(ctx, h.owner, inside); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Start(ctx, h.code(t, inside), "Budi")
	wantErr(t, "Start after Close inside the window", err, attempt.ErrNoOpenExam)
}

func TestStart_RecordsCurrentRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.importExam(t, examText("none", examfile.ShowScore, 1, 2))
	if err := h.exams.Replace(ctx, h.owner, a, []byte(examText("questions", examfile.ShowScore, 2, 2))); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	b := h.importExam(t, examText("none", examfile.ShowScore, 1, 2))
	for _, id := range []int64{a, b} {
		if err := h.exams.Open(ctx, h.owner, id); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	tokenA := h.startIn(t, a, "Ani")
	tokenB := h.startIn(t, b, "Budi")

	revisions := func() map[int64]int {
		t.Helper()
		rows, err := h.db.Query(`SELECT exam_id, revision FROM attempt`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[int64]int{}
		for rows.Next() {
			var examID int64
			var rev int
			if err := rows.Scan(&examID, &rev); err != nil {
				t.Fatal(err)
			}
			got[examID] = rev
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := map[int64]int{a: 2, b: 1}
	if got := revisions(); !reflect.DeepEqual(got, want) {
		t.Errorf("attempt revisions = %v, want %v", got, want)
	}
	h.submit(t, tokenA)
	h.submit(t, tokenB)
	if got := revisions(); !reflect.DeepEqual(got, want) {
		t.Errorf("attempt revisions after submit = %v, want %v", got, want)
	}
}
