package examfile

import (
	"errors"
	"fmt"
	"html"
	"html/template"
	"regexp"
	"strings"
)

// RebuildError says which block of an exam could not be rebuilt exactly.
// Question and Option are 1-based; Option 0 is the question text and
// Question 0 the exam as a whole. Reason is a fixed phrase, except that a
// whole exam that does not parse back appends the parser's message; neither
// carries exam text.
type RebuildError struct {
	Question, Option int
	Reason           string
}

func (e *RebuildError) Error() string {
	switch {
	case e.Question == 0:
		return "exam: " + e.Reason
	case e.Option == 0:
		return fmt.Sprintf("question %d: %s", e.Question, e.Reason)
	default:
		return fmt.Sprintf("question %d, option %d: %s", e.Question, e.Option, e.Reason)
	}
}

const (
	reasonMarkup    = "unsupported markup"
	reasonHTML      = "does not render back to the stored HTML"
	reasonStructure = "changes the question structure"
	reasonImage     = "image file is missing"
	reasonParse     = "does not parse back"
	reasonSettings  = "changes the exam settings"
)

var (
	codeBlockRe = regexp.MustCompile(`(?s)^<pre><code(?: class="language-([^"]*)")?>(.*?)</code></pre>\n`)
	tagRe       = regexp.MustCompile(`^<(/?)([a-z]+)((?: [a-z]+="[^"]*")*)>`)
	attrRe      = regexp.MustCompile(` ([a-z]+)="([^"]*)"`)
	mediaNameRe = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
)

const asciiPunct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// Rebuild returns ex with Source filled for every question and option, Dir
// "." and Images and ImagePaths reduced to the referenced images.
// ex holds the stored exam: Title, the four settings, BodyHTML and Correct of
// every block, and in Images every image file of the exam's media folder
// (Name = file name). mediaBase is the prefix of its image URLs ("/media/<key>").
// Every error is a *RebuildError.
//
// Each block is converted from the HTML grammar goldmark produces for
// paragraphs, emphasis, inline code, line breaks, images and fenced code,
// trying three text escape levels until a probe exam renders the block back
// identically. The whole rebuilt exam must then format and parse back to the
// same settings, Sources, HTML and correct options.
func Rebuild(ex Exam, mediaBase string) (Exam, error) {
	rb := rebuilder{mediaBase: mediaBase, files: map[string][]byte{}}
	for _, img := range ex.Images {
		rb.files[img.Name] = img.Data
	}
	out := Exam{
		Title:           ex.Title,
		Shuffle:         ex.Shuffle,
		ShowResult:      ex.ShowResult,
		Monitor:         ex.Monitor,
		DurationMinutes: ex.DurationMinutes,
		Questions:       make([]Question, len(ex.Questions)),
	}
	for k, q := range ex.Questions {
		src, reason := rb.block(q.BodyHTML, rb.probeQuestion(q))
		if reason != "" {
			return Exam{}, &RebuildError{Question: k + 1, Reason: reason}
		}
		out.Questions[k] = Question{Source: src, BodyHTML: q.BodyHTML, Options: make([]Option, len(q.Options))}
		for m, o := range q.Options {
			src, reason := rb.block(o.BodyHTML, rb.probeOption(o))
			if reason != "" {
				return Exam{}, &RebuildError{Question: k + 1, Option: m + 1, Reason: reason}
			}
			out.Questions[k].Options[m] = Option{Source: src, BodyHTML: o.BodyHTML, Correct: o.Correct}
		}
	}

	got, err := rb.parse(Format(out))
	if err != nil {
		reason := reasonParse
		if pe := (*Error)(nil); errors.As(err, &pe) {
			reason += ": " + pe.Msg
		}
		return Exam{}, &RebuildError{Reason: reason}
	}
	if got.Title != out.Title || got.Shuffle != out.Shuffle || got.ShowResult != out.ShowResult ||
		got.Monitor != out.Monitor || got.DurationMinutes != out.DurationMinutes {
		return Exam{}, &RebuildError{Reason: reasonSettings}
	}
	if len(got.Questions) != len(out.Questions) {
		return Exam{}, &RebuildError{Reason: reasonStructure}
	}
	for k, q := range out.Questions {
		g := got.Questions[k]
		if len(g.Options) != len(q.Options) {
			return Exam{}, &RebuildError{Question: k + 1, Reason: reasonStructure}
		}
		if reason := compare(q.Source, q.BodyHTML, g.Source, g.BodyHTML); reason != "" {
			return Exam{}, &RebuildError{Question: k + 1, Reason: reason}
		}
		for m, o := range q.Options {
			gotOpt := g.Options[m]
			reason := compare(o.Source, o.BodyHTML, gotOpt.Source, gotOpt.BodyHTML)
			if reason == "" && gotOpt.Correct != o.Correct {
				reason = reasonStructure
			}
			if reason != "" {
				return Exam{}, &RebuildError{Question: k + 1, Option: m + 1, Reason: reason}
			}
		}
	}
	return got, nil
}

func compare(wantSrc string, wantHTML template.HTML, gotSrc string, gotHTML template.HTML) string {
	switch {
	case gotSrc != wantSrc:
		return reasonStructure
	case gotHTML != wantHTML:
		return reasonHTML
	}
	return ""
}

type rebuilder struct {
	mediaBase string
	files     map[string][]byte // image file name → data
}

func (rb *rebuilder) parse(text []byte) (Exam, error) {
	return Parse(Upload{Text: text, Dir: ".", Files: rb.files}, rb.mediaBase)
}

// probeExam is a minimal valid exam around the blocks under test.
func probeExam(q Question) Exam {
	return Exam{
		Title:           "T",
		Shuffle:         ShuffleNone,
		ShowResult:      ShowNone,
		Monitor:         true,
		DurationMinutes: DefaultDurationMinutes,
		Questions:       []Question{q},
	}
}

// probeQuestion checks a candidate source for question text q in its own
// role, returning "" when it parses back to itself and q's HTML.
func (rb *rebuilder) probeQuestion(q Question) func(string) string {
	return func(src string) string {
		got, err := rb.parse(Format(probeExam(Question{Source: src, Options: []Option{
			{Source: "a", Correct: true},
			{Source: "b"},
		}})))
		if err != nil {
			return reasonParse
		}
		if len(got.Questions) != 1 || len(got.Questions[0].Options) != 2 {
			return reasonStructure
		}
		return compare(src, q.BodyHTML, got.Questions[0].Source, got.Questions[0].BodyHTML)
	}
}

// probeOption checks a candidate source for option o in its own role.
func (rb *rebuilder) probeOption(o Option) func(string) string {
	return func(src string) string {
		got, err := rb.parse(Format(probeExam(Question{Source: "Q", Options: []Option{
			{Source: src, Correct: o.Correct},
			{Source: "x", Correct: !o.Correct},
		}})))
		if err != nil {
			return reasonParse
		}
		if len(got.Questions) != 1 || len(got.Questions[0].Options) != 2 {
			return reasonStructure
		}
		g := got.Questions[0].Options[0]
		if g.Correct != o.Correct {
			return reasonStructure
		}
		return compare(src, o.BodyHTML, g.Source, g.BodyHTML)
	}
}

// segment is a piece of rebuilt source: literal markup, or visible text that
// is escaped by the chosen level.
type segment struct {
	s    string
	text bool
}

// block converts stored HTML to source text, accepting the first escape
// level that probe accepts. It returns the source or a failure reason.
func (rb *rebuilder) block(h template.HTML, probe func(string) string) (string, string) {
	segs, reason := rb.segments(string(h))
	if reason != "" {
		return "", reason
	}
	prev := ""
	for level := range 3 {
		src := join(segs, level)
		if level > 0 && src == prev {
			continue
		}
		prev = src
		if reason = probe(src); reason == "" {
			return src, ""
		}
	}
	return "", reason
}

func join(segs []segment, level int) string {
	var b strings.Builder
	for _, s := range segs {
		if s.text {
			b.WriteString(escape(s.s, level))
		} else {
			b.WriteString(s.s)
		}
	}
	return b.String()
}

// escape backslash-escapes text: level 0 not at all, level 1 the characters
// that start inline markup, level 2 every ASCII punctuation character.
func escape(s string, level int) string {
	set := ""
	switch level {
	case 1:
		set = "\\`*_[]<&"
	case 2:
		set = asciiPunct
	}
	if set == "" || !strings.ContainsAny(s, set) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(set, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// segments splits h into paragraphs and fenced code blocks joined by one
// empty line. A leading code block gets an empty first line before it:
// Format writes the first line after the question number or dash, where the
// parser does not see an opening fence.
func (rb *rebuilder) segments(h string) ([]segment, string) {
	if h == "" {
		return nil, reasonMarkup
	}
	var segs []segment
	for h != "" {
		if len(segs) > 0 {
			segs = append(segs, segment{s: "\n\n"})
		}
		if m := codeBlockRe.FindStringSubmatch(h); m != nil {
			if len(segs) == 0 {
				segs = append(segs, segment{s: "\n"})
			}
			code := html.UnescapeString(m[2])
			fence := strings.Repeat("`", max(3, longestRun(code)+1))
			segs = append(segs, segment{s: fence + html.UnescapeString(m[1]) + "\n" + code + fence})
			h = h[len(m[0]):]
			continue
		}
		inner, ok := strings.CutPrefix(h, "<p>")
		if !ok {
			return nil, reasonMarkup
		}
		end := strings.Index(inner, "</p>\n")
		if end < 0 {
			return nil, reasonMarkup
		}
		para, reason := rb.inline(inner[:end])
		if reason != "" {
			return nil, reason
		}
		segs = append(segs, para...)
		h = inner[end+len("</p>\n"):]
	}
	return segs, ""
}

// inline converts the content of a paragraph.
func (rb *rebuilder) inline(h string) ([]segment, string) {
	var segs []segment
	for h != "" {
		i := strings.IndexAny(h, "<>")
		if i < 0 {
			segs = append(segs, segment{s: html.UnescapeString(h), text: true})
			break
		}
		if h[i] == '>' {
			return nil, reasonMarkup
		}
		if i > 0 {
			segs = append(segs, segment{s: html.UnescapeString(h[:i]), text: true})
		}
		h = h[i:]
		m := tagRe.FindStringSubmatch(h)
		if m == nil {
			return nil, reasonMarkup
		}
		h = h[len(m[0]):]
		closing, name, attrs := m[1] == "/", m[2], attrRe.FindAllStringSubmatch(m[3], -1)
		if closing && len(attrs) > 0 {
			return nil, reasonMarkup
		}
		switch {
		case name == "em" && len(attrs) == 0:
			segs = append(segs, segment{s: "*"})
		case name == "strong" && len(attrs) == 0:
			segs = append(segs, segment{s: "**"})
		case name == "br" && !closing && len(attrs) == 0:
			rest, ok := strings.CutPrefix(h, "\n")
			if !ok {
				return nil, reasonMarkup
			}
			h = rest
			segs = append(segs, segment{s: "\n"})
		case name == "code" && !closing && len(attrs) == 0:
			end := strings.Index(h, "</code>")
			if end < 0 || strings.ContainsAny(h[:end], "<>") {
				return nil, reasonMarkup
			}
			segs = append(segs, segment{s: codeSpan(html.UnescapeString(h[:end]))})
			h = h[end+len("</code>"):]
		case name == "img" && !closing:
			img, reason := rb.image(attrs)
			if reason != "" {
				return nil, reason
			}
			segs = append(segs, segment{s: img})
		default:
			return nil, reasonMarkup
		}
	}
	return segs, ""
}

// image converts the attributes of an <img> tag to an image reference by
// file name.
func (rb *rebuilder) image(attrs [][]string) (string, string) {
	if len(attrs) != 2 {
		return "", reasonMarkup
	}
	vals := map[string]string{}
	for _, a := range attrs {
		vals[a[1]] = html.UnescapeString(a[2])
	}
	src, okSrc := vals["src"]
	alt, okAlt := vals["alt"]
	if !okSrc || !okAlt {
		return "", reasonMarkup
	}
	name, ok := strings.CutPrefix(src, rb.mediaBase+"/")
	if !ok || !mediaNameRe.MatchString(name) {
		return "", reasonMarkup
	}
	if _, ok := rb.files[name]; !ok {
		return "", reasonImage
	}
	return "![" + escape(alt, 2) + "](" + name + ")", ""
}

// codeSpan wraps t in a backtick run longer than any inside it, padded with
// a space where the run or t's own spaces would otherwise be lost.
func codeSpan(t string) string {
	fence := strings.Repeat("`", longestRun(t)+1)
	if strings.HasPrefix(t, "`") || strings.HasSuffix(t, "`") ||
		(strings.HasPrefix(t, " ") && strings.HasSuffix(t, " ") && strings.Trim(t, " ") != "") {
		t = " " + t + " "
	}
	return fence + t + fence
}

func longestRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}
