package exam_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image/color"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

type storedOption struct {
	html    string
	correct bool
}

type storedQuestion struct {
	html string
	opts []storedOption
}

// storedExam is an exam as an older release left it: rows without a revision.
type storedExam struct {
	title, key, code string
	owner            int64
	createdAt        time.Time
	questions        []storedQuestion
}

// insertStored writes e by hand, the way an exam from before revisions sits
// in the database, and returns its id.
func (h *harness) insertStored(t *testing.T, e storedExam) int64 {
	t.Helper()
	res, err := h.db.Exec(`INSERT INTO exam (title, shuffle, show_result, monitor, duration_minutes, is_open, media_key, created_at, owner_id, code)
		VALUES (?, 'questions', 'score', 1, 45, 0, ?, ?, ?, ?)`, e.title, e.key, e.createdAt.UnixMilli(), e.owner, e.code)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for i, q := range e.questions {
		res, err := h.db.Exec(`INSERT INTO question (exam_id, position, body_html) VALUES (?, ?, ?)`, id, i+1, q.html)
		if err != nil {
			t.Fatal(err)
		}
		qid, _ := res.LastInsertId()
		for j, o := range q.opts {
			if _, err := h.db.Exec(`INSERT INTO question_option (question_id, position, body_html, is_correct) VALUES (?, ?, ?, ?)`,
				qid, j+1, o.html, o.correct); err != nil {
				t.Fatal(err)
			}
		}
	}
	return id
}

// paragraphs returns n renderer-shaped questions with two options each.
func paragraphs(n int) []storedQuestion {
	var qs []storedQuestion
	for i := range n {
		qs = append(qs, storedQuestion{
			html: fmt.Sprintf("<p>Question %d</p>\n", i+1),
			opts: []storedOption{{fmt.Sprintf("<p>Yes %d</p>\n", i+1), true}, {"<p>No</p>\n", false}},
		})
	}
	return qs
}

// dumpTables renders every row of the named tables in rowid order.
func dumpTables(t *testing.T, db *sql.DB, tables ...string) string {
	t.Helper()
	var b strings.Builder
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
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
			fmt.Fprintln(&b, vals...)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, "--", table)
	}
	return b.String()
}

var allTables = []string{"exam", "question", "question_option", "attempt", "exam_revision"}

// attemptRevisions returns the revision column of examID's attempts; -1 for NULL.
func (h *harness) attemptRevisions(t *testing.T, examID int64) []int {
	t.Helper()
	rows, err := h.db.Query(`SELECT COALESCE(revision, -1) FROM attempt WHERE exam_id = ? ORDER BY id`, examID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) mustBackfill(t *testing.T) (int, []exam.Unconverted) {
	t.Helper()
	created, skipped, err := h.svc.BackfillRevisions(context.Background())
	if err != nil {
		t.Fatalf("BackfillRevisions: %v", err)
	}
	return created, skipped
}

func TestBackfillRevisions_CreatesRevision1(t *testing.T) {
	h := newHarness(t)
	other := h.teacher(t, "other@example.test")
	firstAt := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	secondAt := time.Date(2026, 4, 2, 9, 15, 0, 0, time.UTC)
	frozen := h.insertStored(t, storedExam{title: "Frozen", key: "k1", code: "ABCDEF", owner: h.owner.TeacherID, createdAt: firstAt, questions: paragraphs(3)})
	draft := h.insertStored(t, storedExam{title: "Draft", key: "k2", code: "GHJKMN", owner: other.TeacherID, createdAt: secondAt, questions: paragraphs(2)})
	for _, name := range []string{"alice", "bob", "carol"} {
		h.insertAttempt(t, frozen, name)
	}

	created, skipped := h.mustBackfill(t)
	if created != 2 || len(skipped) != 0 {
		t.Fatalf("BackfillRevisions = %d, %v; want 2, none skipped", created, skipped)
	}
	for _, c := range []struct {
		id    int64
		at    time.Time
		email string
		title string
	}{{frozen, firstAt, "owner@example.test", "Frozen"}, {draft, secondAt, "other@example.test", "Draft"}} {
		revs := h.mustRevisions(t, c.id)
		if len(revs) != 1 {
			t.Fatalf("exam %d revisions = %+v, want one", c.id, revs)
		}
		r := revs[0]
		if r.Number != 1 || !r.CreatedAt.Equal(c.at) || r.TeacherEmail != c.email || r.Summary != "Imported" ||
			r.Title != c.title || r.Shuffle != examfile.ShuffleQuestions || r.ShowResult != examfile.ShowScore ||
			!r.Monitor || r.DurationMinutes != 45 {
			t.Errorf("exam %d revision = %+v", c.id, r)
		}
		if got, want := flatten(h.mustRevision(t, c.id, 1).Questions), flatten(h.mustQuestions(t, c.id)); !reflect.DeepEqual(got, want) {
			t.Errorf("exam %d revision questions =\n%+v\nwant\n%+v", c.id, got, want)
		}
	}
	if got := h.attemptRevisions(t, frozen); !slices.Equal(got, []int{1, 1, 1}) {
		t.Errorf("attempt revisions = %v, want [1 1 1]", got)
	}

	before := dumpTables(t, h.db, allTables...)
	created, skipped = h.mustBackfill(t)
	if created != 0 || len(skipped) != 0 {
		t.Errorf("second BackfillRevisions = %d, %v; want 0, none", created, skipped)
	}
	if after := dumpTables(t, h.db, allTables...); after != before {
		t.Errorf("second call changed rows:\n%s\nwant\n%s", after, before)
	}
}

func TestBackfillRevisions_LeavesExamsWithRevisionAlone(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(simpleExam))
	before := dumpTables(t, h.db, allTables...)

	created, skipped := h.mustBackfill(t)
	if created != 0 || len(skipped) != 0 {
		t.Errorf("BackfillRevisions = %d, %v; want 0, none", created, skipped)
	}
	if got := revisionNumbers(h.mustRevisions(t, id)); !slices.Equal(got, []int{1}) {
		t.Errorf("revisions = %v, want [1]", got)
	}
	if after := dumpTables(t, h.db, allTables...); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
}

func TestBackfillRevisions_StopsOnDatabaseError(t *testing.T) {
	h := newHarness(t)
	h.insertStored(t, storedExam{title: "Good", key: "k1", code: "PQRSTV", owner: h.owner.TeacherID, createdAt: fixedNow, questions: paragraphs(2)})
	if _, err := h.db.Exec(`DROP TABLE question_option`); err != nil {
		t.Fatal(err)
	}

	created, skipped, err := h.svc.BackfillRevisions(context.Background())
	var re *examfile.RebuildError
	if err == nil || errors.As(err, &re) {
		t.Fatalf("err = %v, want a database error", err)
	}
	if created != 0 || len(skipped) != 0 {
		t.Errorf("BackfillRevisions = %d, %v; want 0, none", created, skipped)
	}
}

// storedImageExam writes img under a new media folder key, as Import would,
// and returns renderer-shaped questions that show it.
func (h *harness) storedImageExam(t *testing.T, key string, img []byte) []storedQuestion {
	t.Helper()
	if err := h.media.Mkdir(key, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := h.media.WriteFile(key+"/"+imageName(img), img, 0o640); err != nil {
		t.Fatal(err)
	}
	return []storedQuestion{{
		html: `<p>Look <img src="/media/` + key + "/" + imageName(img) + `" alt="x"></p>` + "\n",
		opts: []storedOption{{"<p>a</p>\n", true}, {"<p>b</p>\n", false}},
	}}
}

const imageKey = "0123456789abcdef0123456789abcdef"

func TestBackfillRevisions_WithImages(t *testing.T) {
	h := newHarness(t)
	img := pngBytes(t, color.RGBA{10, 200, 10, 255})
	qs := h.storedImageExam(t, imageKey, img)
	id := h.insertStored(t, storedExam{title: "Pictures", key: imageKey, code: "PQRSTV", owner: h.owner.TeacherID, createdAt: fixedNow, questions: qs})

	created, skipped := h.mustBackfill(t)
	if created != 1 || len(skipped) != 0 {
		t.Fatalf("BackfillRevisions = %d, %v; want 1, none", created, skipped)
	}
	name := imageName(img)
	var dir, mapped, key string
	if err := h.db.QueryRow(`SELECT json_extract(content, '$.dir'), json_extract(content, '$.images."`+name+`"'), media_key
		FROM exam_revision WHERE exam_id = ? AND number = 1`, id).Scan(&dir, &mapped, &key); err != nil {
		t.Fatal(err)
	}
	if dir != "." || mapped != name || key != imageKey {
		t.Errorf("content dir %q, images[%s] = %q, media_key %q", dir, name, mapped, key)
	}
	if got := string(h.mustRevision(t, id, 1).Questions[0].BodyHTML); got != qs[0].html {
		t.Errorf("Revision(1) html = %q, want %q", got, qs[0].html)
	}
	requireLiveMatchesRevision(t, h, id)
}

// unconvertible inserts the two exams the rebuild cannot convert (a list,
// and an image whose file is gone) and one it can, in that order.
func (h *harness) unconvertible(t *testing.T) (list, missing, good int64) {
	t.Helper()
	list = h.insertStored(t, storedExam{title: "List", key: "k1", code: "ABCDEF", owner: h.owner.TeacherID, createdAt: fixedNow,
		questions: []storedQuestion{
			{html: "<p>Fine</p>\n", opts: []storedOption{{"<p>a</p>\n", true}, {"<p>b</p>\n", false}}},
			{html: "<ul>\n<li>x</li>\n</ul>\n", opts: []storedOption{{"<p>a</p>\n", true}, {"<p>b</p>\n", false}}},
		}})
	missing = h.insertStored(t, storedExam{title: "Missing", key: "k2", code: "GHJKMN", owner: h.owner.TeacherID, createdAt: fixedNow,
		questions: []storedQuestion{{
			html: `<p>Look <img src="/media/k2/` + imageName([]byte("gone")) + `" alt="x"></p>` + "\n",
			opts: []storedOption{{"<p>a</p>\n", true}, {"<p>b</p>\n", false}},
		}}})
	good = h.insertStored(t, storedExam{title: "Good", key: "k3", code: "PQRSTV", owner: h.owner.TeacherID, createdAt: fixedNow, questions: paragraphs(3)})
	return list, missing, good
}

func TestBackfillRevisions_SkipsUnconvertible(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	list, missing, good := h.unconvertible(t)
	h.insertAttempt(t, list, "alice")
	before := dumpTables(t, h.db, "exam", "question", "question_option", "attempt")

	created, skipped := h.mustBackfill(t)
	if created != 1 {
		t.Errorf("created = %d, want 1", created)
	}
	if len(skipped) != 2 || skipped[0].ExamID != list || skipped[1].ExamID != missing {
		t.Fatalf("skipped = %+v, want exams %d and %d", skipped, list, missing)
	}
	for _, s := range skipped {
		var re *examfile.RebuildError
		if !errors.As(s.Err, &re) {
			t.Errorf("exam %d: err = %T %v, want *examfile.RebuildError", s.ExamID, s.Err, s.Err)
		}
	}
	if got := revisionNumbers(h.mustRevisions(t, good)); !slices.Equal(got, []int{1}) {
		t.Errorf("good exam revisions = %v, want [1]", got)
	}
	for _, id := range []int64{list, missing} {
		if revs := h.mustRevisions(t, id); len(revs) != 0 {
			t.Errorf("exam %d revisions = %+v, want none", id, revs)
		}
		if ex := h.mustGet(t, id); ex.Revision != 0 {
			t.Errorf("exam %d Get.Revision = %d, want 0", id, ex.Revision)
		}
	}
	if after := dumpTables(t, h.db, "exam", "question", "question_option", "attempt"); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	h.insertAttempt(t, list, "bob")
	if got := h.attemptRevisions(t, list); !slices.Equal(got, []int{-1, -1}) {
		t.Errorf("attempt revisions = %v, want both NULL", got)
	}

	h.clock = fixedNow.Add(time.Hour)
	if err := h.svc.Replace(ctx, h.owner, missing, []byte(simpleExam)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	revs := h.mustRevisions(t, missing)
	if len(revs) != 1 || revs[0].Number != 1 || revs[0].Summary != "Replaced from file" {
		t.Errorf("revisions after Replace = %+v, want one: 1 Replaced from file", revs)
	}
}

func TestCheckRebuild(t *testing.T) {
	h := newHarness(t)
	list, missing, good := h.unconvertible(t)
	before := dumpTables(t, h.db, allTables...)

	results, err := exam.CheckRebuild(context.Background(), h.db, os.DirFS(h.dir))
	if err != nil {
		t.Fatalf("CheckRebuild: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v, want three", results)
	}
	wantIDs := []int64{list, missing, good}
	wantTitles := []string{"List", "Missing", "Good"}
	for i, r := range results {
		if r.ExamID != wantIDs[i] || r.Title != wantTitles[i] {
			t.Errorf("result %d = %+v, want exam %d %q", i, r, wantIDs[i], wantTitles[i])
		}
	}
	var re *examfile.RebuildError
	if !errors.As(results[0].Err, &re) || re.Question != 2 || re.Option != 0 {
		t.Errorf("list exam err = %v, want a RebuildError at question 2", results[0].Err)
	}
	if !errors.As(results[1].Err, &re) || re.Question != 1 {
		t.Errorf("missing-image exam err = %v, want a RebuildError at question 1", results[1].Err)
	}
	if g := results[2]; g.Err != nil || g.Questions != 3 || g.Options != 6 {
		t.Errorf("good exam = %+v, want 3 questions, 6 options, no error", g)
	}
	if after := dumpTables(t, h.db, allTables...); after != before {
		t.Errorf("rows changed:\n%s\nwant\n%s", after, before)
	}
	if entries := h.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries = %v, want none", entries)
	}
}
