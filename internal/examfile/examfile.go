// Package examfile reads an uploaded exam (plain text or zip) and parses its
// Markdown-like grammar into questions with rendered, safe HTML. It is pure:
// no database, no HTTP handlers and no filesystem access.
package examfile

import (
	"fmt"
	"html/template"
)

// Shuffle says which parts of an exam are shuffled per attempt.
type Shuffle string

const (
	ShuffleNone             Shuffle = "none"
	ShuffleQuestions        Shuffle = "questions"
	ShuffleQuestionsOptions Shuffle = "questions_options"
)

// ShowResult says what a student sees after an attempt ends.
type ShowResult string

const (
	ShowNone   ShowResult = "none"
	ShowScore  ShowResult = "score"
	ShowReview ShowResult = "review"
)

const (
	MaxUploadBytes   = 10 << 20
	MaxTextBytes     = 1 << 20
	MaxZipFiles      = 200
	MaxZipEntryBytes = 5 << 20
	MaxZipTotalBytes = 20 << 20
	MaxQuestions     = 500
	MaxOptions       = 10
	MaxAnswers       = 10
	MaxAnswerRunes   = 200
	MaxTitleRunes    = 200
)

const (
	MaxSections          = 50
	MaxSectionTitleRunes = 200
)

// Exam length in whole minutes, set by the "duration:" header.
const (
	MinDurationMinutes     = 5
	MaxDurationMinutes     = 240
	DefaultDurationMinutes = 60
)

// Upload is the content of an uploaded exam file.
type Upload struct {
	Text  []byte            // exam text, valid UTF-8
	Dir   string            // directory of the text file inside the zip ("." for plain text or zip root)
	Files map[string][]byte // other regular zip entries keyed by cleaned slash path; empty for plain text
}

// Exam is a parsed and validated exam.
type Exam struct {
	Title           string
	Shuffle         Shuffle
	ShowResult      ShowResult
	Monitor         bool // anti-cheat monitor; "monitor: on" by default
	DurationMinutes int  // DefaultDurationMinutes without a "duration:" header
	Questions       []Question
	// Sections are the exam's named parts in file order; nil for an exam
	// without sections. A section holds the questions from its Start up to the
	// next section's Start; the first one starts at question 1.
	Sections []Section
	Images   []Image // referenced images only, unique by Name
	Dir      string  // Upload.Dir: folder of the text file inside the upload
	// ImagePaths maps the cleaned upload path of every referenced image to
	// its Image.Name; nil without images.
	ImagePaths map[string]string
}

// Question.Source and Option.Source are the block text as written: the first
// line plus the dedented continuation lines, without an option's "*" marker.
type Question struct {
	Source   string
	BodyHTML template.HTML
	Options  []Option
	// Answers are the accepted answers of a fill-in question as written: plain
	// text, one line each, trimmed; nil for multiple choice. A question has
	// Options or Answers, never both.
	Answers []string
}

// Section is a named part of an exam. Source is its instructions as
// written: the dedented lines after the heading, without leading blank
// lines and trailing white space; "" for none.
type Section struct {
	Title    string
	Source   string
	BodyHTML template.HTML // "" without instructions
	Start    int           // 1-based number of its first question
}

type Option struct {
	Source   string
	BodyHTML template.HTML
	Correct  bool
}

// Image is an image referenced by the exam. Name is the first 32 hex
// characters of the SHA-256 of Data plus ".png", ".jpg", ".gif" or ".webp".
type Image struct {
	Name string
	Data []byte
}

// Error is a user-facing sentence describing why an upload or exam is invalid.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errorf(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}
