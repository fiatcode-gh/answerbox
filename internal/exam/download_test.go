package exam_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

func (h *harness) mustDownload(t *testing.T, actor exam.Actor, id int64) exam.File {
	t.Helper()
	f, err := h.svc.Download(context.Background(), actor, id)
	if err != nil {
		t.Fatalf("Download(%d): %v", id, err)
	}
	return f
}

// requireEqualExams fails unless exams a and b have the same title,
// settings, questions, options, answer key and HTML, once b's media key is
// read as a's.
func requireEqualExams(t *testing.T, h *harness, a, b int64) {
	t.Helper()
	ea, eb := h.mustGet(t, a), h.mustGet(t, b)
	type settings struct {
		Title      string
		Shuffle    examfile.Shuffle
		ShowResult examfile.ShowResult
		Monitor    bool
		Duration   int
	}
	sa := settings{ea.Title, ea.Shuffle, ea.ShowResult, ea.Monitor, ea.DurationMinutes}
	sb := settings{eb.Title, eb.Shuffle, eb.ShowResult, eb.Monitor, eb.DurationMinutes}
	if sa != sb {
		t.Errorf("exam %d settings = %+v, exam %d settings = %+v", a, sa, b, sb)
	}
	keyA, keyB := h.mediaKey(t, a), h.mediaKey(t, b)
	qa, qb := flatten(h.mustQuestions(t, a)), flatten(h.mustQuestions(t, b))
	replaceKey(qb, keyB, keyA)
	if !reflect.DeepEqual(qa, qb) {
		t.Errorf("exam %d questions =\n%+v\nexam %d questions =\n%+v", a, qa, b, qb)
	}
}

const formattedExam = "# Formatting & Co\n" +
	"shuffle: none\n" +
	"show_result: review\n" +
	"monitor: off\n" +
	"duration: 25\n" +
	"\n" +
	"1. A **bold** start with `code span`\n" +
	"   and a second line.\n" +
	"\n" +
	"   ```\n" +
	"   fenced\n" +
	"     block\n" +
	"   ```\n" +
	"- *plain* option\n" +
	"- `x < y` *\n" +
	"\n" +
	"2. Second\n" +
	"- one\n" +
	"- two *\n" +
	"- three\n"

func TestDownload_TextExam(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(formattedExam))
	f := h.mustDownload(t, h.owner, id)
	if want := "exam-" + h.mustGet(t, id).Code + ".md"; f.Name != want {
		t.Errorf("Name = %q, want %q", f.Name, want)
	}
	if f.ContentType != "text/markdown; charset=utf-8" {
		t.Errorf("ContentType = %q", f.ContentType)
	}
	again := h.mustImport(t, f.Data)
	requireEqualExams(t, h, id, again)
}

// imageZip is an exam whose text sits in a folder and references one image
// below it, with a space in its name, and one beside the folder.
func imageZip(t *testing.T) (data, inner, shared []byte) {
	t.Helper()
	inner, shared = pngBytes(t, color.RGBA{10, 200, 10, 255}), pngBytes(t, color.RGBA{10, 10, 200, 255})
	text := "# Pictures\nshuffle: questions\n\n1. Look ![a](img/a%20b.png)\n- yes *\n- no\n\n2. And ![s](../shared.png)\n- ![again](img/a%20b.png)\n- other *\n"
	data = buildZip(t,
		zipEntry{"exam/quiz.md", []byte(text)},
		zipEntry{"exam/img/a b.png", inner},
		zipEntry{"shared.png", shared},
	)
	return data, inner, shared
}

func TestDownload_ImageExamZip(t *testing.T) {
	h := newHarness(t)
	data, inner, shared := imageZip(t)
	id := h.mustImport(t, data)
	f := h.mustDownload(t, h.owner, id)
	if want := "exam-" + h.mustGet(t, id).Code + ".zip"; f.Name != want {
		t.Errorf("Name = %q, want %q", f.Name, want)
	}
	if f.ContentType != "application/zip" {
		t.Errorf("ContentType = %q", f.ContentType)
	}
	u, err := examfile.ReadUpload(f.Data)
	if err != nil {
		t.Fatalf("ReadUpload: %v", err)
	}
	if u.Dir != "exam" {
		t.Errorf("Dir = %q, want exam", u.Dir)
	}
	zr, err := zip.NewReader(bytes.NewReader(f.Data), int64(len(f.Data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range zr.File {
		names = append(names, e.Name)
	}
	if want := []string{"exam/exam.md", "exam/img/a b.png", "shared.png"}; !slices.Equal(names, want) {
		t.Errorf("zip entries = %q, want %q", names, want)
	}
	want := map[string][]byte{"exam/img/a b.png": inner, "shared.png": shared}
	if !reflect.DeepEqual(u.Files, want) {
		t.Errorf("files = %q, want exam/img/a b.png and shared.png", slices.Sorted(maps.Keys(u.Files)))
	}
	again := h.mustImport(t, f.Data)
	requireEqualExams(t, h, id, again)
}

func TestDownload_CurrentRevisionAfterReplace(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte("# Old title\n\n1. Old question\n- a *\n- b\n"))
	if err := h.svc.Replace(context.Background(), h.owner, id, []byte("# New title\nduration: 30\n\n1. New question\n- c\n- d *\n")); err != nil {
		t.Fatal(err)
	}
	f := h.mustDownload(t, h.owner, id)
	for _, old := range []string{"Old title", "Old question"} {
		if bytes.Contains(f.Data, []byte(old)) {
			t.Errorf("download holds %q:\n%s", old, f.Data)
		}
	}
	again := h.mustImport(t, f.Data)
	requireEqualExams(t, h, id, again)
}

func TestDownload_FrozenExam(t *testing.T) {
	h := newHarness(t)
	data, _, _ := imageZip(t)
	id := h.mustImport(t, data)
	before := h.mustDownload(t, h.owner, id)
	h.insertAttempt(t, id, "Ayu")
	after := h.mustDownload(t, h.owner, id)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("download changed after an attempt")
	}
}

func TestDownload_UpgradedExam(t *testing.T) {
	h := newHarness(t)
	var qs []storedQuestion
	for i := range 20 {
		q := storedQuestion{html: fmt.Sprintf("<p>Question number %d</p>\n", i+1)}
		for j := range 4 {
			q.opts = append(q.opts, storedOption{fmt.Sprintf("<p>Option %d of %d</p>\n", j+1, i+1), j == i%4})
		}
		qs = append(qs, q)
	}
	id := h.insertStored(t, storedExam{title: "Upgraded", key: "upgradedkey", code: "PGRADE", owner: h.owner.TeacherID, questions: qs})
	if created, skipped := h.mustBackfill(t); created != 1 || len(skipped) != 0 {
		t.Fatalf("BackfillRevisions = %d, %v; want 1, none", created, skipped)
	}
	f := h.mustDownload(t, h.owner, id)
	if !strings.HasSuffix(f.Name, ".md") {
		t.Errorf("Name = %q, want .md", f.Name)
	}
	again := h.mustImport(t, f.Data)
	requireEqualExams(t, h, id, again)
	got := flatten(h.mustQuestions(t, again))
	blocks := 0
	for i, q := range got {
		if q.html != qs[i].html {
			t.Errorf("question %d html = %q, want %q", i+1, q.html, qs[i].html)
		}
		blocks++
		for j, o := range q.opts {
			if o.html != qs[i].opts[j].html {
				t.Errorf("question %d option %d html = %q, want %q", i+1, j+1, o.html, qs[i].opts[j].html)
			}
			blocks++
		}
	}
	if blocks != 100 {
		t.Errorf("compared %d blocks, want 100", blocks)
	}
}

func TestDownload_ScopeAndNoRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	other := h.teacher(t, "other@example.test")
	if _, err := h.svc.Download(ctx, other, id); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("other teacher: err = %v, want ErrNotFound", err)
	}
	if _, err := h.svc.Download(ctx, h.owner, 999); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	if got, want := h.mustDownload(t, h.admin(t), id), h.mustDownload(t, h.owner, id); !reflect.DeepEqual(got, want) {
		t.Errorf("admin download = %+v, want %+v", got, want)
	}
	old := h.insertStored(t, storedExam{title: "Old", key: "oldkey", code: "XDEXMM", owner: h.owner.TeacherID, questions: paragraphs(1)})
	if _, err := h.svc.Download(ctx, h.owner, old); !errors.Is(err, exam.ErrNoRevision) {
		t.Errorf("exam without revision: err = %v, want ErrNoRevision", err)
	}
}

func TestDownload_Deterministic(t *testing.T) {
	h := newHarness(t)
	data, _, _ := imageZip(t)
	id := h.mustImport(t, data)
	a, b := h.mustDownload(t, h.owner, id), h.mustDownload(t, h.owner, id)
	if !bytes.Equal(a.Data, b.Data) {
		t.Errorf("two downloads differ")
	}
}
