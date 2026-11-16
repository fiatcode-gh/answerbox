package examfile

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParse_Defaults(t *testing.T) {
	ex := mustParse(t, textUpload(lines(
		"# Arithmetic",
		"",
		"1. One plus one?",
		"- 1",
		"- 2 *",
		"- 3",
		"",
		"2. Two plus two?",
		"- 4 *",
		"- 5",
	)))

	if ex.Title != "Arithmetic" {
		t.Errorf("Title = %q", ex.Title)
	}
	if ex.Shuffle != ShuffleQuestions || ex.ShowResult != ShowScore {
		t.Errorf("Shuffle, ShowResult = %q, %q; want questions, score", ex.Shuffle, ex.ShowResult)
	}
	type opt struct {
		html    string
		correct bool
	}
	type q struct {
		html string
		opts []opt
	}
	want := []q{
		{"<p>One plus one?</p>\n", []opt{{"<p>1</p>\n", false}, {"<p>2</p>\n", true}, {"<p>3</p>\n", false}}},
		{"<p>Two plus two?</p>\n", []opt{{"<p>4</p>\n", true}, {"<p>5</p>\n", false}}},
	}
	var got []q
	for _, qq := range ex.Questions {
		g := q{html: string(qq.BodyHTML)}
		for _, o := range qq.Options {
			g.opts = append(g.opts, opt{string(o.BodyHTML), o.Correct})
		}
		got = append(got, g)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("questions =\n%#v\nwant\n%#v", got, want)
	}
	if len(ex.Images) != 0 {
		t.Errorf("Images = %v, want none", ex.Images)
	}
}

func TestParse_HeaderOptions(t *testing.T) {
	const body = "1. Q\n- a *\n- b\n"
	cases := []struct {
		headers string
		shuffle Shuffle
		show    ShowResult
	}{
		{"shuffle: none", ShuffleNone, ShowScore},
		{"shuffle: questions", ShuffleQuestions, ShowScore},
		{"shuffle: questions, options", ShuffleQuestionsOptions, ShowScore},
		{"shuffle:Questions , Options  ", ShuffleQuestionsOptions, ShowScore},
		{"shuffle \t:   questions,options", ShuffleQuestionsOptions, ShowScore},
		{"show_result: none", ShuffleQuestions, ShowNone},
		{"show_result: score", ShuffleQuestions, ShowScore},
		{"show_result: REVIEW", ShuffleQuestions, ShowReview},
		{"\nshuffle: none\n\n\nshow_result: review\n\n", ShuffleNone, ShowReview},
		{"show_result: none\nshuffle: questions, options", ShuffleQuestionsOptions, ShowNone},
	}
	for _, c := range cases {
		t.Run(c.headers, func(t *testing.T) {
			ex := mustParse(t, textUpload("# T\n"+c.headers+"\n"+body))
			if ex.Shuffle != c.shuffle || ex.ShowResult != c.show {
				t.Errorf("got %q, %q; want %q, %q", ex.Shuffle, ex.ShowResult, c.shuffle, c.show)
			}
		})
	}
}

func TestParse_MonitorHeader(t *testing.T) {
	const body = "1. Q\n- a *\n- b\n"
	cases := []struct {
		headers string
		want    bool
	}{
		{"", true},
		{"monitor: on", true},
		{"monitor: off", false},
		{"monitor:OFF  ", false},
		{"shuffle: none\nmonitor \t: On\nshow_result: review", true},
	}
	for _, c := range cases {
		t.Run(c.headers, func(t *testing.T) {
			ex := mustParse(t, textUpload("# T\n"+c.headers+"\n"+body))
			if ex.Monitor != c.want {
				t.Errorf("Monitor = %v, want %v", ex.Monitor, c.want)
			}
		})
	}
}

func TestParse_Markdown(t *testing.T) {
	src := lines(
		"# Markdown",
		"",
		"1) Some **bold**, *italic* and `code`.",
		"   Second line.",
		"",
		"   ```go",
		"   - not an option",
		"   x := 1",
		"",
		"   1. not a question",
		"   ```",
		"   What is x?",
		"- first line *",
		"  second line",
		"- other",
		"2) Next",
		"- a *",
		"- b",
	)
	ex := mustParse(t, textUpload(src))
	if len(ex.Questions) != 2 {
		t.Fatalf("got %d questions, want 2", len(ex.Questions))
	}
	q := ex.Questions[0]
	body := string(q.BodyHTML)
	for _, want := range []string{
		"<strong>bold</strong>",
		"<em>italic</em>",
		"<code>code</code>",
		"<code>code</code>.<br>\nSecond line.</p>",
		"<pre><code class=\"language-go\">- not an option\nx := 1\n\n1. not a question\n</code></pre>",
		"<p>What is x?</p>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody: %s", want, body)
		}
	}
	if len(q.Options) != 2 {
		t.Fatalf("got %d options, want 2", len(q.Options))
	}
	if got, want := string(q.Options[0].BodyHTML), "<p>first line<br>\nsecond line</p>\n"; got != want {
		t.Errorf("multi-line option = %q, want %q", got, want)
	}
	if !q.Options[0].Correct || q.Options[1].Correct {
		t.Errorf("correct flags = %v, %v", q.Options[0].Correct, q.Options[1].Correct)
	}

	t.Run("CRLF and BOM", func(t *testing.T) {
		for name, data := range map[string]string{
			"CRLF":     strings.ReplaceAll(src, "\n", "\r\n"),
			"CR":       strings.ReplaceAll(src, "\n", "\r"),
			"BOM+CRLF": "\uFEFF" + strings.ReplaceAll(src, "\n", "\r\n"),
		} {
			u, err := ReadUpload([]byte(data))
			if err != nil {
				t.Fatalf("%s: ReadUpload: %v", name, err)
			}
			got := mustParse(t, u)
			if !reflect.DeepEqual(got, ex) {
				t.Errorf("%s: parse differs from LF input\ngot  %#v\nwant %#v", name, got, ex)
			}
		}
	})
}

func TestParse_CorrectMarker(t *testing.T) {
	cases := []struct {
		line    string
		correct bool
		html    string // rendered option when correct; "" skips the check
	}{
		{"7*", true, "<p>7</p>\n"},
		{"7 *", true, "<p>7</p>\n"},
		{"**bold**", false, ""},
		{"**bold** *", true, "<p><strong>bold</strong></p>\n"},
		{"**bold***", true, "<p><strong>bold</strong></p>\n"},
		{"*italic*", false, ""},
		{"2 * 3 = 6 *", true, "<p>2 * 3 = 6</p>\n"},
		{`x\*`, false, ""},
		{"`a*` b *", true, "<p><code>a*</code> b</p>\n"},
		{"`a*`*", true, "<p><code>a*</code></p>\n"},
		{"*\n  ```\n  code\n  ```", true, "<pre><code>code\n</code></pre>\n"},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			other := "- other"
			if !c.correct {
				other += " *"
			}
			ex := mustParse(t, textUpload(lines("# T", "1. Q", "- "+c.line, other)))
			opts := ex.Questions[0].Options
			if opts[0].Correct != c.correct || opts[1].Correct == c.correct {
				t.Fatalf("Correct = %v, %v; want %v, %v", opts[0].Correct, opts[1].Correct, c.correct, !c.correct)
			}
			if c.html != "" && string(opts[0].BodyHTML) != c.html {
				t.Errorf("BodyHTML = %q, want %q", opts[0].BodyHTML, c.html)
			}
		})
	}
}

func TestParse_RawHTMLNotRendered(t *testing.T) {
	ex := mustParse(t, textUpload(lines(
		"# T",
		"1. Inline <b onclick=\"x()\">bold</b> and [x](javascript:alert(1))",
		"",
		"   <script>alert(1)</script>",
		"- <img src=x onerror=alert(1)> *",
		"- b",
	)))
	q := ex.Questions[0]
	body := string(q.BodyHTML)
	for _, bad := range []string{"<script", "<b ", "javascript:", "onclick"} {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q: %s", bad, body)
		}
	}
	if opt := string(q.Options[0].BodyHTML); strings.Contains(opt, "<img") || strings.Contains(opt, "onerror") {
		t.Errorf("option contains raw HTML: %s", opt)
	}
}

func TestParse_DurationHeader(t *testing.T) {
	const body = "1. Q\n- a *\n- b\n"
	cases := []struct {
		headers string
		want    int
	}{
		{"", 60},
		{"duration: 5", 5},
		{"duration: 240", 240},
		{"duration:45  ", 45},
		{"duration \t:  90", 90},
		{"shuffle: none\nduration: 30\nmonitor: off\nshow_result: review", 30},
		{"duration: 120\nshow_result: none\nshuffle: none", 120},
	}
	for _, c := range cases {
		t.Run(c.headers, func(t *testing.T) {
			ex := mustParse(t, textUpload("# T\n"+c.headers+"\n"+body))
			if ex.DurationMinutes != c.want {
				t.Errorf("DurationMinutes = %d, want %d", ex.DurationMinutes, c.want)
			}
		})
	}
	ex := mustParse(t, textUpload("# T\nshuffle: none\nduration: 30\nmonitor: off\nshow_result: review\n"+body))
	if ex.Shuffle != ShuffleNone || ex.Monitor || ex.ShowResult != ShowReview {
		t.Errorf("other headers = %q, %v, %q; want none, false, review", ex.Shuffle, ex.Monitor, ex.ShowResult)
	}
}

func durationMsg(line int) string {
	return fmt.Sprintf("line %d: duration must be a whole number of minutes from 5 to 240", line)
}

func TestParse_Rejections(t *testing.T) {
	q := "1. Q\n- a *\n- b"
	many := func(n int) string {
		var b strings.Builder
		b.WriteString("# T\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d. Q%d\n- a *\n- b\n", i, i)
		}
		return b.String()
	}
	opts := func(n int) string {
		var b strings.Builder
		b.WriteString("# T\n1. Q\n- a *\n")
		for i := 2; i <= n; i++ {
			fmt.Fprintf(&b, "- o%d\n", i)
		}
		return b.String()
	}
	cases := []struct {
		name  string
		input string
		files map[string][]byte
		want  string
	}{
		{"empty file", "", nil, `line 1: expected "# Title" as the first line`},
		{"no title", "\n\nHello\n" + q, nil, `line 3: expected "# Title" as the first line`},
		{"heading level 2", "## T\n" + q, nil, `line 1: expected "# Title" as the first line`},
		{"long title", "# " + strings.Repeat("é", 201) + "\n" + q, nil, `line 1: title is longer than 200 characters`},
		{"header zone junk", "# T\n\nintro\n" + q, nil, `line 3: expected "shuffle:", "show_result:", "monitor:", "duration:" or question 1`},
		{"duplicate header", "# T\nshuffle: none\nshow_result: none\nshuffle: none\n" + q, nil, `line 4: duplicate "shuffle" header`},
		{"bad shuffle", "# T\nshuffle: random\n" + q, nil, `line 2: shuffle must be "none", "questions" or "questions, options"`},
		{"bad show_result", "# T\nshow_result: all\n" + q, nil, `line 2: show_result must be "none", "score" or "review"`},
		{"bad monitor", "# T\nmonitor: yes\n" + q, nil, `line 2: monitor must be "on" or "off"`},
		{"duplicate monitor", "# T\nmonitor: on\nmonitor: off\n" + q, nil, `line 3: duplicate "monitor" header`},
		{"duration 4", "# T\nduration: 4\n" + q, nil, durationMsg(2)},
		{"duration 241", "# T\nduration: 241\n" + q, nil, durationMsg(2)},
		{"duration 0", "# T\nduration: 0\n" + q, nil, durationMsg(2)},
		{"duration leading zero", "# T\nduration: 045\n" + q, nil, durationMsg(2)},
		{"duration with unit", "# T\nshuffle: none\nduration: 60 minutes\n" + q, nil, durationMsg(3)},
		{"duration fraction", "# T\nduration: 1.5\n" + q, nil, durationMsg(2)},
		{"duration negative", "# T\nduration: -5\n" + q, nil, durationMsg(2)},
		{"duration plus sign", "# T\nduration: +5\n" + q, nil, durationMsg(2)},
		{"duration empty", "# T\nduration:\n" + q, nil, durationMsg(2)},
		{"duration 1000", "# T\nduration: 1000\n" + q, nil, durationMsg(2)},
		{"duration words", "# T\nduration: sixty\n" + q, nil, durationMsg(2)},
		{"duplicate duration", "# T\nduration: 30\nduration: 30\n" + q, nil, `line 3: duplicate "duration" header`},
		{"first question not 1", "# T\n2. Q\n- a *\n- b", nil, `line 2: expected question 1, found question 2`},
		{"question skipped", "# T\n" + q + "\n3. Q\n- a *\n- b", nil, `line 5: expected question 2, found question 3`},
		{"501 questions", many(501), nil, `exam has more than 500 questions`},
		{"unclosed fence", "# T\n1. Q\n   ok\n   ```\n   code\n- a *\n- b", nil, `line 4: code block is never closed`},
		{"unclosed second fence", "# T\n1. Q\n```\nx\n```\n- a *\n- b\n  ```\n  y", nil, `line 8: code block is never closed`},
		{"no questions", "# T\nshuffle: none\n", nil, `exam has no questions`},
		{"question without text", "# T\n1.\n- a *\n- b", nil, `question 1 (line 2) has no text`},
		{"one option", "# T\n" + q + "\n2. Q\n- a *", nil, `question 2 (line 5) needs at least two options`},
		{"eleven options", opts(11), nil, `question 1 (line 2) has more than 10 options`},
		{"option without text", "# T\n1. Q\n- a *\n-\n- c", nil, `question 1, option 2 (line 4) has no text`},
		{"no correct option", "# T\n1. Q\n- a\n- b", nil, `question 1 (line 2) has no correct option`},
		{"two correct options", "# T\n1. Q\n- a *\n- b *", nil, `question 1 (line 2) has more than one correct option`},
		{"image missing", "# T\n1. Q\n- a *\n- ![x](nope.png)", nil, `question 1: image "nope.png" not found in upload`},
		{"image URL", "# T\n1. ![x](http://example.com/a.png)\n- a *\n- b", nil, `question 1: image "http://example.com/a.png" must be a file inside the upload`},
		{"image absolute", "# T\n1. ![x](/a.png)\n- a *\n- b", map[string][]byte{"a.png": pngBytes(t, colorRGB(1, 2, 3))}, `question 1: image "/a.png" must be a file inside the upload`},
		{"image escaping", "# T\n1. ![x](../a.png)\n- a *\n- b", nil, `question 1: image "../a.png" must be a file inside the upload`},
		{"image bad escape", "# T\n1. ![x](a%zz.png)\n- a *\n- b", nil, `question 1: image "a%zz.png" must be a file inside the upload`},
		{"image not an image", "# T\n1. ![x](a.png)\n- a *\n- b", map[string][]byte{"a.png": []byte("hello")}, `question 1: image "a.png" is not a PNG, JPEG, GIF or WebP file`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := textUpload(c.input)
			if c.files != nil {
				u.Files = c.files
			}
			_, err := Parse(u, "/media/k")
			if got := errMsg(t, err); got != c.want {
				t.Errorf("message = %q\nwant      %q", got, c.want)
			}
		})
	}
}

func TestParse_Images(t *testing.T) {
	a := pngBytes(t, colorRGB(255, 0, 0))
	b := pngBytes(t, colorRGB(0, 255, 0))
	unused := pngBytes(t, colorRGB(0, 0, 255))
	jpg, gf, webp := jpegBytes(t), gifBytes(t), webpBytes()

	t.Run("body, option, dedupe, percent", func(t *testing.T) {
		u := Upload{Dir: ".", Files: map[string][]byte{
			"img/a.png":   a,
			"img/b c.png": b,
			"unused.png":  unused,
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
		))}
		ex := mustParse(t, u)
		an, bn := imageName(a, ".png"), imageName(b, ".png")
		want := []Image{
			{an, a}, {bn, b},
			{imageName(jpg, ".jpg"), jpg}, {imageName(gf, ".gif"), gf}, {imageName(webp, ".webp"), webp},
		}
		if !reflect.DeepEqual(ex.Images, want) {
			var names []string
			for _, im := range ex.Images {
				names = append(names, im.Name)
			}
			t.Fatalf("Images = %v", names)
		}
		checkSrc := func(html, name string) {
			t.Helper()
			if !strings.Contains(html, `src="/media/k/`+name+`"`) {
				t.Errorf("html %q lacks src for %s", html, name)
			}
		}
		q1 := ex.Questions[0]
		checkSrc(string(q1.BodyHTML), an)
		checkSrc(string(q1.Options[0].BodyHTML), bn)
		checkSrc(string(q1.Options[1].BodyHTML), an)
		checkSrc(string(ex.Questions[1].Options[1].BodyHTML), an)
	})

	t.Run("relative to exam folder", func(t *testing.T) {
		u := Upload{Dir: "exam", Files: map[string][]byte{
			"exam/img/a.png": a,
			"b.png":          b,
		}, Text: []byte("# T\n1. ![a](img/a.png)\n- ![b](../b.png) *\n- c")}
		ex := mustParse(t, u)
		if len(ex.Images) != 2 || ex.Images[0].Name != imageName(a, ".png") || ex.Images[1].Name != imageName(b, ".png") {
			t.Errorf("Images = %+v", ex.Images)
		}
	})

	t.Run("escaping the zip", func(t *testing.T) {
		u := Upload{Dir: "exam", Files: map[string][]byte{"b.png": b}, Text: []byte("# T\n1. ![b](../../b.png)\n- a *\n- c")}
		_, err := Parse(u, "/media/k")
		if got, want := errMsg(t, err), `question 1: image "../../b.png" must be a file inside the upload`; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})
}
