package examfile

import (
	"archive/zip"
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParse_Sources(t *testing.T) {
	ex := mustParse(t, textUpload(lines(
		"# T",
		"1. First line",
		"second line",
		"```",
		"code",
		"- not an option",
		"```",
		"- a *",
		`- \*`,
		"",
		"2. Second",
		"- **bold***",
		"  continued",
		"    indented more",
		"- x",
		"",
		"3.",
		"Body below",
		"- *",
		"  only continuation",
		"- y",
	)))

	type src struct {
		body    string
		options []string
	}
	want := []src{
		{"First line\nsecond line\n```\ncode\n- not an option\n```", []string{"a", `\*`}},
		{"Second", []string{"**bold**\ncontinued\n  indented more", "x"}},
		{"\nBody below", []string{"\nonly continuation", "y"}},
	}
	var got []src
	for _, q := range ex.Questions {
		s := src{body: q.Source}
		for _, o := range q.Options {
			s.options = append(s.options, o.Source)
		}
		got = append(got, s)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sources =\n%q\nwant\n%q", got, want)
	}
}

func TestParse_ImagePaths(t *testing.T) {
	a := pngBytes(t, colorRGB(255, 0, 0))
	b := pngBytes(t, colorRGB(0, 255, 0))
	jpg, gf, webp := jpegBytes(t), gifBytes(t), webpBytes()
	an, bn := imageName(a, ".png"), imageName(b, ".png")

	t.Run("body, option, dedupe, percent", func(t *testing.T) {
		ex := mustParse(t, Upload{Dir: ".", Files: map[string][]byte{
			"img/a.png":   a,
			"img/b c.png": b,
			"unused.png":  pngBytes(t, colorRGB(0, 0, 255)),
			"p.jpg":       jpg,
			"p.gif":       gf,
			"p.webp":      webp,
		}, Text: []byte(lines(
			"# T",
			"1. Look ![a](img/a.png)",
			"- ![b](img/b%20c.png) *",
			"- ![again](img/a.png)",
			"2. Types ![j](p.jpg) ![g](p.gif) ![w](p.webp)",
			"- x *",
			"- ![a](./img/../img/a.png)",
		))})
		want := map[string]string{
			"img/a.png":   an,
			"img/b c.png": bn,
			"p.jpg":       imageName(jpg, ".jpg"),
			"p.gif":       imageName(gf, ".gif"),
			"p.webp":      imageName(webp, ".webp"),
		}
		if !reflect.DeepEqual(ex.ImagePaths, want) {
			t.Errorf("ImagePaths = %v, want %v", ex.ImagePaths, want)
		}
		if ex.Dir != "." {
			t.Errorf("Dir = %q", ex.Dir)
		}
	})

	t.Run("relative to exam folder", func(t *testing.T) {
		ex := mustParse(t, Upload{Dir: "exam", Files: map[string][]byte{
			"exam/img/a.png": a,
			"b.png":          b,
		}, Text: []byte("# T\n1. ![a](img/a.png)\n- ![b](../b.png) *\n- c")})
		want := map[string]string{"exam/img/a.png": an, "b.png": bn}
		if !reflect.DeepEqual(ex.ImagePaths, want) {
			t.Errorf("ImagePaths = %v, want %v", ex.ImagePaths, want)
		}
		if ex.Dir != "exam" {
			t.Errorf("Dir = %q", ex.Dir)
		}
	})

	t.Run("same bytes under two names", func(t *testing.T) {
		ex := mustParse(t, Upload{Dir: ".", Files: map[string][]byte{
			"one.png": a,
			"two.png": a,
		}, Text: []byte("# T\n1. ![1](one.png)\n- ![2](two.png) *\n- c")})
		want := map[string]string{"one.png": an, "two.png": an}
		if !reflect.DeepEqual(ex.ImagePaths, want) {
			t.Errorf("ImagePaths = %v, want %v", ex.ImagePaths, want)
		}
		if len(ex.Images) != 1 || ex.Images[0].Name != an {
			t.Errorf("Images = %+v", ex.Images)
		}
	})

	t.Run("plain text", func(t *testing.T) {
		ex := mustParse(t, textUpload("# T\n1. Q\n- a *\n- b"))
		if ex.ImagePaths != nil {
			t.Errorf("ImagePaths = %v, want nil", ex.ImagePaths)
		}
		if ex.Dir != "." {
			t.Errorf("Dir = %q", ex.Dir)
		}
	})
}

// roundTripTexts are exam texts whose Format output must parse back to the
// same exam. They also seed FuzzFormatRoundTrip.
var roundTripTexts = map[string]string{
	"readme": lines(
		"# Chapter 3 quiz",
		"shuffle: questions, options",
		"show_result: review",
		"",
		"1. What is the **area** of this triangle?",
		"![A right triangle with legs 3 and 4](triangle.png)",
		"- 6 *",
		"- 7",
		"- 12",
		"- 14",
		"",
		"2. What does this program print?",
		"```",
		"fmt.Println(2 + 3)",
		"```",
		"- `5` *",
		"- `23`",
		"- `2 + 3`",
	),
	"no headers": "# T\n1. Q\n- a *\n- b",
	"headers none off": lines(
		"# T",
		"shuffle: none",
		"show_result: none",
		"monitor: off",
		"duration: 45",
		"1. Q",
		"- a *",
		"- b",
	),
	"headers questions score on": lines(
		"# T",
		"duration: 5",
		"monitor: on",
		"show_result: score",
		"shuffle: questions",
		"1. Q",
		"- a",
		"- b *",
	),
	"headers questions options": "# T\nshuffle: Questions ,Options\nduration: 240\n1. Q\n- a *\n- b",
	"paren numbering and trailing spaces": lines(
		"# T",
		"1) First line   ",
		"second line",
		"- a *",
		"- b",
		"2)   Q2",
		"- c",
		"- d *",
	),
	"empty first line": "# T\n1.\nText below\n- a *\n- b",
	"indented question-like line": lines(
		"# T",
		"1. Q",
		"   12. not a question",
		"   more",
		"",
		"   2) also not",
		"- a *",
		"- b",
	),
	"question-like line in fence": lines(
		"# T",
		"1. Q",
		"```",
		"12. not a question",
		"- x",
		"2. y",
		"```",
		"- a *",
		"- b",
	),
	"question-like line in and out of fence": lines(
		"# T",
		"1. Q",
		"  ```",
		"  12. in fence",
		"  ```",
		"  3. out of fence",
		"- a *",
		"- b",
	),
	"option markers": lines(
		"# T",
		`1. Q`,
		`- a\ *`,
		`- *italic*`,
		`- \*`,
		"2. Q",
		"- **bold***",
		"- c",
		"3. Q",
		"- *",
		"  below the marker",
		"-",
		"  below the dash",
		"4. Q",
		"- first paragraph",
		"",
		"  second paragraph",
		"- d *",
	),
	"indented code block": lines(
		"# T",
		"1. Look at this:",
		"",
		"        x := 1",
		"        y := 2",
		"",
		"    after",
		"- a *",
		"- b",
	),
	"tabs and blank lines": "# T\r\n\r\n1. Q\r\n\tindented\r\n\r\n\t\tmore\r\n- a\t*\r\n- b\r\n\r\n",
	"sections with instructions": lines(
		"# T",
		"shuffle: none",
		"## Part A",
		"Read **carefully**.",
		"",
		"- one",
		"- two",
		"```",
		"## in a fence",
		"1. in a fence",
		"```",
		"1. Q1",
		"- a *",
		"- b",
		"## Part B: Essays",
		"Text.",
		"2. Q2",
		"- c *",
		"- d",
		"3. Q3",
		"- e",
		"- f *",
	),
	"sections without instructions": "# T\n## A\n1. Q\n- a *\n- b\n##\tB  \n\n2. Q\n- c *\n- d",
	"indented numbered instructions": lines(
		"# T",
		"## A",
		"  1. first",
		"  2. second",
		"1. Q",
		"- a *",
		"- b",
	),
	"indented heading-like line in question": lines(
		"# T",
		"1. Q",
		"    ## not a section",
		"- a *",
		"- b",
	),
	"instructions starting with indented code": lines(
		"# T",
		"## A",
		"",
		"    code",
		"",
		"text",
		"1. Q",
		"- a *",
		"- b",
	),
	"option-like lines in a later section's instructions": lines(
		"# T",
		"## A",
		"1. Q",
		"- a *",
		"- b",
		"## B",
		"- x",
		"- y *",
		"2. R",
		"- c *",
		"- d",
	),
}

// roundTripFiles are the upload files and folder used with a round-trip text.
var roundTripFiles = map[string]func(t *testing.T) (dir string, files map[string][]byte){
	"readme": func(t *testing.T) (string, map[string][]byte) {
		return ".", map[string][]byte{"triangle.png": pngBytes(t, colorRGB(1, 2, 3))}
	},
}

func roundTrip(t *testing.T, u Upload) {
	t.Helper()
	ex := mustParse(t, u)
	out := Format(ex)
	ex2, err := Parse(Upload{Text: out, Dir: u.Dir, Files: u.Files}, "/media/k")
	if err != nil {
		t.Fatalf("Parse(Format(ex)): %v\nformatted:\n%s", err, out)
	}
	if !reflect.DeepEqual(ex, ex2) {
		t.Fatalf("round trip differs\nbefore: %#v\nafter:  %#v\nformatted:\n%s", ex, ex2, out)
	}
}

func TestFormat_RoundTrip(t *testing.T) {
	for name, text := range roundTripTexts {
		t.Run(name, func(t *testing.T) {
			u := textUpload(text)
			if files, ok := roundTripFiles[name]; ok {
				u.Dir, u.Files = files(t)
			}
			roundTrip(t, u)
		})
	}

	t.Run("images in a sub-folder", func(t *testing.T) {
		a := pngBytes(t, colorRGB(255, 0, 0))
		b := pngBytes(t, colorRGB(0, 255, 0))
		roundTrip(t, Upload{Dir: "exam/v2", Files: map[string][]byte{
			"exam/v2/img/a.png": a,
			"exam/b c.png":      b,
			"exam/v2/same.png":  a,
		}, Text: []byte(lines(
			"# T",
			"1. ![a](img/a.png)",
			"- ![b](../b%20c.png) *",
			"- ![same](./same.png)",
		))})
	})

	t.Run("fence lines stay at column 0", func(t *testing.T) {
		out := string(Format(mustParse(t, textUpload(roundTripTexts["question-like line in fence"]))))
		want := "1. Q\n```\n12. not a question\n- x\n2. y\n```\n"
		if !strings.Contains(out, want) {
			t.Errorf("Format =\n%s\nwant it to contain\n%s", out, want)
		}
	})

	t.Run("question-like line is indented", func(t *testing.T) {
		out := string(Format(mustParse(t, textUpload(roundTripTexts["indented question-like line"]))))
		want := "1. Q\n    12. not a question\n    more\n\n    2) also not\n"
		if !strings.Contains(out, want) {
			t.Errorf("Format =\n%s\nwant it to contain\n%s", out, want)
		}
	})

	t.Run("numbered instructions are indented", func(t *testing.T) {
		out := string(Format(mustParse(t, textUpload(roundTripTexts["indented numbered instructions"]))))
		want := "\n## A\n    1. first\n    2. second\n\n1. Q\n"
		if !strings.Contains(out, want) {
			t.Errorf("Format =\n%s\nwant it to contain\n%s", out, want)
		}
	})
}

func TestFormat_Layout(t *testing.T) {
	ex := Exam{
		Title:           "Quiz",
		Shuffle:         ShuffleQuestionsOptions,
		ShowResult:      ShowReview,
		Monitor:         false,
		DurationMinutes: 30,
		Questions: []Question{
			{Source: "Two lines\nhere", Options: []Option{
				{Source: "a"},
				{Source: "b\nmore", Correct: true},
				{Source: ""},
			}},
			{Source: "", Options: []Option{
				{Source: "", Correct: true},
				{Source: "c"},
			}},
		},
	}
	want := lines(
		"# Quiz",
		"shuffle: questions, options",
		"show_result: review",
		"monitor: off",
		"duration: 30",
		"",
		"1. Two lines",
		"here",
		"- a",
		"- b *",
		"more",
		"-",
		"",
		"2.",
		"- *",
		"- c",
		"",
	)
	if got := string(Format(ex)); got != want {
		t.Errorf("Format =\n%q\nwant\n%q", got, want)
	}

	ex.Shuffle, ex.ShowResult, ex.Monitor = ShuffleNone, ShowScore, true
	head := "# Quiz\nshuffle: none\nshow_result: score\nmonitor: on\nduration: 30\n\n"
	if got := string(Format(ex)); !strings.HasPrefix(got, head) {
		t.Errorf("Format =\n%q\nwant prefix\n%q", got, head)
	}
	ex.Shuffle = ShuffleQuestions
	if got := string(Format(ex)); !strings.HasPrefix(got, "# Quiz\nshuffle: questions\n") {
		t.Errorf("Format =\n%q", got)
	}

	sectioned := Exam{
		Title:           "Quiz",
		Shuffle:         ShuffleNone,
		ShowResult:      ShowScore,
		Monitor:         true,
		DurationMinutes: 30,
		Questions: []Question{
			{Source: "Q1", Options: []Option{{Source: "a", Correct: true}, {Source: "b"}}},
			{Source: "Q2", Options: []Option{{Source: "c", Correct: true}, {Source: "d"}}},
		},
		Sections: []Section{
			{Title: "Part A", Source: "Read **carefully**.", Start: 1},
			{Title: "Part B", Start: 2},
		},
	}
	want = lines(
		"# Quiz",
		"shuffle: none",
		"show_result: score",
		"monitor: on",
		"duration: 30",
		"",
		"## Part A",
		"Read **carefully**.",
		"",
		"1. Q1",
		"- a *",
		"- b",
		"",
		"## Part B",
		"",
		"2. Q2",
		"- c *",
		"- d",
		"",
	)
	if got := string(Format(sectioned)); got != want {
		t.Errorf("Format =\n%q\nwant\n%q", got, want)
	}

	sectioned.Sections = []Section{{Title: "A", Source: "1. first\n## second", Start: 1}}
	if got, want := string(Format(sectioned)), "\n## A\n    1. first\n    ## second\n\n1. Q1\n"; !strings.Contains(got, want) {
		t.Errorf("Format =\n%q\nwant it to contain\n%q", got, want)
	}

	sectioned.Sections = []Section{{Title: "A", Start: 1}, {Title: "Late", Source: "x", Start: 3}}
	if got, want := string(Format(sectioned)), "- d\n\n## Late\nx\n"; !strings.HasSuffix(got, want) {
		t.Errorf("Format =\n%q\nwant suffix\n%q", got, want)
	}
}

func FuzzFormatRoundTrip(f *testing.F) {
	for _, text := range roundTripTexts {
		f.Add([]byte(text))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := ReadUpload(data)
		if err != nil || len(u.Files) > 0 || u.Dir != "." {
			return
		}
		ex, err := Parse(u, "/media/k")
		if err != nil {
			return
		}
		out := Format(ex)
		ex2, err := Parse(Upload{Text: out, Dir: u.Dir, Files: u.Files}, "/media/k")
		if err != nil {
			t.Fatalf("Parse(Format(ex)): %v\nformatted:\n%q", err, out)
		}
		if !reflect.DeepEqual(ex, ex2) {
			t.Fatalf("round trip differs\nbefore: %#v\nafter:  %#v\nformatted:\n%q", ex, ex2, out)
		}
	})
}

func TestEncodeUpload(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		text := []byte("# T\n1. Q\n- a *\n- b\n")
		data, zipped, err := EncodeUpload(Upload{Text: text, Dir: ".", Files: map[string][]byte{}})
		if err != nil {
			t.Fatal(err)
		}
		if zipped || !bytes.Equal(data, text) {
			t.Errorf("EncodeUpload = %q, %v; want the text, false", data, zipped)
		}
	})

	t.Run("zip", func(t *testing.T) {
		u := Upload{Text: []byte("# T\n1. ![a](img/a.png)\n- a *\n- b\n"), Dir: "exam", Files: map[string][]byte{
			"exam/img/b.png": pngBytes(t, colorRGB(0, 255, 0)),
			"exam/img/a.png": pngBytes(t, colorRGB(255, 0, 0)),
			"c.png":          pngBytes(t, colorRGB(0, 0, 255)),
		}}
		data, zipped, err := EncodeUpload(u)
		if err != nil {
			t.Fatal(err)
		}
		if !zipped {
			t.Error("zipped = false")
		}
		got, err := ReadUpload(data)
		if err != nil {
			t.Fatalf("ReadUpload: %v", err)
		}
		if !reflect.DeepEqual(got, u) {
			t.Errorf("ReadUpload = %+v, want %+v", got, u)
		}

		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
			if f.Method != zip.Deflate {
				t.Errorf("%s: method %d, want Deflate", f.Name, f.Method)
			}
		}
		wantNames := []string{"exam/exam.md", "c.png", "exam/img/a.png", "exam/img/b.png"}
		if !reflect.DeepEqual(names, wantNames) {
			t.Errorf("entries = %q, want %q", names, wantNames)
		}

		again, _, err := EncodeUpload(u)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, data) {
			t.Error("two calls returned different bytes")
		}
	})

	t.Run("zip at root", func(t *testing.T) {
		u := Upload{Text: []byte("# T\n"), Dir: ".", Files: map[string][]byte{"a.png": {1}}}
		data, _, err := EncodeUpload(u)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if zr.File[0].Name != "exam.md" {
			t.Errorf("first entry = %q, want exam.md", zr.File[0].Name)
		}
	})
}
