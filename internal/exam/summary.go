package exam

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// SummaryKind names one clause of a revision summary the app writes.
type SummaryKind string

const (
	SummaryImported        SummaryKind = "imported"            // Imported
	SummaryReplaced        SummaryKind = "replaced"            // Replaced from file
	SummaryCreated         SummaryKind = "created"             // Created in the editor
	SummaryCopied          SummaryKind = "copied"              // Copied from <Title> (<Code>), revision <N>
	SummaryQuestionEdited  SummaryKind = "question_edited"     // Question <N> edited
	SummaryCorrectChanged  SummaryKind = "correct_changed"     // ; correct option changed
	SummaryAnswersChanged  SummaryKind = "answers_changed"     // ; accepted answers changed
	SummaryQuestionAdded   SummaryKind = "question_added"      // Question <N> added
	SummaryQuestionRemoved SummaryKind = "question_removed"    // Question <N> removed
	SummaryQuestionMoved   SummaryKind = "question_moved"      // Question <N> moved to <M>
	SummaryQuestionSection SummaryKind = "question_to_section" // Question <N> moved to section <M>
	SummarySettingsChanged SummaryKind = "settings_changed"    // Settings changed: <names>
	SummarySectionStarted  SummaryKind = "section_started"     // Section <N> started at question <M>
	SummarySectionEdited   SummaryKind = "section_edited"      // Section <N> edited
	SummarySectionRemoved  SummaryKind = "section_removed"     // Section <N> removed
)

// SummaryClause is one clause with its values; unused fields are zero.
type SummaryClause struct {
	Kind     SummaryKind
	N, M     int
	Title    string   // copied: the source exam's title
	Code     string   // copied: the source exam's code
	Settings []string // settings_changed: of "title", "shuffle", "result shown", "monitor", "length"
}

// summaryPatterns are the one-clause forms with numbers, in the order they
// are tried: "moved to section" before "moved to". Groups 1 and 2, when
// present, are N and M.
var summaryPatterns = []struct {
	kind SummaryKind
	re   *regexp.Regexp
}{
	{SummaryQuestionAdded, regexp.MustCompile(`^Question (\d+) added$`)},
	{SummaryQuestionRemoved, regexp.MustCompile(`^Question (\d+) removed$`)},
	{SummaryQuestionSection, regexp.MustCompile(`^Question (\d+) moved to section (\d+)$`)},
	{SummaryQuestionMoved, regexp.MustCompile(`^Question (\d+) moved to (\d+)$`)},
	{SummarySectionStarted, regexp.MustCompile(`^Section (\d+) started at question (\d+)$`)},
	{SummarySectionEdited, regexp.MustCompile(`^Section (\d+) edited$`)},
	{SummarySectionRemoved, regexp.MustCompile(`^Section (\d+) removed$`)},
}

var (
	// copiedPattern's greedy title keeps a title that itself holds " (".
	copiedPattern = regexp.MustCompile(`(?s)^Copied from (.*) \(([A-Z0-9]{6})\), revision (\d+)$`)
	editedPattern = regexp.MustCompile(`^Question (\d+) edited(; correct option changed|; accepted answers changed)?$`)
)

const settingsPrefix = "Settings changed: "

// summarySettings are the setting names SaveSettings writes.
var summarySettings = []string{"title", "shuffle", "result shown", "monitor", "length"}

// ParseSummary reads a stored summary. ok is false for a text the app
// does not write; the caller then shows it as stored.
func ParseSummary(stored string) (clauses []SummaryClause, ok bool) {
	switch stored {
	case summaryImported:
		return []SummaryClause{{Kind: SummaryImported}}, true
	case summaryReplaced:
		return []SummaryClause{{Kind: SummaryReplaced}}, true
	case summaryCreated:
		return []SummaryClause{{Kind: SummaryCreated}}, true
	}
	if names, found := strings.CutPrefix(stored, settingsPrefix); found {
		c := SummaryClause{Kind: SummarySettingsChanged}
		if names != "" {
			c.Settings = strings.Split(names, ", ")
			for _, name := range c.Settings {
				if !slices.Contains(summarySettings, name) {
					return nil, false
				}
			}
		}
		return []SummaryClause{c}, true
	}
	if m := copiedPattern.FindStringSubmatch(stored); m != nil {
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return nil, false
		}
		return []SummaryClause{{Kind: SummaryCopied, N: n, Title: m[1], Code: m[2]}}, true
	}
	if m := editedPattern.FindStringSubmatch(stored); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, false
		}
		clauses = []SummaryClause{{Kind: SummaryQuestionEdited, N: n}}
		switch m[2] {
		case "; correct option changed":
			clauses = append(clauses, SummaryClause{Kind: SummaryCorrectChanged})
		case "; accepted answers changed":
			clauses = append(clauses, SummaryClause{Kind: SummaryAnswersChanged})
		}
		return clauses, true
	}
	for _, p := range summaryPatterns {
		m := p.re.FindStringSubmatch(stored)
		if m == nil {
			continue
		}
		c := SummaryClause{Kind: p.kind}
		for i, dst := range []*int{&c.N, &c.M}[:len(m)-1] {
			v, err := strconv.Atoi(m[i+1])
			if err != nil {
				return nil, false
			}
			*dst = v
		}
		return []SummaryClause{c}, true
	}
	return nil, false
}
