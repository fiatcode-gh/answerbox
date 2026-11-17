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
	commaRe      = regexp.MustCompile(`\s*,\s*`)
	markerSkipRe = regexp.MustCompile("`[^`]*`|\\\\.")
)

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
}

// Parse validates the exam text of u and renders it. Image sources become
// mediaBase + "/" + Image.Name. Errors are *Error; only the first is reported.
func Parse(u Upload, mediaBase string) (Exam, error) {
	src := strings.ReplaceAll(string(u.Text), "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	ex, questions, err := scan(strings.Split(src, "\n"))
	if err != nil {
		return Exam{}, err
	}

	r := newRenderer(u, mediaBase)
	for i, q := range questions {
		parsed, err := validate(r, i+1, q)
		if err != nil {
			return Exam{}, err
		}
		ex.Questions = append(ex.Questions, parsed)
	}
	ex.Images, ex.ImagePaths, ex.Dir = r.images, r.paths, u.Dir
	return ex, nil
}

func scan(lines []string) (Exam, []*rawQuestion, error) {
	ex := Exam{Shuffle: ShuffleQuestions, ShowResult: ShowScore, Monitor: true, DurationMinutes: DefaultDurationMinutes}

	i := 0
	for i < len(lines) && isBlank(lines[i]) {
		i++
	}
	if i == len(lines) {
		return ex, nil, errorf(`line 1: expected "# Title" as the first line`)
	}
	// titleRe's \S is ASCII-only, so a title of Unicode spaces such as "\v"
	// matches but trims to nothing; it is rejected like a missing title.
	m := titleRe.FindStringSubmatch(lines[i])
	if m != nil {
		ex.Title = strings.TrimSpace(m[1])
	}
	if ex.Title == "" {
		return ex, nil, errorf(`line %d: expected "# Title" as the first line`, i+1)
	}
	if utf8.RuneCountInString(ex.Title) > MaxTitleRunes {
		return ex, nil, errorf("line %d: title is longer than %d characters", i+1, MaxTitleRunes)
	}

	headers := map[string]bool{}
	var questions []*rawQuestion
	var cur *block
	inFence, fenceLine := false, 0
	for i++; i < len(lines); i++ {
		line, n := lines[i], i+1
		// Lines are classified by their right-trimmed form, the form block.text
		// leaves at the end of a block, so "-\f" cannot hide an option there.
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)

		if len(questions) == 0 {
			if isBlank(line) {
				continue
			}
			if m := headerRe.FindStringSubmatch(line); m != nil {
				if err := setHeader(&ex, headers, m[1], m[2], n); err != nil {
					return ex, nil, err
				}
				continue
			}
			if !questionRe.MatchString(trimmed) {
				return ex, nil, errorf(`line %d: expected "shuffle:", "show_result:", "monitor:", "duration:" or question 1`, n)
			}
		}

		fence := isFence(line)
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
		if m := matchQuestion(line, trimmed); m != nil {
			num, _ := strconv.Atoi(m[1])
			if want := len(questions) + 1; num != want {
				return ex, nil, errorf("line %d: expected question %d, found question %d", n, want, num)
			}
			if num > MaxQuestions {
				return ex, nil, errorf("exam has more than %d questions", MaxQuestions)
			}
			q := &rawQuestion{block: block{line: n, first: m[2]}}
			questions = append(questions, q)
			cur = &q.block
			continue
		}
		if m := optionRe.FindStringSubmatch(trimmed); m != nil {
			q := questions[len(questions)-1]
			o := &rawOption{block: block{line: n}}
			o.first, o.correct = splitMarker(m[1])
			q.options = append(q.options, o)
			cur = &o.block
			continue
		}
		cur.cont = append(cur.cont, line)
	}
	if inFence {
		return ex, nil, errorf("line %d: code block is never closed", fenceLine)
	}
	if len(questions) == 0 {
		return ex, nil, errorf("exam has no questions")
	}
	return ex, questions, nil
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
		return errorf(`line %d: duplicate "%s" header`, n, key)
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
			return errorf(`line %d: shuffle must be "none", "questions" or "questions, options"`, n)
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
			return errorf(`line %d: show_result must be "none", "score" or "review"`, n)
		}
	case "monitor":
		switch value {
		case "on":
			ex.Monitor = true
		case "off":
			ex.Monitor = false
		default:
			return errorf(`line %d: monitor must be "on" or "off"`, n)
		}
	case "duration":
		minutes, err := strconv.Atoi(value)
		if !durationRe.MatchString(value) || err != nil || minutes < MinDurationMinutes || minutes > MaxDurationMinutes {
			return errorf("line %d: duration must be a whole number of minutes from %d to %d", n, MinDurationMinutes, MaxDurationMinutes)
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

func validate(r *renderer, k int, q *rawQuestion) (Question, error) {
	body := q.text()
	switch {
	case isBlank(body):
		return Question{}, errorf("question %d (line %d) has no text", k, q.line)
	case len(q.options) < 2:
		return Question{}, errorf("question %d (line %d) needs at least two options", k, q.line)
	case len(q.options) > MaxOptions:
		return Question{}, errorf("question %d (line %d) has more than %d options", k, q.line, MaxOptions)
	}
	texts := make([]string, len(q.options))
	correct := 0
	for m, o := range q.options {
		texts[m] = o.text()
		if isBlank(texts[m]) {
			return Question{}, errorf("question %d, option %d (line %d) has no text", k, m+1, o.line)
		}
		if o.correct {
			correct++
		}
	}
	switch {
	case correct == 0:
		return Question{}, errorf("question %d (line %d) has no correct option", k, q.line)
	case correct > 1:
		return Question{}, errorf("question %d (line %d) has more than one correct option", k, q.line)
	}

	out := Question{Source: body, Options: make([]Option, len(q.options))}
	var err error
	if out.BodyHTML, err = r.render(body, k); err != nil {
		return Question{}, err
	}
	for m, o := range q.options {
		out.Options[m].Source, out.Options[m].Correct = texts[m], o.correct
		if out.Options[m].BodyHTML, err = r.render(texts[m], k); err != nil {
			return Question{}, err
		}
	}
	return out, nil
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

func isFence(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "```")
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
