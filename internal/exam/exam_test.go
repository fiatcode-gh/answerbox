package exam_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math/rand/v2"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

var fixedNow = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

type harness struct {
	svc   *exam.Service
	db    *sql.DB
	media *os.Root
	dir   string
	clock time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	media, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })
	h := &harness{db: sqlitetest.Open(t), media: media, dir: dir, clock: fixedNow}
	random := rand.NewChaCha8([32]byte{1, 2, 3})
	h.svc = exam.NewService(h.db, media, func() time.Time { return h.clock }, random)
	return h
}

func (h *harness) mustImport(t *testing.T, data []byte) int64 {
	t.Helper()
	id, err := h.svc.Import(context.Background(), data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return id
}

func (h *harness) mustGet(t *testing.T, id int64) exam.Exam {
	t.Helper()
	ex, err := h.svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return ex
}

func (h *harness) mustQuestions(t *testing.T, id int64) []exam.Question {
	t.Helper()
	qs, err := h.svc.Questions(context.Background(), id)
	if err != nil {
		t.Fatalf("Questions(%d): %v", id, err)
	}
	return qs
}

func (h *harness) mediaKey(t *testing.T, id int64) string {
	t.Helper()
	var key string
	if err := h.db.QueryRow("SELECT media_key FROM exam WHERE id = ?", id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// mediaEntries lists the media root as "dir/" and "dir/file" names.
func (h *harness) mediaEntries(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(os.DirFS(h.dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.IsDir() {
			p += "/"
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) insertAttempt(t *testing.T, examID int64, name string) int64 {
	t.Helper()
	res, err := h.db.Exec(`INSERT INTO attempt (exam_id, student_name, token_hash, shuffle_seed, started_at, deadline_at)
		VALUES (?, ?, ?, 1, 0, 3600000)`, examID, name, []byte(name))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

type zipEntry struct {
	name string
	data []byte
}

func buildZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngBytes(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := range 2 {
		for y := range 2 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:32] + ".png"
}

func assertExamfileError(t *testing.T, err error) {
	t.Helper()
	var e *examfile.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %T %v, want *examfile.Error", err, err)
	}
}

type flatOption struct {
	html    string
	correct bool
}

type flatQuestion struct {
	position int
	html     string
	opts     []flatOption
}

func flatten(qs []exam.Question) []flatQuestion {
	var out []flatQuestion
	for _, q := range qs {
		fq := flatQuestion{position: q.Position, html: string(q.BodyHTML)}
		for i, o := range q.Options {
			if o.Position != i+1 {
				fq.html += " BAD-OPTION-POSITION"
			}
			fq.opts = append(fq.opts, flatOption{string(o.BodyHTML), o.Correct})
		}
		out = append(out, fq)
	}
	return out
}

const simpleExam = "# Simple\n\n1. Q one\n- a *\n- b\n"

var hexKey = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestImport_StoresExamAndImages(t *testing.T) {
	h := newHarness(t)
	img := pngBytes(t, color.RGBA{200, 10, 10, 255})
	text := strings.Join([]string{
		"# Geometry",
		"shuffle: none",
		"show_result: review",
		"",
		"1. Look at this ![shape](pics/shape.png)",
		"- circle",
		"- square *",
		"",
		"2. Second?",
		"- yes *",
		"- no",
		"- maybe",
	}, "\n")
	id := h.mustImport(t, buildZip(t,
		zipEntry{"exam.md", []byte(text)},
		zipEntry{"pics/shape.png", img},
	))

	ex := h.mustGet(t, id)
	want := exam.Exam{
		ID: id, Title: "Geometry", Shuffle: examfile.ShuffleNone, ShowResult: examfile.ShowReview,
		Open: false, CreatedAt: fixedNow, QuestionCount: 2, AttemptCount: 0,
	}
	if !ex.CreatedAt.Equal(fixedNow) {
		t.Errorf("CreatedAt = %v, want %v", ex.CreatedAt, fixedNow)
	}
	ex.CreatedAt = fixedNow
	if ex != want {
		t.Errorf("Get = %+v\nwant %+v", ex, want)
	}

	key := h.mediaKey(t, id)
	if !hexKey.MatchString(key) {
		t.Fatalf("media_key = %q, want 32 hex chars", key)
	}
	name := imageName(img)
	qs := h.mustQuestions(t, id)
	got := flatten(qs)
	if len(got) != 2 {
		t.Fatalf("questions = %+v", got)
	}
	src := `src="/media/` + key + "/" + name + `"`
	if !strings.Contains(got[0].html, src) {
		t.Errorf("question 1 html = %q, want it to contain %s", got[0].html, src)
	}
	got[0].html = ""
	wantQs := []flatQuestion{
		{1, "", []flatOption{{"<p>circle</p>\n", false}, {"<p>square</p>\n", true}}},
		{2, "<p>Second?</p>\n", []flatOption{{"<p>yes</p>\n", true}, {"<p>no</p>\n", false}, {"<p>maybe</p>\n", false}}},
	}
	if !reflect.DeepEqual(got, wantQs) {
		t.Errorf("questions =\n%+v\nwant\n%+v", got, wantQs)
	}
	for _, q := range qs {
		if q.ID == 0 {
			t.Errorf("question %d has no ID", q.Position)
		}
		for _, o := range q.Options {
			if o.ID == 0 {
				t.Errorf("option %d of question %d has no ID", o.Position, q.Position)
			}
		}
	}

	if entries := h.mediaEntries(t); !slices.Equal(entries, []string{key + "/", key + "/" + name}) {
		t.Errorf("media entries = %v", entries)
	}
	served, err := fs.ReadFile(h.svc.MediaFS(), key+"/"+name)
	if err != nil {
		t.Fatalf("MediaFS read: %v", err)
	}
	if !bytes.Equal(served, img) {
		t.Error("MediaFS returned different bytes")
	}
}

func TestImport_FailureStoresNothing(t *testing.T) {
	h := newHarness(t)
	cases := map[string][]byte{
		"invalid text":  []byte("# T\n\n1. Only one option\n- a *\n"),
		"missing image": buildZip(t, zipEntry{"exam.md", []byte("# T\n\n1. Q ![x](nope.png)\n- a *\n- b\n")}),
		"oversize zip": buildZip(t,
			zipEntry{"exam.md", []byte(simpleExam)},
			zipEntry{"big.bin", make([]byte, examfile.MaxZipEntryBytes+1)},
		),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.svc.Import(context.Background(), data)
			assertExamfileError(t, err)
		})
	}
	list, err := h.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("List = %+v, want empty", list)
	}
	if entries := h.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries = %v, want none", entries)
	}
}

func TestImport_HostileZipWritesOnlyUnderMediaKey(t *testing.T) {
	h := newHarness(t)
	img := pngBytes(t, color.RGBA{0, 0, 255, 255})
	id := h.mustImport(t, buildZip(t,
		zipEntry{"exam.md", []byte("# T\n\n1. Q ![x](sub/../img.png)\n- a *\n- b\n")},
		zipEntry{"img.png", img},
	))
	key := h.mediaKey(t, id)
	entries := h.mediaEntries(t)
	if !slices.Equal(entries, []string{key + "/", key + "/" + imageName(img)}) {
		t.Errorf("media entries = %v", entries)
	}
	if !hexKey.MatchString(key) {
		t.Errorf("media key %q is not 32 hex chars", key)
	}

	h2 := newHarness(t)
	_, err := h2.svc.Import(context.Background(), buildZip(t,
		zipEntry{"exam.md", []byte("# T\n\n1. Q ![x](../evil.png)\n- a *\n- b\n")},
		zipEntry{"../evil.png", img},
	))
	assertExamfileError(t, err)
	if entries := h2.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries after hostile zip = %v, want none", entries)
	}
}

func imageExam(t *testing.T, title string, c color.Color) ([]byte, []byte) {
	t.Helper()
	img := pngBytes(t, c)
	text := "# " + title + "\nshow_result: none\n\n1. Q ![x](i.png)\n- a *\n- b\n"
	return buildZip(t, zipEntry{"exam.md", []byte(text)}, zipEntry{"i.png", img}), img
}

func TestReplace_BeforeAttempts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	oldZip, _ := imageExam(t, "Old", color.Black)
	id := h.mustImport(t, oldZip)
	if err := h.svc.Open(ctx, id); err != nil {
		t.Fatal(err)
	}
	oldKey := h.mediaKey(t, id)
	h.clock = fixedNow.Add(time.Hour)

	newImg := pngBytes(t, color.White)
	newZip := buildZip(t,
		zipEntry{"exam.md", []byte("# New\nshuffle: questions, options\nshow_result: review\n\n1. Fresh ![x](i.png)\n- x\n- y *\n- z\n\n2. Second\n- p *\n- q\n")},
		zipEntry{"i.png", newImg},
	)
	if err := h.svc.Replace(ctx, id, newZip); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	ex := h.mustGet(t, id)
	if ex.ID != id || ex.Title != "New" || ex.Shuffle != examfile.ShuffleQuestionsOptions ||
		ex.ShowResult != examfile.ShowReview || !ex.Open || !ex.CreatedAt.Equal(fixedNow) || ex.QuestionCount != 2 {
		t.Errorf("after Replace: %+v", ex)
	}
	newKey := h.mediaKey(t, id)
	if newKey == oldKey {
		t.Fatal("media key unchanged")
	}
	qs := flatten(h.mustQuestions(t, id))
	if len(qs) != 2 || !strings.Contains(qs[0].html, "/media/"+newKey+"/"+imageName(newImg)) ||
		!reflect.DeepEqual(qs[0].opts, []flatOption{{"<p>x</p>\n", false}, {"<p>y</p>\n", true}, {"<p>z</p>\n", false}}) {
		t.Errorf("questions after Replace = %+v", qs)
	}
	if entries := h.mediaEntries(t); !slices.Equal(entries, []string{newKey + "/", newKey + "/" + imageName(newImg)}) {
		t.Errorf("media entries = %v, want only the new folder", entries)
	}
}

func TestReplace_FrozenOnceAttemptExists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	oldZip, _ := imageExam(t, "Old", color.Black)
	id := h.mustImport(t, oldZip)
	before := h.mustQuestions(t, id)
	entriesBefore := h.mediaEntries(t)
	h.insertAttempt(t, id, "alice")

	newZip, _ := imageExam(t, "New", color.White)
	if err := h.svc.Replace(ctx, id, newZip); !errors.Is(err, exam.ErrFrozen) {
		t.Fatalf("Replace = %v, want ErrFrozen", err)
	}
	if after := h.mustQuestions(t, id); !reflect.DeepEqual(after, before) {
		t.Errorf("questions changed:\n%+v\nwant\n%+v", after, before)
	}
	if ex := h.mustGet(t, id); ex.Title != "Old" || ex.AttemptCount != 1 {
		t.Errorf("exam after frozen Replace = %+v", ex)
	}
	if entries := h.mediaEntries(t); !slices.Equal(entries, entriesBefore) {
		t.Errorf("media entries = %v, want %v", entries, entriesBefore)
	}
}

func TestReplace_AllowedAgainAfterAttemptsDeleted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	attemptID := h.insertAttempt(t, id, "bob")
	if err := h.svc.Replace(ctx, id, []byte("# Second\n\n1. Q\n- a\n- b *\n")); !errors.Is(err, exam.ErrFrozen) {
		t.Fatalf("Replace = %v, want ErrFrozen", err)
	}
	if _, err := h.db.Exec("DELETE FROM attempt WHERE id = ?", attemptID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Replace(ctx, id, []byte("# Second\n\n1. Q\n- a\n- b *\n")); err != nil {
		t.Fatalf("Replace after delete: %v", err)
	}
	if ex := h.mustGet(t, id); ex.Title != "Second" {
		t.Errorf("Title = %q", ex.Title)
	}
}

func TestReplace_InvalidKeepsOldContent(t *testing.T) {
	h := newHarness(t)
	oldZip, _ := imageExam(t, "Old", color.Black)
	id := h.mustImport(t, oldZip)
	before := h.mustQuestions(t, id)
	entriesBefore := h.mediaEntries(t)

	err := h.svc.Replace(context.Background(), id, []byte("# Broken\n\n1. No options\n"))
	assertExamfileError(t, err)
	if after := h.mustQuestions(t, id); !reflect.DeepEqual(after, before) {
		t.Errorf("questions changed:\n%+v\nwant\n%+v", after, before)
	}
	if ex := h.mustGet(t, id); ex.Title != "Old" {
		t.Errorf("Title = %q", ex.Title)
	}
	if entries := h.mediaEntries(t); !slices.Equal(entries, entriesBefore) {
		t.Errorf("media entries = %v, want %v", entries, entriesBefore)
	}
}

func TestOpen_ClosesOtherExam(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.mustImport(t, []byte(simpleExam))
	b := h.mustImport(t, []byte(simpleExam))
	if err := h.svc.Open(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Open(ctx, a); err != nil {
		t.Fatalf("re-opening open exam: %v", err)
	}
	if err := h.svc.Open(ctx, b); err != nil {
		t.Fatal(err)
	}
	if h.mustGet(t, a).Open || !h.mustGet(t, b).Open {
		t.Errorf("open flags: a=%v b=%v, want only b", h.mustGet(t, a).Open, h.mustGet(t, b).Open)
	}
	cur, ok, err := h.svc.Current(ctx)
	if err != nil || !ok || cur.ID != b {
		t.Errorf("Current = %+v, %v, %v; want exam %d", cur, ok, err, b)
	}
}

func TestClose(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.mustImport(t, []byte(simpleExam))
	h.insertAttempt(t, id, "carol")
	if err := h.svc.Open(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Close(ctx, id); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok, err := h.svc.Current(ctx); err != nil || ok {
		t.Errorf("Current after Close: ok=%v err=%v", ok, err)
	}
	if ex := h.mustGet(t, id); ex.Open || ex.AttemptCount != 1 {
		t.Errorf("after Close: %+v", ex)
	}

	const unknown = 9999
	if err := h.svc.Open(ctx, unknown); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Open(unknown) = %v", err)
	}
	if err := h.svc.Close(ctx, unknown); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Close(unknown) = %v", err)
	}
	if _, err := h.svc.Get(ctx, unknown); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Get(unknown) = %v", err)
	}
	if err := h.svc.Replace(ctx, unknown, []byte(simpleExam)); !errors.Is(err, exam.ErrNotFound) {
		t.Errorf("Replace(unknown) = %v", err)
	}
	if entries := h.mediaEntries(t); len(entries) != 0 {
		t.Errorf("media entries = %v, want none", entries)
	}
}

func TestList_NewestFirstWithCounts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first := h.mustImport(t, []byte("# First\n\n1. A\n- a *\n- b\n\n2. B\n- a *\n- b\n"))
	h.clock = fixedNow.Add(time.Minute)
	second := h.mustImport(t, []byte(simpleExam))
	third := h.mustImport(t, []byte(simpleExam)) // same created_at: id breaks the tie
	h.insertAttempt(t, first, "a1")
	h.insertAttempt(t, first, "a2")
	h.insertAttempt(t, third, "a3")

	list, err := h.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id             int64
		qCount, aCount int
	}
	var got []row
	for _, e := range list {
		got = append(got, row{e.ID, e.QuestionCount, e.AttemptCount})
	}
	want := []row{{third, 1, 1}, {second, 1, 0}, {first, 2, 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}
}
