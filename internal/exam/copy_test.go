package exam_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
)

func (h *harness) mustCopy(t *testing.T, actor exam.Actor, id int64) int64 {
	t.Helper()
	copyID, err := h.svc.Copy(context.Background(), actor, id)
	if err != nil {
		t.Fatalf("Copy(%d): %v", id, err)
	}
	return copyID
}

func (h *harness) examCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM exam`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// requireSameQuestions fails unless exams a and b have the same questions,
// options, answer key and HTML once b's media key is read as a's.
func requireSameQuestions(t *testing.T, h *harness, a, b int64) {
	t.Helper()
	keyA, keyB := h.mediaKey(t, a), h.mediaKey(t, b)
	qa, qb := flatten(h.mustQuestions(t, a)), flatten(h.mustQuestions(t, b))
	replaceKey(qb, keyB, keyA)
	if !reflect.DeepEqual(qa, qb) {
		t.Errorf("exam %d questions =\n%+v\nexam %d questions =\n%+v", a, qa, b, qb)
	}
}

func TestCopy_NewClosedExamOwnedByCopier(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, _ := imageExam(t, "First", color.White)
	id := h.mustImport(t, first)
	data, img := imageExam(t, "Pictures", color.Black)
	if err := h.svc.Replace(ctx, h.owner, id, data); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Open(ctx, h.owner, id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetSchedule(ctx, h.owner, id, exam.Schedule{ClosesAt: fixedNow.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	src := h.mustGet(t, id)

	h.clock = fixedNow.Add(time.Hour)
	admin := h.admin(t)
	copyID := h.mustCopy(t, admin, id)
	if copyID == id {
		t.Fatalf("copy id = source id %d", id)
	}
	c, err := h.svc.Get(ctx, admin, copyID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != src.Title || c.Shuffle != src.Shuffle || c.ShowResult != src.ShowResult ||
		c.Monitor != src.Monitor || c.DurationMinutes != src.DurationMinutes {
		t.Errorf("copy settings = %+v, want those of %+v", c, src)
	}
	if c.Open || c.Schedule.Set() {
		t.Errorf("copy open = %v, schedule = %+v; want closed without schedule", c.Open, c.Schedule)
	}
	if !examCode.MatchString(c.Code) || c.Code == src.Code {
		t.Errorf("copy code = %q, source code %q; want a new well-formed code", c.Code, src.Code)
	}
	if c.OwnerID != admin.TeacherID || c.AttemptCount != 0 || c.Revision != 1 {
		t.Errorf("copy owner %d, attempts %d, revision %d; want %d, 0, 1", c.OwnerID, c.AttemptCount, c.Revision, admin.TeacherID)
	}
	if !c.CreatedAt.Equal(h.clock) {
		t.Errorf("copy created at %v, want %v", c.CreatedAt, h.clock)
	}

	revs := h.mustRevisions(t, copyID)
	want := fmt.Sprintf("Copied from Pictures (%s), revision 2", src.Code)
	if len(revs) != 1 || revs[0].Number != 1 || revs[0].Summary != want || revs[0].TeacherEmail != "admin@example.test" {
		t.Errorf("copy revisions = %+v, want one: 1 %q by admin@example.test", revs, want)
	}

	srcKey, copyKey := h.mediaKey(t, id), h.mediaKey(t, copyID)
	if copyKey == srcKey || !hexKey.MatchString(copyKey) {
		t.Errorf("copy media key = %q, source %q; want a new key", copyKey, srcKey)
	}
	requireSameQuestions(t, h, id, copyID)
	if got, err := h.media.ReadFile(copyKey + "/" + imageName(img)); err != nil || !bytes.Equal(got, img) {
		t.Errorf("copy image under %s: %v", copyKey, err)
	}
	if _, err := h.svc.Get(ctx, h.owner, copyID); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("source owner Get(copy): err = %v, want ErrNotFound", err)
	}
}

// examDump renders every row that belongs to examID.
func (h *harness) examDump(t *testing.T, examID int64) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT * FROM exam WHERE id = ?`,
		`SELECT * FROM question WHERE exam_id = ? ORDER BY id`,
		`SELECT o.* FROM question_option o JOIN question q ON q.id = o.question_id WHERE q.exam_id = ? ORDER BY o.id`,
		`SELECT * FROM exam_revision WHERE exam_id = ? ORDER BY number`,
		`SELECT * FROM attempt WHERE exam_id = ? ORDER BY id`,
		`SELECT a.* FROM answer a JOIN attempt t ON t.id = a.attempt_id WHERE t.exam_id = ? ORDER BY a.attempt_id, a.question_id`,
	} {
		rows, err := h.db.Query(q, examID)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		dumpRows(t, &b, rows)
		fmt.Fprintln(&b, "--")
	}
	return b.String()
}

func dumpRows(t *testing.T, b *strings.Builder, rows *sql.Rows) {
	t.Helper()
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(b, vals...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// mediaUnder returns the media entries below the given keys.
func (h *harness) mediaUnder(t *testing.T, keys ...string) []string {
	t.Helper()
	var out []string
	for _, e := range h.mediaEntries(t) {
		for _, k := range keys {
			if strings.HasPrefix(e, k+"/") {
				out = append(out, e)
			}
		}
	}
	return out
}

func TestCopy_SourceUnchanged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, _ := imageExam(t, "First", color.White)
	id := h.mustImport(t, first)
	firstKey := h.mediaKey(t, id)
	data, _ := imageExam(t, "Second", color.Black)
	if err := h.svc.Replace(ctx, h.owner, id, data); err != nil {
		t.Fatal(err)
	}
	keys := []string{firstKey, h.mediaKey(t, id)}
	q := h.mustQuestions(t, id)[0]
	for i, name := range []string{"Ayu", "Budi"} {
		aid := h.insertAttempt(t, id, name)
		if _, err := h.db.Exec(`INSERT INTO answer (attempt_id, question_id, option_id) VALUES (?, ?, ?)`,
			aid, q.ID, q.Options[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	before, beforeFiles := h.examDump(t, id), h.mediaUnder(t, keys...)
	if len(beforeFiles) != 4 {
		t.Fatalf("source media = %v, want two folders with one image each", beforeFiles)
	}

	h.clock = fixedNow.Add(time.Hour)
	copyID := h.mustCopy(t, h.owner, id)

	if after := h.examDump(t, id); after != before {
		t.Errorf("source rows changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	if after := h.mediaUnder(t, keys...); !slices.Equal(after, beforeFiles) {
		t.Errorf("source media = %v, want %v", after, beforeFiles)
	}
	if n := h.mustGet(t, copyID).AttemptCount; n != 0 {
		t.Errorf("copy has %d attempts, want 0", n)
	}
	if revs := h.attemptRevisions(t, copyID); len(revs) != 0 {
		t.Errorf("copy attempts = %v, want none", revs)
	}
}

func TestCopy_ScopeAndNoRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data, _ := imageExam(t, "Mine", color.Black)
	id := h.mustImport(t, data)
	old := h.insertStored(t, storedExam{title: "Old", key: "oldkey", code: "XDEXMM", owner: h.owner.TeacherID, questions: paragraphs(1)})
	other := h.teacher(t, "other@example.test")
	n, entries := h.examCount(t), h.mediaEntries(t)

	for _, c := range []struct {
		name  string
		actor exam.Actor
		id    int64
		want  error
	}{
		{"other teacher", other, id, exam.ErrNotFound},
		{"unknown id", h.owner, 999, exam.ErrNotFound},
		{"no revision", h.owner, old, exam.ErrNoRevision},
	} {
		if _, err := h.svc.Copy(ctx, c.actor, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if got := h.examCount(t); got != n {
			t.Errorf("%s: exams = %d, want %d", c.name, got, n)
		}
		if got := h.mediaEntries(t); !slices.Equal(got, entries) {
			t.Errorf("%s: media entries = %v, want %v", c.name, got, entries)
		}
	}
}

func TestCopy_FailureLeavesNoMedia(t *testing.T) {
	abcdef := codeBytes(0, 1, 2, 3, 4, 5)
	random := slices.Concat(
		bytes.Repeat([]byte{1}, 16), abcdef,
		bytes.Repeat([]byte{2}, 16), bytes.Repeat(abcdef, 5),
	)
	h := newHarnessWithRandom(t, bytes.NewReader(random))
	data, _ := imageExam(t, "Unlucky", color.Black)
	id := h.mustImport(t, data)
	entries := h.mediaEntries(t)

	_, err := h.svc.Copy(context.Background(), h.owner, id)
	if err == nil || !strings.Contains(err.Error(), "could not generate a unique exam code") {
		t.Fatalf("Copy after five collisions: err = %v, want the unique-code failure", err)
	}
	if n := h.examCount(t); n != 1 {
		t.Errorf("exams = %d, want 1", n)
	}
	if got := h.mediaEntries(t); !slices.Equal(got, entries) {
		t.Errorf("media entries = %v, want %v", got, entries)
	}
}

func TestCopy_OfUpgradedExam(t *testing.T) {
	h := newHarness(t)
	id := h.insertStored(t, storedExam{title: "Upgraded", key: "upgradedkey", code: "PGRADE", owner: h.owner.TeacherID, questions: paragraphs(3)})
	if created, skipped := h.mustBackfill(t); created != 1 || len(skipped) != 0 {
		t.Fatalf("BackfillRevisions = %d, %v; want 1, none", created, skipped)
	}
	copyID := h.mustCopy(t, h.owner, id)
	if a, b := flatten(h.mustQuestions(t, id)), flatten(h.mustQuestions(t, copyID)); !reflect.DeepEqual(a, b) {
		t.Errorf("copy questions =\n%+v\nwant\n%+v", b, a)
	}
	requireEqualExams(t, h, id, copyID)
	revs := h.mustRevisions(t, copyID)
	if want := "Copied from Upgraded (PGRADE), revision 1"; len(revs) != 1 || revs[0].Summary != want {
		t.Errorf("copy revisions = %+v, want one %q", revs, want)
	}
}

func TestCopy_OfCopy(t *testing.T) {
	h := newHarness(t)
	data, _ := imageExam(t, "Chain", color.Black)
	id := h.mustImport(t, data)
	first := h.mustCopy(t, h.owner, id)
	second := h.mustCopy(t, h.owner, first)
	requireEqualExams(t, h, id, second)
	revs := h.mustRevisions(t, second)
	want := fmt.Sprintf("Copied from Chain (%s), revision 1", h.mustGet(t, first).Code)
	if len(revs) != 1 || revs[0].Summary != want {
		t.Errorf("copy of copy revisions = %+v, want one %q", revs, want)
	}
}
