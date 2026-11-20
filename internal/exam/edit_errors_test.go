package exam

import (
	"errors"
	"reflect"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

func TestEditorError_Codes(t *testing.T) {
	parse := func(text string) error {
		_, err := examfile.Parse(examfile.Upload{Text: []byte(text), Dir: "."}, "/media/k")
		if err == nil {
			t.Fatalf("Parse(%q) accepted the text", text)
		}
		return err
	}
	_, uploadErr := examfile.ReadUpload(nil)
	shape := examfile.Exam{
		Title: "T", Shuffle: examfile.ShuffleNone, ShowResult: examfile.ShowScore, Monitor: true,
		DurationMinutes: examfile.DefaultDurationMinutes,
		Questions: []examfile.Question{
			{Source: "Q", Options: []examfile.Option{{Source: "a", Correct: true}, {Source: "b"}}},
			{Source: "Which?\n- not this", Options: []examfile.Option{{Source: "c", Correct: true}, {Source: "d"}}},
		},
	}
	_, shapeErr := parseEdited(shape, ".", map[string][]byte{}, "k")

	cases := []struct {
		name string
		err  error
		code examfile.Code
		args []any
		msg  string
	}{
		{"missing title", editorError(parse("\n\nHello\n1. Q\n- a *\n- b")), examfile.CodeEditorTitleMissing, nil,
			"the exam needs a title"},
		{"section after question 1", editorError(parse("# T\n1. Q\n- a *\n- b\n## Late\n2. Q\n- a *\n- b")), examfile.CodeEditorFirstSection, nil,
			"the first section must start at question 1"},
		{"section left empty", editorError(parse("# T\n## A\n1. Q\n- a *\n- b\n## B\n")), examfile.CodeEditorSectionLeftEmpty, []any{2},
			"section 2 would be left without questions; remove the section first"},
		{"plain refusal", editorError(parse("# T\n1. Q\n- a *\n-\n- c")), examfile.CodeOptionNoText, []any{1, 2},
			"question 1, option 2 has no text"},
		{"refusal without a line", editorError(uploadErr), examfile.CodeUploadEmpty, nil, "upload is empty"},
		{"shape", shapeErr, examfile.CodeEditorShape, []any{2},
			`question 2: the text cannot be saved as written: a line that starts with "-" would become an option, ` +
				`and an option that ends with "*" would become the correct one; write \- or \* instead`},
		{"title line", errTitleLine, examfile.CodeEditorTitleLine, nil, "the title must be one line of text"},
		{"answer line", errAnswerLine, examfile.CodeEditorAnswerLine, nil, "an accepted answer must be one line of text"},
		{"section title line", errSectionTitleLine, examfile.CodeEditorSectionTitleLine, nil, "the section title must be one line of text"},
		{"too large", errTooLarge, examfile.CodeEditorTooLarge, nil,
			"exam with its images is larger than 10 MB, the largest file that can be imported"},
		{"not storable", errNotStorable, examfile.CodeEditorNotStorable, nil, "this text cannot be stored in an exam file"},
		{"image size", errImageSize, examfile.CodeEditorImageSize, nil, "image is larger than 5 MB"},
		{"image type", errImageType, examfile.CodeEditorImageType, nil, "image is not a PNG, JPEG, GIF or WebP file"},
		{"image hidden", errImageHidden, examfile.CodeEditorImageHidden, nil,
			"the image line would not show at the end of the text, for example after raw HTML; change the end of the text and save again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var fe *examfile.Error
			if !errors.As(c.err, &fe) {
				t.Fatalf("error = %v (%T), want *examfile.Error", c.err, c.err)
			}
			if fe.Code != c.code || fe.Line != 0 || !reflect.DeepEqual(fe.Args, c.args) || fe.Msg != c.msg {
				t.Errorf("got Code %q Line %d Args %#v Msg %q\nwant Code %q Line 0 Args %#v Msg %q",
					fe.Code, fe.Line, fe.Args, fe.Msg, c.code, c.args, c.msg)
			}
		})
	}

	t.Run("other errors unchanged", func(t *testing.T) {
		other := errors.New("disk full")
		if got := editorError(other); got != other {
			t.Errorf("editorError(%v) = %v, want it unchanged", other, got)
		}
	})
}
