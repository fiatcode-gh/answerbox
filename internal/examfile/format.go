package examfile

import (
	"archive/zip"
	"bytes"
	"maps"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

var shuffleText = map[Shuffle]string{
	ShuffleNone:             "none",
	ShuffleQuestions:        "questions",
	ShuffleQuestionsOptions: "questions, options",
}

// Format writes ex as exam text. For an exam whose Sources and accepted
// answers are as Parse returns them, Parse of the result (with the same files
// and media base) returns the same title, settings, sections, Sources, HTML,
// correct options and accepted answers. Format does not validate ex: sections
// are written in slice order, each before the first question whose number is
// at least its Start, or after the last question when there is none.
func Format(ex Exam) []byte {
	var b strings.Builder
	monitor := "off"
	if ex.Monitor {
		monitor = "on"
	}
	b.WriteString("# " + ex.Title + "\n")
	b.WriteString("shuffle: " + shuffleText[ex.Shuffle] + "\n")
	b.WriteString("show_result: " + string(ex.ShowResult) + "\n")
	b.WriteString("monitor: " + monitor + "\n")
	b.WriteString("duration: " + strconv.Itoa(ex.DurationMinutes) + "\n")
	next := 0
	sectionsUpTo := func(number int) {
		for ; next < len(ex.Sections) && ex.Sections[next].Start <= number; next++ {
			s := ex.Sections[next]
			b.WriteString("\n## " + s.Title + "\n")
			if s.Source != "" {
				writeLines(&b, strings.Split(s.Source, "\n"))
			}
		}
	}
	for k, q := range ex.Questions {
		sectionsUpTo(k + 1)
		b.WriteString("\n")
		writeBlock(&b, strconv.Itoa(k+1)+".", q.Source, "")
		for _, o := range q.Options {
			marker := ""
			if o.Correct {
				marker = "*"
			}
			writeBlock(&b, "-", o.Source, marker)
		}
		for _, a := range q.Answers {
			writeBlock(&b, "=", a, "")
		}
	}
	sectionsUpTo(math.MaxInt)
	return []byte(b.String())
}

// writeBlock writes lead, the first line of source and marker separated by
// single spaces (empty parts left out), then the continuation lines.
func writeBlock(b *strings.Builder, lead, source, marker string) {
	first, rest, hasCont := strings.Cut(source, "\n")
	b.WriteString(lead)
	for _, part := range []string{first, marker} {
		if part != "" {
			b.WriteString(" " + part)
		}
	}
	b.WriteString("\n")
	if hasCont {
		writeLines(b, strings.Split(rest, "\n"))
	}
}

// writeLines writes continuation lines, every non-empty one indented by
// blockPad(lines), which dedent removes again on parse.
func writeLines(b *strings.Builder, lines []string) {
	pad := blockPad(lines)
	for _, l := range lines {
		if l != "" {
			b.WriteString(pad + l)
		}
		b.WriteString("\n")
	}
}

// blockPad is the indentation that keeps continuation lines text: eight
// spaces when a line outside a fenced block is an accepted-answer line, else
// four when one matches the question number grammar or, right-trimmed, is a
// section heading, else none.
func blockPad(lines []string) string {
	pad := ""
	inFence := false
	for _, l := range lines {
		if IsFence(l) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimRightFunc(l, unicode.IsSpace)
		if _, ok := answerLine(trimmed); ok {
			return "        "
		}
		if questionRe.MatchString(l) || sectionRe.MatchString(trimmed) {
			pad = "    "
		}
	}
	return pad
}

// EncodeUpload is the inverse of ReadUpload: the text alone when u has no
// files, else a zip with the text at <Dir>/exam.md and every file at its key.
func EncodeUpload(u Upload) (data []byte, zipped bool, err error) {
	if len(u.Files) == 0 {
		return u.Text, false, nil
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, content []byte) error {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = f.Write(content)
		return err
	}
	if err := add(path.Join(u.Dir, "exam.md"), u.Text); err != nil {
		return nil, false, err
	}
	for _, k := range slices.Sorted(maps.Keys(u.Files)) {
		if err := add(k, u.Files[k]); err != nil {
			return nil, false, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}
