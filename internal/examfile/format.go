package examfile

import (
	"archive/zip"
	"bytes"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

var shuffleText = map[Shuffle]string{
	ShuffleNone:             "none",
	ShuffleQuestions:        "questions",
	ShuffleQuestionsOptions: "questions, options",
}

// Format writes ex as exam text. For an exam whose Sources are block texts as
// Parse returns them, Parse of the result (with the same files and media
// base) returns the same title, settings, Sources, HTML and correct options.
// Format does not validate ex.
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
	for k, q := range ex.Questions {
		b.WriteString("\n")
		writeBlock(&b, strconv.Itoa(k+1)+".", q.Source, "")
		for _, o := range q.Options {
			marker := ""
			if o.Correct {
				marker = "*"
			}
			writeBlock(&b, "-", o.Source, marker)
		}
	}
	return []byte(b.String())
}

// writeBlock writes lead, the first line of source and marker separated by
// single spaces (empty parts left out), then the continuation lines. When a
// continuation line outside a fenced block would start a question at column
// 0, every non-empty continuation line is indented by four spaces, which
// dedent removes again on parse.
func writeBlock(b *strings.Builder, lead, source, marker string) {
	first, rest, hasCont := strings.Cut(source, "\n")
	b.WriteString(lead)
	for _, part := range []string{first, marker} {
		if part != "" {
			b.WriteString(" " + part)
		}
	}
	b.WriteString("\n")
	if !hasCont {
		return
	}
	cont := strings.Split(rest, "\n")
	pad := ""
	if startsQuestion(cont) {
		pad = "    "
	}
	for _, l := range cont {
		if l != "" {
			b.WriteString(pad + l)
		}
		b.WriteString("\n")
	}
}

// startsQuestion reports whether a line outside a fenced block matches the
// question number grammar.
func startsQuestion(cont []string) bool {
	inFence := false
	for _, l := range cont {
		if isFence(l) {
			inFence = !inFence
			continue
		}
		if !inFence && questionRe.MatchString(l) {
			return true
		}
	}
	return false
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
