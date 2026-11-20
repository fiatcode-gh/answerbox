package examfile

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// errCase is one refusal of Parse or ReadUpload and the Code, Line and Args
// its *Error must carry.
type errCase struct {
	name string
	err  func(t *testing.T) error
	code Code
	line int
	args []any
}

func parseErr(text string, files map[string][]byte) func(t *testing.T) error {
	return func(t *testing.T) error {
		u := textUpload(text)
		if files != nil {
			u.Files = files
		}
		_, err := Parse(u, "/media/k")
		return err
	}
}

func uploadErr(data func(t *testing.T) []byte) func(t *testing.T) error {
	return func(t *testing.T) error {
		_, err := ReadUpload(data(t))
		return err
	}
}

func bytesOf(b []byte) func(t *testing.T) []byte {
	return func(*testing.T) []byte { return b }
}

func zipOf(entries ...zipEntry) func(t *testing.T) []byte {
	return func(t *testing.T) []byte { return buildZip(t, entries...) }
}

// errorCases has at least one refusal for every code this package raises.
func errorCases() []errCase {
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
	answers := func(n int, text string) string {
		var b strings.Builder
		b.WriteString("# T\n1. Q\n")
		for range n {
			b.WriteString("= " + text + "\n")
		}
		return b.String()
	}
	hello := map[string][]byte{"a.png": []byte("hello")}
	md := zipEntry{name: "exam.md", data: []byte(examMD)}
	tooMany := []zipEntry{md}
	for i := range MaxZipFiles {
		tooMany = append(tooMany, zipEntry{name: fmt.Sprintf("f%d.png", i), data: []byte("x")})
	}
	tooBig := []zipEntry{md}
	for i := range 5 {
		tooBig = append(tooBig, zipEntry{name: fmt.Sprintf("z%d.bin", i), data: make([]byte, MaxZipEntryBytes)})
	}

	return []errCase{
		{"empty file", parseErr("", nil), CodeTitleMissing, 1, nil},
		{"no title", parseErr("\n\nHello\n"+q, nil), CodeTitleMissing, 3, nil},
		{"long title", parseErr("# "+strings.Repeat("é", 201)+"\n"+q, nil), CodeTitleTooLong, 1, []any{200}},
		{"header zone junk", parseErr("# T\n\nintro\n"+q, nil), CodeHeaderExpected, 3, nil},
		{"heading after question 1", parseErr("# T\n"+q+"\n## Late\n2. Q\n- a *\n- b", nil), CodeSectionLate, 5, nil},
		{"empty section title", parseErr("# T\n## \n"+q, nil), CodeSectionTitleEmpty, 2, nil},
		{"long section title", parseErr("# T\n## "+strings.Repeat("é", 201)+"\n"+q, nil), CodeSectionTitleTooLong, 2, []any{200}},
		{"51 sections", parseErr(sections(51), nil), CodeTooManySections, 0, []any{50}},
		{"question skipped", parseErr("# T\n"+q+"\n3. Q\n- a *\n- b", nil), CodeQuestionNumber, 5, []any{2, 3}},
		{"501 questions", parseErr(many(501), nil), CodeTooManyQuestions, 0, []any{500}},
		{"unclosed fence", parseErr("# T\n1. Q\n   ok\n   ```\n   code\n- a *\n- b", nil), CodeFenceOpen, 4, nil},
		{"no questions", parseErr("# T\nshuffle: none\n", nil), CodeNoQuestions, 0, nil},
		{"section at the end", parseErr("# T\n## A\n"+q+"\n## B\n", nil), CodeSectionEmpty, 6, []any{2}},
		{"duplicate header", parseErr("# T\nshuffle: none\nshow_result: none\nshuffle: none\n"+q, nil), CodeHeaderDuplicate, 4, []any{"shuffle"}},
		{"bad shuffle", parseErr("# T\nshuffle: random\n"+q, nil), CodeShuffleValue, 2, nil},
		{"bad show_result", parseErr("# T\nshow_result: all\n"+q, nil), CodeShowResultValue, 2, nil},
		{"bad monitor", parseErr("# T\nmonitor: yes\n"+q, nil), CodeMonitorValue, 2, nil},
		{"bad duration", parseErr("# T\nshuffle: none\nduration: 60 minutes\n"+q, nil), CodeDurationValue, 3, []any{5, 240}},
		{"question without text", parseErr("# T\n1.\n- a *\n- b", nil), CodeQuestionNoText, 2, []any{1}},
		{"options and answers", parseErr("# T\n1. Q\n- a *\n- b\n= c", nil), CodeQuestionBothKinds, 2, []any{1}},
		{"neither", parseErr("# T\n1. Q\n- a *\n- b\n2. R", nil), CodeQuestionNoAnswers, 5, []any{2}},
		{"one option", parseErr("# T\n"+q+"\n2. Q\n- a *", nil), CodeQuestionFewOptions, 5, []any{2}},
		{"eleven options", parseErr(opts(11), nil), CodeQuestionManyOptions, 2, []any{1, 10}},
		{"option without text", parseErr("# T\n1. Q\n- a *\n-\n- c", nil), CodeOptionNoText, 4, []any{1, 2}},
		{"no correct option", parseErr("# T\n1. Q\n- a\n- b", nil), CodeQuestionNoCorrect, 2, []any{1}},
		{"two correct options", parseErr("# T\n1. Q\n- a *\n- b *", nil), CodeQuestionManyCorrect, 2, []any{1}},
		{"11 answers", parseErr(answers(11, "a"), nil), CodeQuestionManyAccepted, 2, []any{1, 10}},
		{"bare =", parseErr("# T\n1. Q\n= a\n=", nil), CodeAcceptedNoText, 4, []any{1, 2}},
		{"continuation", parseErr("# T\n1. Q\n= a\n= b\n  more", nil), CodeAcceptedMultiline, 4, []any{1, 2}},
		{"201 runes", parseErr(answers(1, strings.Repeat("é", 201)), nil), CodeAcceptedTooLong, 3, []any{1, 1, 200}},
		{"question image escaping", parseErr("# T\n1. ![x](../a.png)\n- a *\n- b", nil), CodeImageOutsideQuestion, 0, []any{1, "../a.png"}},
		{"section image escaping", parseErr("# T\n## A\n![x](../a.png)\n"+q, nil), CodeImageOutsideSection, 0, []any{1, "../a.png"}},
		{"question image missing", parseErr("# T\n1. Q\n- a *\n- ![x](nope.png)", nil), CodeImageMissingQuestion, 0, []any{1, "nope.png"}},
		{"section image missing", parseErr("# T\n## A\n![x](nope.png)\n"+q, nil), CodeImageMissingSection, 0, []any{1, "nope.png"}},
		{"question image not an image", parseErr("# T\n1. ![x](a.png)\n- a *\n- b", hello), CodeImageTypeQuestion, 0, []any{1, "a.png"}},
		{"section image not an image", parseErr("# T\n## A\n![x](a.png)\n"+q, hello), CodeImageTypeSection, 0, []any{1, "a.png"}},
		{"empty upload", uploadErr(bytesOf(nil)), CodeUploadEmpty, 0, nil},
		{"zip garbage", uploadErr(bytesOf([]byte("PK\x03\x04 this is not a zip"))), CodeZipUnreadable, 0, nil},
		{"parent path", uploadErr(zipOf(md, zipEntry{name: "../evil.png", data: []byte("x")})), CodeZipUnsafePath, 0, nil},
		{"non-UTF-8 file name", uploadErr(zipOf(md, zipEntry{name: "\xe9.png", data: []byte("x")})), CodeZipNameNotUTF8, 0, nil},
		{"too many files", uploadErr(zipOf(tooMany...)), CodeZipTooManyFiles, 0, []any{200}},
		{"entry too large", uploadErr(zipOf(md, zipEntry{name: "img/big.bin", data: make([]byte, MaxZipEntryBytes+1)})), CodeZipEntryTooLarge, 0, []any{"img/big.bin"}},
		{"total too large", uploadErr(zipOf(tooBig...)), CodeZipTotalTooLarge, 0, nil},
		{"two exam texts", uploadErr(zipOf(md, zipEntry{name: "b/notes.txt", data: []byte("x")})), CodeZipTextCount, 0, []any{2}},
		{"text too large", uploadErr(bytesOf([]byte("# " + strings.Repeat("x", MaxTextBytes)))), CodeTextTooLarge, 0, nil},
		{"invalid UTF-8", uploadErr(bytesOf([]byte("# T\xff\n"))), CodeTextNotUTF8, 0, nil},
	}
}

func fileError(t *testing.T, err error) *Error {
	t.Helper()
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("error = %v (%T), want *Error", err, err)
	}
	return fe
}

func TestError_CodeLineArgs(t *testing.T) {
	for _, c := range errorCases() {
		t.Run(c.name, func(t *testing.T) {
			fe := fileError(t, c.err(t))
			if fe.Code != c.code || fe.Line != c.line || !reflect.DeepEqual(fe.Args, c.args) {
				t.Errorf("got Code %q Line %d Args %#v\nwant Code %q Line %d Args %#v", fe.Code, fe.Line, fe.Args, c.code, c.line, c.args)
			}
		})
	}
}

// editorCodes are the codes package exam raises.
var editorCodes = []Code{
	CodeEditorTitleLine, CodeEditorAnswerLine, CodeEditorSectionTitleLine, CodeEditorTooLarge,
	CodeEditorNotStorable, CodeEditorImageSize, CodeEditorImageType, CodeEditorImageHidden,
	CodeEditorImageNameTaken, CodeEditorShape, CodeEditorTitleMissing, CodeEditorFirstSection,
	CodeEditorSectionLeftEmpty,
}

func TestError_EveryCodeRaised(t *testing.T) {
	seen := map[Code]bool{}
	for _, c := range errorCases() {
		seen[c.code] = true
	}
	for _, code := range editorCodes {
		seen[code] = true
	}
	got := slices.Sorted(maps.Keys(seen))
	want := slices.Sorted(maps.Keys(English()))
	if !slices.Equal(got, want) {
		t.Errorf("codes raised or editor codes = %q\nkeys of English() = %q", got, want)
	}
}

// rendered is format with its {line} marker replaced as the plan words it,
// then filled with args.
func rendered(format string, line int, args []any) string {
	ref := ""
	switch {
	case line == 0:
	case strings.HasPrefix(format, "{line}"):
		ref = fmt.Sprintf("line %d: ", line)
	default:
		ref = fmt.Sprintf(" (line %d)", line)
	}
	return fmt.Sprintf(strings.Replace(format, "{line}", ref, 1), args...)
}

func TestError_MsgFromTable(t *testing.T) {
	table := English()
	for _, c := range errorCases() {
		t.Run(c.name, func(t *testing.T) {
			fe := fileError(t, c.err(t))
			if want := rendered(table[fe.Code], fe.Line, fe.Args); fe.Msg != want || fe.Error() != want {
				t.Errorf("Msg = %q, Error() = %q, want %q", fe.Msg, fe.Error(), want)
			}
		})
	}
}

func TestError_WithoutLine(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		want string
	}{
		{"line at the start", NewError(CodeTitleMissing, 3), `expected "# Title" as the first line`},
		{"line inside", NewError(CodeOptionNoText, 4, 1, 2), "question 1, option 2 has no text"},
		{"no line", NewError(CodeTooManySections, 0, 50), "exam has more than 50 sections"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.err.WithoutLine()
			if got.Line != 0 || got.Code != c.err.Code || !reflect.DeepEqual(got.Args, c.err.Args) || got.Msg != c.want {
				t.Errorf("WithoutLine() = %+v, want Code %q Args %#v Msg %q", got, c.err.Code, c.err.Args, c.want)
			}
		})
	}

	t.Run("empty code", func(t *testing.T) {
		e := &Error{Msg: "line 3: something"}
		if got := e.WithoutLine(); got != e || got.Msg != "line 3: something" {
			t.Errorf("WithoutLine() = %+v, want the same error unchanged", got)
		}
	})
}

func TestNewError_UnknownCodePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewError with an unknown code did not panic")
		}
	}()
	NewError("no_such_code", 0)
}
