package examfile

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	titleRe      = regexp.MustCompile(`^#[ \t]+(\S.*)$`)
	headerRe     = regexp.MustCompile(`^(shuffle|show_result|monitor|duration)[ \t]*:[ \t]*(.*)$`)
	durationRe   = regexp.MustCompile(`^[1-9][0-9]{0,2}$`)
	questionRe   = regexp.MustCompile(`^(\d{1,4})[.)](?:[ \t]+(.*))?$`)
	optionRe     = regexp.MustCompile(`^[ \t]*-(?:[ \t]+(.*))?$`)
	sectionRe    = regexp.MustCompile(`^##(?:[ \t]+(.*))?$`)
	answerRe     = regexp.MustCompile(`^=(?:[ \t]+(.*))?$`)
	commaRe      = regexp.MustCompile(`\s*,\s*`)
	markerSkipRe = regexp.MustCompile("`[^`]*`|\\\\.")
)

// answerIndentLimit is the indentation in columns from which an "=" line is
// text of the current block instead of an accepted answer.
const answerIndentLimit = 8

// block is one question body or option as written: the text after the
// question number or dash, then its continuation lines.
type block struct {
	line  int // 1-based line of the block's first line
	first string
	cont  []string
}

type rawOption struct {
	block
	correct bool
}

type rawQuestion struct {
	block
	options []*rawOption
	answers []*block
}

// rawSection is a section heading (block.line) and its instructions: the
// lines after it up to the next question number or heading, in block.cont.
type rawSection struct {
	block
	title string
	start int // number of its first question
}

// NormalizeNewlines turns "\r\n" and a lone "\r" into "\n".
func NormalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// Parse validates the exam text of u and renders it. Image sources become
// mediaBase + "/" + Image.Name. Errors are *Error; only the first is reported.
func Parse(u Upload, mediaBase string) (Exam, error) {
	src := NormalizeNewlines(string(u.Text))
	ex, questions, sections, err := scan(strings.Split(src, "\n"))
	if err != nil {
		return Exam{}, err
	}

	r := newRenderer(u, mediaBase)
	next := 0
	for i, q := range questions {
		// A section is rendered just before its first question, so images
		// and image errors come in file order.
		for ; next < len(sections) && sections[next].start == i+1; next++ {
			s, err := renderSection(r, next+1, sections[next])
			if err != nil {
				return Exam{}, err
			}
			ex.Sections = append(ex.Sections, s)
		}
		parsed, err := validate(r, i+1, q)
		if err != nil {
			return Exam{}, err
		}
		ex.Questions = append(ex.Questions, parsed)
	}
	ex.Images, ex.ImagePaths, ex.Dir = r.images, r.paths, u.Dir
	return ex, nil
}

// renderSection renders section k; empty instructions stay "" without
// calling the renderer.
func renderSection(r *renderer, k int, s *rawSection) (Section, error) {
	out := Section{Title: s.title, Source: s.instructions(), Start: s.start}
	if out.Source == "" {
		return out, nil
	}
	var err error
	if out.BodyHTML, err = r.render(out.Source, place{section: true, n: k}); err != nil {
		return Section{}, err
	}
	return out, nil
}

func scan(lines []string) (Exam, []*rawQuestion, []*rawSection, error) {
	ex := Exam{Shuffle: ShuffleQuestions, ShowResult: ShowScore, Monitor: true, DurationMinutes: DefaultDurationMinutes}

	i := 0
	for i < len(lines) && isBlank(lines[i]) {
		i++
	}
	if i == len(lines) {
		return ex, nil, nil, NewError(CodeTitleMissing, 1)
	}
	// titleRe's \S is ASCII-only, so a title of Unicode spaces such as "\v"
	// matches but trims to nothing; it is rejected like a missing title.
	m := titleRe.FindStringSubmatch(lines[i])
	if m != nil {
		ex.Title = strings.TrimSpace(m[1])
	}
	if ex.Title == "" {
		return ex, nil, nil, NewError(CodeTitleMissing, i+1)
	}
	if utf8.RuneCountInString(ex.Title) > MaxTitleRunes {
		return ex, nil, nil, NewError(CodeTitleTooLong, i+1, MaxTitleRunes)
	}

	headers := map[string]bool{}
	var questions []*rawQuestion
	var sections []*rawSection
	var cur *block
	inSection := false // cur is a section's instructions
	inFence, fenceLine := false, 0
	for i++; i < len(lines); i++ {
		line, n := lines[i], i+1
		// Lines are classified by their right-trimmed form, the form block.text
		// leaves at the end of a block, so "-\f" cannot hide an option there.
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)

		if len(questions) == 0 && len(sections) == 0 {
			if isBlank(line) {
				continue
			}
			if m := headerRe.FindStringSubmatch(line); m != nil {
				if err := setHeader(&ex, headers, m[1], m[2], n); err != nil {
					return ex, nil, nil, err
				}
				continue
			}
			if !questionRe.MatchString(trimmed) && !sectionRe.MatchString(trimmed) {
				return ex, nil, nil, NewError(CodeHeaderExpected, n)
			}
		}

		fence := IsFence(line)
		if inFence || fence {
			cur.cont = append(cur.cont, line)
			if fence {
				inFence = !inFence
				if inFence {
					fenceLine = n
				}
			}
			continue
		}
		if m := sectionRe.FindStringSubmatch(trimmed); m != nil {
			if len(sections) == 0 && len(questions) > 0 {
				return ex, nil, nil, NewError(CodeSectionLate, n)
			}
			if err := lastSectionHasQuestion(sections, len(questions)); err != nil {
				return ex, nil, nil, err
			}
			title := strings.TrimSpace(m[1])
			if title == "" {
				return ex, nil, nil, NewError(CodeSectionTitleEmpty, n)
			}
			if utf8.RuneCountInString(title) > MaxSectionTitleRunes {
				return ex, nil, nil, NewError(CodeSectionTitleTooLong, n, MaxSectionTitleRunes)
			}
			if len(sections) == MaxSections {
				return ex, nil, nil, NewError(CodeTooManySections, 0, MaxSections)
			}
			s := &rawSection{block: block{line: n}, title: title, start: len(questions) + 1}
			sections = append(sections, s)
			cur, inSection = &s.block, true
			continue
		}
		if m := matchQuestion(line, trimmed); m != nil {
			num, _ := strconv.Atoi(m[1])
			if want := len(questions) + 1; num != want {
				return ex, nil, nil, NewError(CodeQuestionNumber, n, want, num)
			}
			if num > MaxQuestions {
				return ex, nil, nil, NewError(CodeTooManyQuestions, 0, MaxQuestions)
			}
			q := &rawQuestion{block: block{line: n, first: m[2]}}
			questions = append(questions, q)
			cur, inSection = &q.block, false
			continue
		}
		// In a section's instructions a line like an option is text.
		if m := optionRe.FindStringSubmatch(trimmed); m != nil && !inSection {
			q := questions[len(questions)-1]
			o := &rawOption{block: block{line: n}}
			o.first, o.correct = splitMarker(m[1])
			q.options = append(q.options, o)
			cur = &o.block
			continue
		}
		// In a section's instructions a line like an accepted answer is text.
		if text, ok := answerLine(trimmed); ok && !inSection {
			q := questions[len(questions)-1]
			a := &block{line: n, first: text}
			q.answers = append(q.answers, a)
			cur = a
			continue
		}
		cur.cont = append(cur.cont, line)
	}
	if inFence {
		return ex, nil, nil, NewError(CodeFenceOpen, fenceLine)
	}
	if err := lastSectionHasQuestion(sections, len(questions)); err != nil {
		return ex, nil, nil, err
	}
	if len(questions) == 0 {
		return ex, nil, nil, NewError(CodeNoQuestions, 0)
	}
	return ex, questions, sections, nil
}

// lastSectionHasQuestion refuses the last of sections when no question came
// after its heading; questions is the number of questions read so far.
func lastSectionHasQuestion(sections []*rawSection, questions int) error {
	if len(sections) == 0 {
		return nil
	}
	if last := sections[len(sections)-1]; last.start > questions {
		return NewError(CodeSectionEmpty, last.line, len(sections))
	}
	return nil
}

// instructions is the dedented instructions text without leading empty
// lines and trailing white space; the first kept line keeps its own
// indentation.
func (s *rawSection) instructions() string {
	lines := dedent(s.cont)
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	return strings.TrimRightFunc(strings.Join(lines, "\n"), unicode.IsSpace)
}

// matchQuestion matches questionRe against trimmed, the right-trimmed line,
// taking the first-line text from line itself when it matches too so that
// trailing whitespace before continuation lines is kept as written.
func matchQuestion(line, trimmed string) []string {
	if m := questionRe.FindStringSubmatch(line); m != nil {
		return m
	}
	return questionRe.FindStringSubmatch(trimmed)
}

func setHeader(ex *Exam, seen map[string]bool, key, raw string, n int) error {
	if seen[key] {
		return NewError(CodeHeaderDuplicate, n, key)
	}
	seen[key] = true
	value := strings.TrimSpace(commaRe.ReplaceAllString(strings.ToLower(raw), ","))
	switch key {
	case "shuffle":
		switch value {
		case "none":
			ex.Shuffle = ShuffleNone
		case "questions":
			ex.Shuffle = ShuffleQuestions
		case "questions,options":
			ex.Shuffle = ShuffleQuestionsOptions
		default:
			return NewError(CodeShuffleValue, n)
		}
	case "show_result":
		switch value {
		case "none":
			ex.ShowResult = ShowNone
		case "score":
			ex.ShowResult = ShowScore
		case "review":
			ex.ShowResult = ShowReview
		default:
			return NewError(CodeShowResultValue, n)
		}
	case "monitor":
		switch value {
		case "on":
			ex.Monitor = true
		case "off":
			ex.Monitor = false
		default:
			return NewError(CodeMonitorValue, n)
		}
	case "duration":
		minutes, err := strconv.Atoi(value)
		if !durationRe.MatchString(value) || err != nil || minutes < MinDurationMinutes || minutes > MaxDurationMinutes {
			return NewError(CodeDurationValue, n, MinDurationMinutes, MaxDurationMinutes)
		}
		ex.DurationMinutes = minutes
	}
	return nil
}

// splitMarker removes a trailing correct-answer "*" from an option's
// right-trimmed first line. The "*" counts when it is not escaped and either
// stands alone or after whitespace, or is the odd one out among the line's
// asterisks outside code spans and escapes (so "**bold***" is marked but
// "*italic*" is not).
func splitMarker(s string) (string, bool) {
	rest, ok := strings.CutSuffix(s, "*")
	if !ok || strings.HasSuffix(rest, `\`) {
		return s, false
	}
	marked := rest == "" ||
		strings.HasSuffix(rest, " ") || strings.HasSuffix(rest, "\t") ||
		strings.Count(markerSkipRe.ReplaceAllString(s, ""), "*")%2 == 1
	if !marked {
		return s, false
	}
	return strings.TrimRight(rest, " \t"), true
}

// answerLine reports whether trimmed, a right-trimmed line, is an accepted
// answer line and returns its trimmed text.
func answerLine(trimmed string) (text string, ok bool) {
	w, rest := indent(trimmed)
	if w >= answerIndentLimit {
		return "", false
	}
	m := answerRe.FindStringSubmatch(rest)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

func validate(r *renderer, k int, q *rawQuestion) (Question, error) {
	body := q.text()
	switch {
	case isBlank(body):
		return Question{}, NewError(CodeQuestionNoText, q.line, k)
	case len(q.options) > 0 && len(q.answers) > 0:
		return Question{}, NewError(CodeQuestionBothKinds, q.line, k)
	case len(q.options) == 0 && len(q.answers) == 0:
		return Question{}, NewError(CodeQuestionNoAnswers, q.line, k)
	case len(q.answers) > 0:
		return validateFillIn(r, k, q, body)
	case len(q.options) < 2:
		return Question{}, NewError(CodeQuestionFewOptions, q.line, k)
	case len(q.options) > MaxOptions:
		return Question{}, NewError(CodeQuestionManyOptions, q.line, k, MaxOptions)
	}
	texts := make([]string, len(q.options))
	correct := 0
	for m, o := range q.options {
		texts[m] = o.text()
		if isBlank(texts[m]) {
			return Question{}, NewError(CodeOptionNoText, o.line, k, m+1)
		}
		if o.correct {
			correct++
		}
	}
	switch {
	case correct == 0:
		return Question{}, NewError(CodeQuestionNoCorrect, q.line, k)
	case correct > 1:
		return Question{}, NewError(CodeQuestionManyCorrect, q.line, k)
	}

	out := Question{Source: body, Options: make([]Option, len(q.options))}
	at := place{n: k}
	var err error
	if out.BodyHTML, err = r.render(body, at); err != nil {
		return Question{}, err
	}
	for m, o := range q.options {
		out.Options[m].Source, out.Options[m].Correct = texts[m], o.correct
		if out.Options[m].BodyHTML, err = r.render(texts[m], at); err != nil {
			return Question{}, err
		}
	}
	return out, nil
}

// validateFillIn checks the accepted answers of q, whose text is body.
func validateFillIn(r *renderer, k int, q *rawQuestion, body string) (Question, error) {
	if len(q.answers) > MaxAnswers {
		return Question{}, NewError(CodeQuestionManyAccepted, q.line, k, MaxAnswers)
	}
	texts := make([]string, len(q.answers))
	for m, a := range q.answers {
		texts[m] = a.text()
		switch {
		case isBlank(texts[m]):
			return Question{}, NewError(CodeAcceptedNoText, a.line, k, m+1)
		case strings.Contains(texts[m], "\n"):
			return Question{}, NewError(CodeAcceptedMultiline, a.line, k, m+1)
		case utf8.RuneCountInString(texts[m]) > MaxAnswerRunes:
			return Question{}, NewError(CodeAcceptedTooLong, a.line, k, m+1, MaxAnswerRunes)
		}
	}
	html, err := r.render(body, place{n: k})
	if err != nil {
		return Question{}, err
	}
	return Question{Source: body, BodyHTML: html, Answers: texts}, nil
}

// text joins the first line with the dedented continuation lines and drops
// trailing whitespace, including trailing blank lines.
func (b *block) text() string {
	parts := append([]string{b.first}, dedent(b.cont)...)
	return strings.TrimRightFunc(strings.Join(parts, "\n"), unicode.IsSpace)
}

// dedent removes the indentation common to all non-blank lines; leading
// tabs count as four spaces. Blank lines become empty.
func dedent(lines []string) []string {
	common := -1
	for _, l := range lines {
		if isBlank(l) {
			continue
		}
		if w, _ := indent(l); common < 0 || w < common {
			common = w
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if isBlank(l) {
			continue
		}
		w, rest := indent(l)
		out[i] = strings.Repeat(" ", w-common) + rest
	}
	return out
}

func indent(l string) (width int, rest string) {
	for i := range len(l) {
		switch l[i] {
		case ' ':
			width++
		case '\t':
			width += 4
		default:
			return width, l[i:]
		}
	}
	return width, ""
}

// IsFence reports whether line opens or closes a fenced code block: its
// first non-blank characters are three backticks.
func IsFence(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "```")
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
