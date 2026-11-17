package examfile

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// rebuildTexts are exam texts whose stored HTML must rebuild. They also seed
// FuzzRebuild.
var rebuildTexts = map[string]string{
	"features": lines(
		"# Features",
		"shuffle: questions",
		"show_result: review",
		"monitor: off",
		"duration: 90",
		"1. Plain text",
		"- a *",
		"- b",
		"2. One paragraph",
		"",
		"Another paragraph",
		"- **bold** option *",
		"- *italic* option",
		"3. Line one",
		"line two",
		"- `code` option",
		"- two *",
		"  lines",
		"4. Some **bold** and *italic* text",
		"- a",
		"- b *",
		"5. *An **important** word*",
		"- a *",
		"- b",
		"6. Run `go test` now",
		"- a *",
		"- b",
		"7. Type `` ` `` or ``a`b`` or `` `x` ``",
		"- a *",
		"- b",
		"8. Code:",
		"```",
		"x := 1 & y < 2",
		"```",
		"- a *",
		"- b",
		"9. Go:",
		"```go",
		`fmt.Println("hi")`,
		"```",
		"- a *",
		"- b",
		"10. See",
		"```",
		"- x",
		"2. y",
		"s := `raw` + ``two``",
		"```",
		"- a *",
		"- b",
		"11. Before",
		"```",
		"code",
		"```",
		"After",
		"- a *",
		"- b",
	),
	"plain": lines(
		"# Plain",
		"1. Berapakah hasil dari 1.003 + 2,5?",
		"- 1.005,5 *",
		"- 100%",
		"- A. Rp9.000",
		"- B) Rp1.000",
		"2. Harga Rp10.000,00 (sepuluh ribu) — diskon 10%!",
		"- -5 *",
		"- = sama",
		`- x < y > z && "q"`,
	),
	"escapes": lines(
		"# Escapes",
		`1. a\*b\*c`,
		`- 5 \*`,
		`- \* *`,
		"2. Para",
		"    12. dua belas",
		"- a *",
		"- b",
		"3. Para",
		`\- strip`,
		`- 1\. satu *`,
		`- \+ plus`,
		"4. Entity &amp;copy; and \\&lt;b>",
		"- a *",
		"- b",
	),
}

// stored is what the database holds of ex: everything but the Sources, the
// folder and the image paths.
func stored(ex Exam) Exam {
	out := ex
	out.Dir, out.ImagePaths = "", nil
	out.Questions = make([]Question, len(ex.Questions))
	for k, q := range ex.Questions {
		out.Questions[k] = Question{BodyHTML: q.BodyHTML, Options: make([]Option, len(q.Options))}
		for m, o := range q.Options {
			out.Questions[k].Options[m] = Option{BodyHTML: o.BodyHTML, Correct: o.Correct}
		}
	}
	return out
}

func imageFiles(images []Image) map[string][]byte {
	files := map[string][]byte{}
	for _, img := range images {
		files[img.Name] = img.Data
	}
	return files
}

// sameRendering reports how got differs from want in settings, structure,
// HTML or correct flags, or "" when it does not.
func sameRendering(want, got Exam) string {
	if want.Title != got.Title || want.Shuffle != got.Shuffle || want.ShowResult != got.ShowResult ||
		want.Monitor != got.Monitor || want.DurationMinutes != got.DurationMinutes {
		return fmt.Sprintf("settings %+v, want %+v", got, want)
	}
	if len(got.Questions) != len(want.Questions) {
		return fmt.Sprintf("%d questions, want %d", len(got.Questions), len(want.Questions))
	}
	for k, q := range want.Questions {
		g := got.Questions[k]
		if g.BodyHTML != q.BodyHTML || len(g.Options) != len(q.Options) {
			return fmt.Sprintf("question %d = %q with %d options, want %q with %d", k+1, g.BodyHTML, len(g.Options), q.BodyHTML, len(q.Options))
		}
		for m, o := range q.Options {
			if g.Options[m].BodyHTML != o.BodyHTML || g.Options[m].Correct != o.Correct {
				return fmt.Sprintf("question %d option %d = %q %v, want %q %v", k+1, m+1, g.Options[m].BodyHTML, g.Options[m].Correct, o.BodyHTML, o.Correct)
			}
		}
	}
	return ""
}

// mustRebuild rebuilds the stored form of ex and checks that the formatted
// result parses back to ex's HTML and correct options.
func mustRebuild(t *testing.T, ex Exam) Exam {
	t.Helper()
	rebuilt, err := Rebuild(stored(ex), "/media/k")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	out := Format(rebuilt)
	again, err := Parse(Upload{Text: out, Dir: ".", Files: imageFiles(ex.Images)}, "/media/k")
	if err != nil {
		t.Fatalf("Parse(Format(rebuilt)): %v\nformatted:\n%s", err, out)
	}
	if diff := sameRendering(ex, again); diff != "" {
		t.Fatalf("Parse(Format(rebuilt)): %s\nformatted:\n%s", diff, out)
	}
	return rebuilt
}

func TestRebuild_Features(t *testing.T) {
	ex := mustParse(t, textUpload(rebuildTexts["features"]))
	rebuilt := mustRebuild(t, ex)
	if rebuilt.Dir != "." {
		t.Errorf("Dir = %q", rebuilt.Dir)
	}
	if len(rebuilt.Images) != 0 || rebuilt.ImagePaths != nil {
		t.Errorf("Images = %v, ImagePaths = %v", rebuilt.Images, rebuilt.ImagePaths)
	}
	for k, want := range map[int]string{
		9:  "Go:\n\n```go\nfmt.Println(\"hi\")\n```",
		10: "See\n\n```\n- x\n2. y\ns := `raw` + ``two``\n```",
		11: "Before\n\n```\ncode\n```\n\nAfter",
	} {
		if got := rebuilt.Questions[k-1].Source; got != want {
			t.Errorf("question %d Source = %q, want %q", k, got, want)
		}
	}
}

func TestRebuild_PlainTextStaysPlain(t *testing.T) {
	ex := mustParse(t, textUpload(rebuildTexts["plain"]))
	rebuilt := mustRebuild(t, ex)
	for k, q := range ex.Questions {
		if got := rebuilt.Questions[k].Source; got != q.Source || strings.Contains(got, `\`) {
			t.Errorf("question %d Source = %q, want %q", k+1, got, q.Source)
		}
		for m, o := range q.Options {
			if got := rebuilt.Questions[k].Options[m].Source; got != o.Source || strings.Contains(got, `\`) {
				t.Errorf("question %d option %d Source = %q, want %q", k+1, m+1, got, o.Source)
			}
		}
	}
}

func TestRebuild_EscapesOnlyWhenNeeded(t *testing.T) {
	ex := mustParse(t, textUpload(rebuildTexts["escapes"]))
	rebuilt := mustRebuild(t, ex)
	src := func(q, o int) string {
		if o == 0 {
			return rebuilt.Questions[q-1].Source
		}
		return rebuilt.Questions[q-1].Options[o-1].Source
	}
	if got := src(1, 0); !strings.Contains(got, `\*`) {
		t.Errorf("question 1 Source = %q, want an escaped *", got)
	}
	if got := src(1, 1); got != `5 \*` {
		t.Errorf("wrong option Source = %q, want %q", got, `5 \*`)
	}
	if out := string(Format(rebuilt)); !strings.Contains(out, "\n- \\* *\n") {
		t.Errorf("Format =\n%s\nwant a line `- \\* *`", out)
	}
	if got := src(2, 0); got != "Para\n12. dua belas" {
		t.Errorf("question 2 Source = %q", got)
	}
	if got := src(3, 1); got != `1\. satu` {
		t.Errorf("question 3 option 1 Source = %q", got)
	}
	if got := src(3, 2); got != `\+ plus` {
		t.Errorf("question 3 option 2 Source = %q", got)
	}
}

// A block that starts with a code block gets an empty first line, so that
// Format does not put the opening fence on the question or option line.
func TestRebuild_LeadingCodeBlock(t *testing.T) {
	ex := mustParse(t, textUpload(lines(
		"# T",
		"1.",
		"```",
		"code",
		"```",
		"What?",
		"- a",
		"-",
		"  ```",
		"  x",
		"  ```",
		"- b *",
	)))
	rebuilt := mustRebuild(t, ex)
	if got, want := rebuilt.Questions[0].Source, "\n```\ncode\n```\n\nWhat?"; got != want {
		t.Errorf("question Source = %q, want %q", got, want)
	}
	if got, want := rebuilt.Questions[0].Options[1].Source, "\n```\nx\n```"; got != want {
		t.Errorf("option Source = %q, want %q", got, want)
	}
}

func TestRebuild_Images(t *testing.T) {
	a := pngBytes(t, colorRGB(255, 0, 0))
	b := pngBytes(t, colorRGB(0, 255, 0))
	unused := pngBytes(t, colorRGB(0, 0, 255))
	an, bn := imageName(a, ".png"), imageName(b, ".png")
	ex := mustParse(t, Upload{Dir: "exam", Files: map[string][]byte{
		"exam/img/a.png": a,
		"b.png":          b,
	}, Text: []byte(lines(
		"# T",
		`1. Look ![a [b] "c" \*d\*](img/a.png) here`,
		"- ![](../b.png) *",
		"- ![again](img/a.png)",
	))})
	full := ex
	full.Images = append(append([]Image{}, ex.Images...), Image{Name: imageName(unused, ".png"), Data: unused})

	rebuilt, err := Rebuild(stored(full), "/media/k")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if rebuilt.Dir != "." {
		t.Errorf("Dir = %q", rebuilt.Dir)
	}
	if want := map[string]string{an: an, bn: bn}; !reflect.DeepEqual(rebuilt.ImagePaths, want) {
		t.Errorf("ImagePaths = %v, want %v", rebuilt.ImagePaths, want)
	}
	if !reflect.DeepEqual(rebuilt.Images, ex.Images) {
		t.Errorf("Images = %v, want %v", rebuilt.Images, ex.Images)
	}
	mustRebuild(t, ex)

	missing := stored(ex)
	missing.Images = ex.Images[:1]
	got, err := Rebuild(missing, "/media/k")
	var re *RebuildError
	if !errors.As(err, &re) || re.Question != 1 || re.Option != 1 {
		t.Fatalf("Rebuild without %s: %v, want a *RebuildError for question 1, option 1", bn, err)
	}
	if !reflect.DeepEqual(got, Exam{}) {
		t.Errorf("Rebuild returned %+v with its error", got)
	}
}

func TestRebuild_Unsupported(t *testing.T) {
	cases := []struct {
		name             string
		text             string
		question, option int
	}{
		{"bullet list", "# T\n1. Q\n\n* item\n- a *\n- b", 1, 0},
		{"link", "# T\n1. Q\n- a *\n- b\n2. Q\n- c *\n- [secret](http://example.com)", 2, 2},
		{"autolink", "# T\n1. Q\n- a *\n- <http://example.com/secret>", 1, 2},
		{"quote", "# T\n1. Q\n- a *\n- b\n2. > secret\n- c *\n- d", 2, 0},
		{"raw HTML", "# T\n1. Q\n- <b>secret</b> *\n- b", 1, 1},
		{"heading", "# T\n1. Q\n\n## secret\n- a *\n- b", 1, 0},
	}
	check := func(t *testing.T, ex Exam, question, option int) {
		t.Helper()
		got, err := Rebuild(ex, "/media/k")
		var re *RebuildError
		if !errors.As(err, &re) {
			t.Fatalf("Rebuild error = %v, want a *RebuildError", err)
		}
		if re.Question != question || re.Option != option {
			t.Errorf("RebuildError names question %d option %d, want %d %d", re.Question, re.Option, question, option)
		}
		if strings.Contains(err.Error(), "secret") || re.Reason == "" {
			t.Errorf("Error() = %q", err.Error())
		}
		if !reflect.DeepEqual(got, Exam{}) {
			t.Errorf("Rebuild returned %+v with its error", got)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			check(t, stored(mustParse(t, textUpload(c.text))), c.question, c.option)
		})
	}
	t.Run("hand-written q1", func(t *testing.T) {
		check(t, Exam{
			Title: "T", Shuffle: ShuffleNone, ShowResult: ShowNone, Monitor: true, DurationMinutes: 60,
			Questions: []Question{{BodyHTML: "q1", Options: []Option{
				{BodyHTML: "<p>a</p>\n", Correct: true},
				{BodyHTML: "<p>b</p>\n"},
			}}},
		}, 1, 0)
	})
}

// olympiadText is shaped like the load test's exam (cmd/loadtest/main.go).
func olympiadText(questions, options int) string {
	var b strings.Builder
	b.WriteString("# Olimpiade 2026-11-17T08:00:00+07:00\nshuffle: questions, options\nshow_result: score\n\n")
	for q := 1; q <= questions; q++ {
		fmt.Fprintf(&b, "%d. Question %d\n", q, q)
		for k := 1; k <= options; k++ {
			mark := ""
			if k-1 == (q-1)%options {
				mark = " *"
			}
			fmt.Fprintf(&b, "- Q%d option %d%s\n", q, k, mark)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestRebuild_OlympiadShape(t *testing.T) {
	ex := mustParse(t, textUpload(olympiadText(20, 4)))
	rebuilt := mustRebuild(t, ex)
	if !reflect.DeepEqual(rebuilt, ex) {
		t.Errorf("rebuilt exam differs from the parsed one\nrebuilt: %#v\nparsed:  %#v", rebuilt, ex)
	}
}

func FuzzRebuild(f *testing.F) {
	for _, text := range rebuildTexts {
		f.Add([]byte(text))
	}
	for _, text := range roundTripTexts {
		f.Add([]byte(text))
	}
	f.Add([]byte(olympiadText(3, 4)))
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := ReadUpload(data)
		if err != nil || len(u.Files) > 0 || u.Dir != "." {
			return
		}
		ex, err := Parse(u, "/media/k")
		if err != nil {
			return
		}
		rebuilt, err := Rebuild(stored(ex), "/media/k")
		if err != nil {
			var re *RebuildError
			if !errors.As(err, &re) {
				t.Fatalf("error %T %v is not a *RebuildError", err, err)
			}
			return
		}
		out := Format(rebuilt)
		again, err := Parse(Upload{Text: out, Dir: ".", Files: map[string][]byte{}}, "/media/k")
		if err != nil {
			t.Fatalf("Parse(Format(rebuilt)): %v\nformatted:\n%q", err, out)
		}
		if diff := sameRendering(ex, again); diff != "" {
			t.Fatalf("Parse(Format(rebuilt)): %s\nformatted:\n%q", diff, out)
		}
	})
}
