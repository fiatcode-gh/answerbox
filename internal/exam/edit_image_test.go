package exam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// withImage returns q carrying data as its uploaded image.
func withImage(q exam.QuestionSource, data []byte) exam.QuestionSource {
	q.Image = data
	return q
}

// revisionImages returns content.images of examID's latest revision.
func (h *harness) revisionImages(t *testing.T, id int64) map[string]string {
	t.Helper()
	var doc string
	if err := h.db.QueryRow(`SELECT content FROM exam_revision WHERE exam_id = ? ORDER BY number DESC LIMIT 1`, id).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	var content struct {
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal([]byte(doc), &content); err != nil {
		t.Fatal(err)
	}
	return content.Images
}

// requireImageQuestion fails unless question n (1-based) of examID ends with
// a reference to name and its live HTML points at the exam's media folder.
func (h *harness) requireImageQuestion(t *testing.T, id int64, n int, name string) {
	t.Helper()
	src := h.mustSource(t, h.owner, id)
	if got := src.Questions[n-1].Text; !strings.HasSuffix(got, "\n![]("+name+")") {
		t.Errorf("question %d Source = %q, want it to end with a line ![](%s)", n, got, name)
	}
	key := h.mediaKey(t, id)
	if html := string(h.mustQuestions(t, id)[n-1].BodyHTML); !strings.Contains(html, "/media/"+key+"/"+name) {
		t.Errorf("question %d HTML = %q, want /media/%s/%s", n, html, key, name)
	}
}

// pseudoPNG is size bytes that start with the PNG signature and do not
// compress.
func pseudoPNG(size int, seed byte) []byte {
	data := make([]byte, size)
	rand.NewChaCha8([32]byte{seed}).Read(data)
	copy(data, "\x89PNG\r\n\x1a\n")
	return data
}

func TestSaveQuestion_ImageJoinsFolder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	key := h.mediaKey(t, id)
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})
	name := imageName(img)

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 1, withImage(question("First", 0, "a", "b"), img)))
	if got, want := h.mediaEntries(t), []string{key + "/", key + "/" + name}; !slices.Equal(got, want) {
		t.Errorf("media entries = %q, want %q", got, want)
	}
	h.requireImageQuestion(t, id, 1, name)
	if got := h.mediaKey(t, id); got != key {
		t.Errorf("media_key = %q, want %q", got, key)
	}
	if got, want := h.revisionImages(t, id), map[string]string{name: name}; !reflect.DeepEqual(got, want) {
		t.Errorf("content.images = %q, want %q", got, want)
	}
	if got := h.latestSummary(t, id); got != "Question 1 edited" {
		t.Errorf("summary = %q", got)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestSaveQuestion_ImageInSubfolderExam(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data, inner, shared := imageZip(t)
	id := h.mustImport(t, data)
	key := h.mediaKey(t, id)
	before := h.mediaEntries(t)
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})
	name := imageName(img)

	q := withImage(question("Look ![a](img/a%20b.png)", 0, "yes", "no"), img)
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 1, q))
	want := map[string]string{"exam/img/a b.png": imageName(inner), "shared.png": imageName(shared), "exam/" + name: name}
	if got := h.revisionImages(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("content.images = %q, want %q", got, want)
	}
	if got, want := h.mediaEntries(t), slices.Sorted(slices.Values(append(before, key+"/"+name))); !slices.Equal(got, want) {
		t.Errorf("media entries = %q, want %q", got, want)
	}
	for _, old := range [][]byte{inner, shared} {
		got, err := h.media.ReadFile(key + "/" + imageName(old))
		if err != nil || !bytes.Equal(got, old) {
			t.Errorf("old image %s changed: %v", imageName(old), err)
		}
	}
	h.requireImageQuestion(t, id, 1, name)
	requireLiveMatchesRevision(t, h, id)
}

func TestSaveQuestion_ImageOnEmptyText(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(editExam))
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(context.Background(), h.owner, id, 1, 1, withImage(question("  \n", 0, "a", "b"), img)))
	if got, want := h.mustSource(t, h.owner, id).Questions[0].Text, "![]("+imageName(img)+")"; got != want {
		t.Errorf("Source = %q, want %q", got, want)
	}
}

func TestSaveQuestion_SameImageTwice(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})

	mustNil(t, "SaveQuestion 1", h.svc.SaveQuestion(ctx, h.owner, id, 1, 1, withImage(question("First", 0, "a", "b"), img)))
	before := h.mediaEntries(t)
	mustNil(t, "SaveQuestion 2", h.svc.SaveQuestion(ctx, h.owner, id, 2, 2, withImage(question("Second", 1, "c", "d", "e"), img)))
	if got := h.mediaEntries(t); !slices.Equal(got, before) {
		t.Errorf("media entries = %q, want %q", got, before)
	}
	if got := h.current(t, id); got != 3 {
		t.Errorf("revision = %d, want 3", got)
	}
	h.requireImageQuestion(t, id, 2, imageName(img))
	requireLiveMatchesRevision(t, h, id)
}

func TestAddQuestion_WithImage(t *testing.T) {
	h := newHarness(t)
	id := h.mustImport(t, []byte(editExam))
	key := h.mediaKey(t, id)
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})
	name := imageName(img)

	mustNil(t, "AddQuestion", h.svc.AddQuestion(context.Background(), h.owner, id, 1, withImage(question("Third", 0, "x", "y"), img)))
	if got, want := h.mediaEntries(t), []string{key + "/", key + "/" + name}; !slices.Equal(got, want) {
		t.Errorf("media entries = %q, want %q", got, want)
	}
	h.requireImageQuestion(t, id, 3, name)
	if got := h.mediaKey(t, id); got != key {
		t.Errorf("media_key = %q, want %q", got, key)
	}
	if got, want := h.revisionImages(t, id), map[string]string{name: name}; !reflect.DeepEqual(got, want) {
		t.Errorf("content.images = %q, want %q", got, want)
	}
	if got := h.latestSummary(t, id); got != "Question 3 added" {
		t.Errorf("summary = %q", got)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestCreate_WithImage(t *testing.T) {
	h := newHarness(t)
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})
	name := imageName(img)

	id, err := h.svc.Create(context.Background(), h.owner, defaultSettings, withImage(question("First", 0, "a", "b"), img))
	mustNil(t, "Create", err)
	key := h.mediaKey(t, id)
	if got, want := h.mediaEntries(t), []string{key + "/", key + "/" + name}; !slices.Equal(got, want) {
		t.Errorf("media entries = %q, want %q", got, want)
	}
	h.requireImageQuestion(t, id, 1, name)
	if got, want := h.revisionImages(t, id), map[string]string{name: name}; !reflect.DeepEqual(got, want) {
		t.Errorf("content.images = %q, want %q", got, want)
	}
	if got := h.latestSummary(t, id); got != "Created in the editor" {
		t.Errorf("summary = %q", got)
	}
	requireLiveMatchesRevision(t, h, id)
}

// agedMediaTimes sets every media entry's modification time to one old time,
// so that any later write shows, and returns those times.
func (h *harness) agedMediaTimes(t *testing.T) map[string]time.Time {
	t.Helper()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range h.mediaEntries(t) {
		if err := os.Chtimes(filepath.Join(h.dir, e), old, old); err != nil {
			t.Fatal(err)
		}
	}
	return h.mediaTimes(t)
}

func (h *harness) mediaTimes(t *testing.T) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	for _, e := range h.mediaEntries(t) {
		fi, err := os.Stat(filepath.Join(h.dir, e))
		if err != nil {
			t.Fatal(err)
		}
		out[e] = fi.ModTime()
	}
	return out
}

func TestEdit_TextSaveWritesNoFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 1, withImage(question("First", 0, "a", "b"), pngBytes(t, color.White))))
	mustNil(t, "AddQuestion", h.svc.AddQuestion(ctx, h.owner, id, 2, withImage(question("Third", 0, "x", "y"), pngBytes(t, color.Black))))
	entries, times := h.mediaEntries(t), h.agedMediaTimes(t)

	mustNil(t, "SaveQuestion text", h.svc.SaveQuestion(ctx, h.owner, id, 3, 2, question("Second edited", 1, "c", "d", "e")))
	set := defaultSettings
	set.Title = "Renamed"
	mustNil(t, "SaveSettings", h.svc.SaveSettings(ctx, h.owner, id, 4, set))
	if got := h.mediaEntries(t); !slices.Equal(got, entries) {
		t.Errorf("media entries = %q, want %q", got, entries)
	}
	if got := h.mediaTimes(t); !reflect.DeepEqual(got, times) {
		t.Errorf("media mod times = %v, want %v", got, times)
	}
	requireLiveMatchesRevision(t, h, id)
}

func TestImage_Refusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	text := h.mustImport(t, []byte(editExam))

	a, b := pseudoPNG(4_900_000, 1), pseudoPNG(4_900_000, 2)
	big := h.mustImport(t, buildZip(t,
		zipEntry{"exam.md", []byte("# Big\n\n1. Q ![](a.png) ![](b.png)\n- a *\n- b\n")},
		zipEntry{"a.png", a},
		zipEntry{"b.png", b},
	))

	imgA, imgB := pngBytes(t, color.White), pngBytes(t, color.Black)
	clash := h.mustImport(t, buildZip(t,
		zipEntry{"exam.md", []byte("# Clash\n\n1. Q ![](" + imageName(imgB) + ")\n- a *\n- b\n")},
		zipEntry{imageName(imgB), imgA},
	))

	typeMsg := "image is not a PNG, JPEG, GIF or WebP file"
	cases := []struct {
		name string
		id   int64
		q    exam.QuestionSource
		want string
	}{
		{"text bytes", text, question("First", 0, "a", "b"), typeMsg},
		{"svg", text, question("First", 0, "a", "b"), typeMsg},
		{"over 5 MB", text, question("First", 0, "a", "b"), "image is larger than 5 MB"},
		{"exam over 10 MB", big, question("Q ![](a.png) ![](b.png)", 0, "a", "b"),
			"exam with its images is larger than 10 MB, the largest file that can be imported"},
		{"name clash", clash, question("Q ![]("+imageName(imgB)+")", 0, "a", "b"),
			"this exam already has a different image file named " + imageName(imgB)},
		{"image line hidden", text, question("pic\n\n<div>", 0, "a", "b"), hiddenImageMsg},
	}
	images := [][]byte{
		[]byte("just some text\n"),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`),
		pseudoPNG(examfile.MaxZipEntryBytes+1, 3),
		pseudoPNG(4_900_000, 4),
		imgB,
		pngBytes(t, color.RGBA{9, 8, 7, 255}),
	}
	for i, c := range cases {
		dump, media := h.examDump(t, c.id), h.mediaEntries(t)
		start := time.Now()
		err := h.svc.SaveQuestion(ctx, h.owner, c.id, 1, 1, withImage(c.q, images[i]))
		t.Logf("%s: %v", c.name, time.Since(start))
		var fileErr *examfile.Error
		if !errors.As(err, &fileErr) || fileErr.Msg != c.want {
			t.Errorf("%s: err = %v, want *examfile.Error %q", c.name, err, c.want)
		}
		if got := h.examDump(t, c.id); got != dump {
			t.Errorf("%s: refused save stored something:\n%s", c.name, got)
		}
		if got := h.mediaEntries(t); !slices.Equal(got, media) {
			t.Errorf("%s: media entries = %q, want %q", c.name, got, media)
		}
	}
}

const hiddenImageMsg = "the image line would not show at the end of the text, for example after raw HTML; " +
	"change the end of the text and save again"

func TestCreate_ImageLineHiddenStoresNothing(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Create(context.Background(), h.owner, defaultSettings,
		withImage(question("pic\n\n<div>", 0, "a", "b"), pngBytes(t, color.RGBA{9, 8, 7, 255})))
	var fileErr *examfile.Error
	if !errors.As(err, &fileErr) || fileErr.Msg != hiddenImageMsg {
		t.Errorf("Create: err = %v, want *examfile.Error %q", err, hiddenImageMsg)
	}
	if n := h.examCount(t); n != 0 {
		t.Errorf("exam count = %d, want 0", n)
	}
	if entries := h.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries = %v, want none", entries)
	}
}

func TestImage_FailedSaveRemovesOnlyItsFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	text := h.mustImport(t, []byte(editExam))
	zipData, held := imageExam(t, "Pictures", color.Black)
	pictures := h.mustImport(t, zipData)
	again := h.mustImport(t, zipData)
	if _, err := h.db.Exec(`CREATE TRIGGER fail_revision_2 BEFORE INSERT ON exam_revision WHEN NEW.number = 2
		BEGIN SELECT RAISE(ABORT, 'test: revision 2 refused'); END`); err != nil {
		t.Fatal(err)
	}
	fresh := pngBytes(t, color.RGBA{1, 2, 3, 255})

	cases := []struct {
		name string
		id   int64
		q    exam.QuestionSource
	}{
		{"text exam", text, withImage(question("First", 0, "a", "b"), fresh)},
		{"image exam", pictures, withImage(question("Q ![x](i.png)", 0, "a", "b"), fresh)},
		{"held image", again, withImage(question("Q ![x](i.png)", 0, "a", "b"), held)},
	}
	for _, c := range cases {
		media := h.mediaEntries(t)
		err := h.svc.SaveQuestion(ctx, h.owner, c.id, 1, 1, c.q)
		if err == nil || !strings.Contains(err.Error(), "revision 2 refused") {
			t.Fatalf("%s: err = %v, want the trigger's refusal", c.name, err)
		}
		if got := h.mediaEntries(t); !slices.Equal(got, media) {
			t.Errorf("%s: media entries = %q, want %q", c.name, got, media)
		}
	}
	if got, err := h.media.ReadFile(h.mediaKey(t, again) + "/" + imageName(held)); err != nil || !bytes.Equal(got, held) {
		t.Errorf("held image: %v", err)
	}
}

func TestImage_StaleSaveWritesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})

	if err := h.svc.SaveQuestion(ctx, h.owner, id, 2, 1, withImage(question("First", 0, "a", "b"), img)); !errors.Is(err, exam.ErrStale) {
		t.Errorf("SaveQuestion: err = %v, want ErrStale", err)
	}
	if err := h.svc.AddQuestion(ctx, h.owner, id, 0, withImage(question("Third", 0, "x", "y"), img)); !errors.Is(err, exam.ErrStale) {
		t.Errorf("AddQuestion: err = %v, want ErrStale", err)
	}
	if got := h.mediaEntries(t); len(got) != 0 {
		t.Errorf("media entries = %q, want none", got)
	}
}

func TestImage_ConcurrentSavesKeepFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(editExam))
	key := h.mediaKey(t, id)
	img := pngBytes(t, color.RGBA{1, 2, 3, 255})
	name := imageName(img)

	var (
		wg   sync.WaitGroup
		errs [2]error
	)
	for i := range errs {
		wg.Go(func() {
			errs[i] = h.svc.SaveQuestion(ctx, h.owner, id, 1, 1, withImage(question("First", 0, "a", "b"), img))
		})
	}
	wg.Wait()
	nils, stale := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			nils++
		case errors.Is(err, exam.ErrStale):
			stale++
		}
	}
	if nils != 1 || stale != 1 {
		t.Fatalf("errors = %v, want one nil and one ErrStale", errs)
	}
	if got, err := h.media.ReadFile(key + "/" + name); err != nil || !bytes.Equal(got, img) {
		t.Errorf("image file: %v", err)
	}
	h.requireImageQuestion(t, id, 1, name)
	requireLiveMatchesRevision(t, h, id)
}

func TestEdit_ImageDownloadRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zipData, held := imageExam(t, "Pictures", color.Black)
	id := h.mustImport(t, zipData)
	key := h.mediaKey(t, id)
	rev1 := flatten(h.mustRevision(t, id, 1).Questions)

	mustNil(t, "SaveQuestion", h.svc.SaveQuestion(ctx, h.owner, id, 1, 1,
		withImage(question("Q ![x](i.png)", 0, "a", "b"), pngBytes(t, color.White))))
	mustNil(t, "AddQuestion", h.svc.AddQuestion(ctx, h.owner, id, 2,
		withImage(question("", 1, "x", "y"), pngBytes(t, color.RGBA{1, 2, 3, 255}))))

	f := h.mustDownload(t, h.owner, id)
	if f.ContentType != "application/zip" {
		t.Errorf("ContentType = %q, want application/zip", f.ContentType)
	}
	requireEqualExams(t, h, id, h.mustImport(t, f.Data))
	requireEqualExams(t, h, id, h.mustCopy(t, h.owner, id))
	if got := flatten(h.mustRevision(t, id, 1).Questions); !reflect.DeepEqual(got, rev1) {
		t.Errorf("revision 1 questions =\n%+v\nwant\n%+v", got, rev1)
	}
	if got, err := h.media.ReadFile(key + "/" + imageName(held)); err != nil || !bytes.Equal(got, held) {
		t.Errorf("revision 1 image: %v", err)
	}
}
