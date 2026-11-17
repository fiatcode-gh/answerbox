package exam_test

import (
	"context"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

func (h *harness) mustRevisions(t *testing.T, id int64) []exam.Revision {
	t.Helper()
	revs, err := h.svc.Revisions(context.Background(), id)
	if err != nil {
		t.Fatalf("Revisions(%d): %v", id, err)
	}
	return revs
}

func (h *harness) mustRevision(t *testing.T, id int64, number int) exam.Revision {
	t.Helper()
	rev, err := h.svc.Revision(context.Background(), id, number)
	if err != nil {
		t.Fatalf("Revision(%d, %d): %v", id, number, err)
	}
	return rev
}

func revisionNumbers(revs []exam.Revision) []int {
	var out []int
	for _, r := range revs {
		out = append(out, r.Number)
	}
	return out
}

// requireLiveMatchesRevision fails unless the latest revision of examID holds
// the exam's current title, settings and questions.
func requireLiveMatchesRevision(t *testing.T, h *harness, examID int64) {
	t.Helper()
	revs := h.mustRevisions(t, examID)
	if len(revs) == 0 {
		t.Fatalf("exam %d has no revision", examID)
	}
	rev := h.mustRevision(t, examID, revs[0].Number)
	ex := h.mustGet(t, examID)
	if ex.Revision != rev.Number || rev.Title != ex.Title || rev.Shuffle != ex.Shuffle || rev.ShowResult != ex.ShowResult ||
		rev.Monitor != ex.Monitor || rev.DurationMinutes != ex.DurationMinutes {
		t.Errorf("latest revision %+v does not match exam %+v", rev, ex)
	}
	if got, want := flatten(rev.Questions), flatten(h.mustQuestions(t, examID)); !reflect.DeepEqual(got, want) {
		t.Errorf("revision %d questions =\n%+v\nlive questions\n%+v", rev.Number, got, want)
	}
}

func TestImport_WritesRevision1(t *testing.T) {
	h := newHarness(t)
	zipData, img := imageExam(t, "Pictures", color.Black)
	id := h.mustImport(t, zipData)

	revs := h.mustRevisions(t, id)
	if len(revs) != 1 {
		t.Fatalf("Revisions = %+v, want one", revs)
	}
	r := revs[0]
	if r.Number != 1 || !r.CreatedAt.Equal(fixedNow) || r.TeacherEmail != "owner@example.test" || r.Summary != "Imported" ||
		r.Title != "Pictures" || r.Shuffle != examfile.ShuffleQuestions || r.ShowResult != examfile.ShowNone ||
		!r.Monitor || r.DurationMinutes != 60 || r.Questions != nil {
		t.Errorf("revision 1 = %+v", r)
	}

	full := h.mustRevision(t, id, 1)
	if got, want := flatten(full.Questions), flatten(h.mustQuestions(t, id)); !reflect.DeepEqual(got, want) {
		t.Errorf("Revision(1) questions =\n%+v\nwant\n%+v", got, want)
	}
	for _, q := range full.Questions {
		if q.ID != 0 {
			t.Errorf("question %d has ID %d, want 0", q.Position, q.ID)
		}
		for _, o := range q.Options {
			if o.ID != 0 {
				t.Errorf("option %d of question %d has ID %d, want 0", o.Position, q.Position, o.ID)
			}
		}
	}

	var dir, image, qSource, aSource, bSource string
	err := h.db.QueryRow(`SELECT json_extract(content, '$.dir'), json_extract(content, '$.images."i.png"'),
		json_extract(content, '$.questions[0].source'), json_extract(content, '$.questions[0].options[0].source'),
		json_extract(content, '$.questions[0].options[1].source')
		FROM exam_revision WHERE exam_id = ? AND number = 1`, id).Scan(&dir, &image, &qSource, &aSource, &bSource)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "." || image != imageName(img) || qSource != "Q ![x](i.png)" || aSource != "a" || bSource != "b" {
		t.Errorf("content: dir %q, image %q, sources %q %q %q", dir, image, qSource, aSource, bSource)
	}

	if ex := h.mustGet(t, id); ex.Revision != 1 {
		t.Errorf("Get.Revision = %d, want 1", ex.Revision)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestReplace_WritesNextRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	oldZip, oldImg := imageExam(t, "Old", color.Black)
	id := h.mustImport(t, oldZip)
	oldKey := h.mediaKey(t, id)
	oldQuestions := flatten(h.mustQuestions(t, id))

	h.clock = fixedNow.Add(time.Hour)
	admin := h.admin(t)
	newZip := buildZip(t,
		zipEntry{"exam.md", []byte("# New\nshuffle: none\nshow_result: review\nmonitor: off\nduration: 30\n\n1. Fresh ![x](i.png)\n- x\n- y *\n")},
		zipEntry{"i.png", pngBytes(t, color.White)},
	)
	if err := h.svc.Replace(ctx, admin, id, newZip); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	revs := h.mustRevisions(t, id)
	if got := revisionNumbers(revs); !slices.Equal(got, []int{2, 1}) {
		t.Fatalf("Revisions = %v, want [2 1]", got)
	}
	r := revs[0]
	if r.Summary != "Replaced from file" || r.TeacherEmail != "admin@example.test" || !r.CreatedAt.Equal(fixedNow.Add(time.Hour)) ||
		r.Title != "New" || r.Shuffle != examfile.ShuffleNone || r.ShowResult != examfile.ShowReview || r.Monitor || r.DurationMinutes != 30 {
		t.Errorf("revision 2 = %+v", r)
	}

	first := h.mustRevision(t, id, 1)
	if first.Title != "Old" || first.Shuffle != examfile.ShuffleQuestions || first.ShowResult != examfile.ShowNone ||
		!first.Monitor || first.DurationMinutes != 60 || first.Summary != "Imported" || first.TeacherEmail != "owner@example.test" {
		t.Errorf("revision 1 = %+v", first)
	}
	if got := flatten(first.Questions); !reflect.DeepEqual(got, oldQuestions) {
		t.Errorf("revision 1 questions =\n%+v\nwant\n%+v", got, oldQuestions)
	}
	if src := "/media/" + oldKey + "/" + imageName(oldImg); !strings.Contains(string(first.Questions[0].BodyHTML), src) {
		t.Errorf("revision 1 question html = %q, want %s", first.Questions[0].BodyHTML, src)
	}
	if _, err := os.Stat(filepath.Join(h.dir, oldKey, imageName(oldImg))); err != nil {
		t.Errorf("old image: %v", err)
	}

	if ex := h.mustGet(t, id); ex.Revision != 2 {
		t.Errorf("Get.Revision = %d, want 2", ex.Revision)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestReplace_RefusedWritesNoRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	oldZip, _ := imageExam(t, "Old", color.Black)
	id := h.mustImport(t, oldZip)
	newZip, _ := imageExam(t, "New", color.White)
	before := h.mustRevisions(t, id)
	entriesBefore := h.mediaEntries(t)

	check := func(t *testing.T) {
		t.Helper()
		if after := h.mustRevisions(t, id); !reflect.DeepEqual(after, before) {
			t.Errorf("Revisions = %+v, want %+v", after, before)
		}
		if entries := h.mediaEntries(t); !slices.Equal(entries, entriesBefore) {
			t.Errorf("media entries = %v, want %v", entries, entriesBefore)
		}
	}

	t.Run("foreign teacher", func(t *testing.T) {
		other := h.teacher(t, "other@example.test")
		if err := h.svc.Replace(ctx, other, id, newZip); !errors.Is(err, exam.ErrNotFound) {
			t.Fatalf("Replace = %v, want ErrNotFound", err)
		}
		check(t)
	})
	t.Run("invalid upload", func(t *testing.T) {
		assertExamfileError(t, h.svc.Replace(ctx, h.owner, id, []byte("# Broken\n\n1. No options\n")))
		check(t)
	})
	t.Run("frozen", func(t *testing.T) {
		h.insertAttempt(t, id, "alice")
		if err := h.svc.Replace(ctx, h.owner, id, newZip); !errors.Is(err, exam.ErrFrozen) {
			t.Fatalf("Replace = %v, want ErrFrozen", err)
		}
		check(t)
	})
}

func TestReplace_AfterAttemptsDeleted_ContinuesNumbers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Replace(ctx, h.owner, id, []byte("# Second\n\n1. Q\n- a\n- b *\n")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	attemptID := h.insertAttempt(t, id, "bob")
	if _, err := h.db.Exec("DELETE FROM attempt WHERE id = ?", attemptID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Replace(ctx, h.owner, id, []byte("# Third\n\n1. Q\n- a *\n- b\n")); err != nil {
		t.Fatalf("Replace after delete: %v", err)
	}
	if got := revisionNumbers(h.mustRevisions(t, id)); !slices.Equal(got, []int{3, 2, 1}) {
		t.Errorf("Revisions = %v, want [3 2 1]", got)
	}
	if rev := h.mustRevision(t, id, 3); rev.Title != "Third" {
		t.Errorf("revision 3 title = %q", rev.Title)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestRevision_Unknown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Replace(ctx, h.owner, id, []byte("# Second\n\n1. Q\n- a\n- b *\n")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	other := h.mustImport(t, []byte(simpleExam))

	for _, c := range []struct {
		examID int64
		number int
	}{{id, 0}, {id, 3}, {other, 2}} {
		if _, err := h.svc.Revision(ctx, c.examID, c.number); !errors.Is(err, exam.ErrNotFound) {
			t.Errorf("Revision(%d, %d) = %v, want ErrNotFound", c.examID, c.number, err)
		}
	}
	revs, err := h.svc.Revisions(ctx, other+100)
	if err != nil || len(revs) != 0 {
		t.Errorf("Revisions(unknown) = %+v, %v; want empty, nil", revs, err)
	}
}
