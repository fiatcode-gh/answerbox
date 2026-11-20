package examfile

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
)

// Code names a file rule an *Error reports; it keys the rule's sentence in
// every language.
type Code string

// Codes raised by this package.
const (
	CodeTitleMissing         Code = "title_missing"
	CodeTitleTooLong         Code = "title_too_long"
	CodeHeaderExpected       Code = "header_expected"
	CodeSectionLate          Code = "section_late"
	CodeSectionTitleEmpty    Code = "section_title_empty"
	CodeSectionTitleTooLong  Code = "section_title_too_long"
	CodeTooManySections      Code = "too_many_sections"
	CodeQuestionNumber       Code = "question_number"
	CodeTooManyQuestions     Code = "too_many_questions"
	CodeFenceOpen            Code = "fence_open"
	CodeNoQuestions          Code = "no_questions"
	CodeSectionEmpty         Code = "section_empty"
	CodeHeaderDuplicate      Code = "header_duplicate"
	CodeShuffleValue         Code = "shuffle_value"
	CodeShowResultValue      Code = "show_result_value"
	CodeMonitorValue         Code = "monitor_value"
	CodeDurationValue        Code = "duration_value"
	CodeQuestionNoText       Code = "question_no_text"
	CodeQuestionBothKinds    Code = "question_both_kinds"
	CodeQuestionNoAnswers    Code = "question_no_answers"
	CodeQuestionFewOptions   Code = "question_few_options"
	CodeQuestionManyOptions  Code = "question_many_options"
	CodeOptionNoText         Code = "option_no_text"
	CodeQuestionNoCorrect    Code = "question_no_correct"
	CodeQuestionManyCorrect  Code = "question_many_correct"
	CodeQuestionManyAccepted Code = "question_many_accepted"
	CodeAcceptedNoText       Code = "accepted_no_text"
	CodeAcceptedMultiline    Code = "accepted_multiline"
	CodeAcceptedTooLong      Code = "accepted_too_long"
	CodeImageOutsideQuestion Code = "image_outside_question"
	CodeImageOutsideSection  Code = "image_outside_section"
	CodeImageMissingQuestion Code = "image_missing_question"
	CodeImageMissingSection  Code = "image_missing_section"
	CodeImageTypeQuestion    Code = "image_type_question"
	CodeImageTypeSection     Code = "image_type_section"
	CodeUploadEmpty          Code = "upload_empty"
	CodeZipUnreadable        Code = "zip_unreadable"
	CodeZipUnsafePath        Code = "zip_unsafe_path"
	CodeZipNameNotUTF8       Code = "zip_name_not_utf8"
	CodeZipTooManyFiles      Code = "zip_too_many_files"
	CodeZipEntryTooLarge     Code = "zip_entry_too_large"
	CodeZipTotalTooLarge     Code = "zip_total_too_large"
	CodeZipTextCount         Code = "zip_text_count"
	CodeTextTooLarge         Code = "text_too_large"
	CodeTextNotUTF8          Code = "text_not_utf8"
)

// Codes raised by package exam for the editor.
const (
	CodeEditorTitleLine        Code = "editor_title_line"
	CodeEditorAnswerLine       Code = "editor_answer_line"
	CodeEditorSectionTitleLine Code = "editor_section_title_line"
	CodeEditorTooLarge         Code = "editor_too_large"
	CodeEditorNotStorable      Code = "editor_not_storable"
	CodeEditorImageSize        Code = "editor_image_size"
	CodeEditorImageType        Code = "editor_image_type"
	CodeEditorImageHidden      Code = "editor_image_hidden"
	CodeEditorImageNameTaken   Code = "editor_image_name_taken"
	CodeEditorShape            Code = "editor_shape"
	CodeEditorTitleMissing     Code = "editor_title_missing"
	CodeEditorFirstSection     Code = "editor_first_section"
	CodeEditorSectionLeftEmpty Code = "editor_section_left_empty"
)

// lineMarker marks where a format names its line: at the start it becomes
// "line N: ", elsewhere " (line N)", and nothing for line 0.
const lineMarker = "{line}"

// english is the one English wording of every code, as fmt formats taking
// the error's Args.
var english = map[Code]string{
	CodeTitleMissing:         `{line}expected "# Title" as the first line`,
	CodeTitleTooLong:         `{line}title is longer than %d characters`,
	CodeHeaderExpected:       `{line}expected "shuffle:", "show_result:", "monitor:", "duration:", a "## " section heading or question 1`,
	CodeSectionLate:          `{line}the first section heading must come before question 1`,
	CodeSectionTitleEmpty:    `{line}section title is empty`,
	CodeSectionTitleTooLong:  `{line}section title is longer than %d characters`,
	CodeTooManySections:      `exam has more than %d sections`,
	CodeQuestionNumber:       `{line}expected question %d, found question %d`,
	CodeTooManyQuestions:     `exam has more than %d questions`,
	CodeFenceOpen:            `{line}code block is never closed`,
	CodeNoQuestions:          `exam has no questions`,
	CodeSectionEmpty:         `section %d{line} has no questions`,
	CodeHeaderDuplicate:      `{line}duplicate "%s" header`,
	CodeShuffleValue:         `{line}shuffle must be "none", "questions" or "questions, options"`,
	CodeShowResultValue:      `{line}show_result must be "none", "score" or "review"`,
	CodeMonitorValue:         `{line}monitor must be "on" or "off"`,
	CodeDurationValue:        `{line}duration must be a whole number of minutes from %d to %d`,
	CodeQuestionNoText:       `question %d{line} has no text`,
	CodeQuestionBothKinds:    `question %d{line} has both options and accepted answers; use "-" lines or "=" lines, not both`,
	CodeQuestionNoAnswers:    `question %d{line} needs options (lines starting with "-") or accepted answers (lines starting with "=")`,
	CodeQuestionFewOptions:   `question %d{line} needs at least two options`,
	CodeQuestionManyOptions:  `question %d{line} has more than %d options`,
	CodeOptionNoText:         `question %d, option %d{line} has no text`,
	CodeQuestionNoCorrect:    `question %d{line} has no correct option`,
	CodeQuestionManyCorrect:  `question %d{line} has more than one correct option`,
	CodeQuestionManyAccepted: `question %d{line} has more than %d accepted answers`,
	CodeAcceptedNoText:       `question %d, accepted answer %d{line} has no text`,
	CodeAcceptedMultiline:    `question %d, accepted answer %d{line} must be one line`,
	CodeAcceptedTooLong:      `question %d, accepted answer %d{line} is longer than %d characters`,
	CodeImageOutsideQuestion: `question %d: image "%s" must be a file inside the upload`,
	CodeImageOutsideSection:  `section %d: image "%s" must be a file inside the upload`,
	CodeImageMissingQuestion: `question %d: image "%s" not found in upload`,
	CodeImageMissingSection:  `section %d: image "%s" not found in upload`,
	CodeImageTypeQuestion:    `question %d: image "%s" is not a PNG, JPEG, GIF or WebP file`,
	CodeImageTypeSection:     `section %d: image "%s" is not a PNG, JPEG, GIF or WebP file`,
	CodeUploadEmpty:          `upload is empty`,
	CodeZipUnreadable:        `zip file could not be read`,
	CodeZipUnsafePath:        `zip contains an unsafe file path`,
	CodeZipNameNotUTF8:       `zip contains a file name that is not valid UTF-8`,
	CodeZipTooManyFiles:      `zip has more than %d files`,
	CodeZipEntryTooLarge:     `zip file "%s" is larger than 5 MB`,
	CodeZipTotalTooLarge:     `zip contents are larger than 20 MB in total`,
	CodeZipTextCount:         `zip must contain exactly one .md or .txt exam file, found %d`,
	CodeTextTooLarge:         `exam text is larger than 1 MB`,
	CodeTextNotUTF8:          `exam text is not valid UTF-8`,

	CodeEditorTitleLine:        `the title must be one line of text`,
	CodeEditorAnswerLine:       `an accepted answer must be one line of text`,
	CodeEditorSectionTitleLine: `the section title must be one line of text`,
	CodeEditorTooLarge:         `exam with its images is larger than 10 MB, the largest file that can be imported`,
	CodeEditorNotStorable:      `this text cannot be stored in an exam file`,
	CodeEditorImageSize:        `image is larger than 5 MB`,
	CodeEditorImageType:        `image is not a PNG, JPEG, GIF or WebP file`,
	CodeEditorImageHidden:      `the image line would not show at the end of the text, for example after raw HTML; change the end of the text and save again`,
	CodeEditorImageNameTaken:   `this exam already has a different image file named %s`,
	CodeEditorShape:            `question %d: the text cannot be saved as written: a line that starts with "-" would become an option, and an option that ends with "*" would become the correct one; write \- or \* instead`,
	CodeEditorTitleMissing:     `the exam needs a title`,
	CodeEditorFirstSection:     `the first section must start at question 1`,
	CodeEditorSectionLeftEmpty: `section %d would be left without questions; remove the section first`,
}

// Error is a user-facing sentence describing why an upload or exam is
// invalid, with the rule and values it was built from.
type Error struct {
	Msg  string // the English sentence
	Code Code   // the rule; keys the translation
	Line int    // 1-based line the sentence names; 0 for none
	Args []any  // the sentence's values in order, without the line: int or string
}

func (e *Error) Error() string { return e.Msg }

// NewError builds the *Error of code naming line (0 for none) with args.
// An unknown code panics.
func NewError(code Code, line int, args ...any) *Error {
	format, ok := english[code]
	if !ok {
		panic(fmt.Sprintf("examfile: unknown error code %q", code))
	}
	ref := ""
	switch {
	case line == 0:
	case strings.HasPrefix(format, lineMarker):
		ref = "line " + strconv.Itoa(line) + ": "
	default:
		ref = " (line " + strconv.Itoa(line) + ")"
	}
	return &Error{
		Msg:  fmt.Sprintf(strings.Replace(format, lineMarker, ref, 1), args...),
		Code: code,
		Line: line,
		Args: args,
	}
}

// WithoutLine returns e's sentence without its line reference. An Error
// without a Code is returned unchanged.
func (e *Error) WithoutLine() *Error {
	if e.Code == "" {
		return e
	}
	return NewError(e.Code, 0, e.Args...)
}

// English returns a copy of the English table: every code and its format,
// with {line} where the line reference goes.
func English() map[Code]string { return maps.Clone(english) }
