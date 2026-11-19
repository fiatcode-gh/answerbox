package exam

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// StartSection starts the section sec at question position (1-based) of the
// exam's revision base. An unknown question, or one that already starts a
// section, is ErrNotFound.
func (s *Service) StartSection(ctx context.Context, actor Actor, examID int64, base, position int, sec SectionSource) error {
	number := 0 // 1-based number of the new section
	return s.edit(ctx, actor, examID, base, sec.Image,
		func(next *examfile.Exam, imageName string) error {
			if !inRange(position, next) {
				return ErrNotFound
			}
			i := slices.IndexFunc(next.Sections, func(t examfile.Section) bool { return t.Start >= position })
			if i < 0 {
				i = len(next.Sections)
			} else if next.Sections[i].Start == position {
				return ErrNotFound
			}
			typed := typedSection(sec, imageName)
			typed.Start = position
			next.Sections = slices.Insert(next.Sections, i, typed)
			number = i + 1
			return nil
		},
		func(_, _ examfile.Exam) string {
			return fmt.Sprintf("Section %d started at question %d", number, position)
		})
}

// SaveSection replaces the title and instructions of section number
// (1-based) of the exam's revision base with sec; its first question stays.
func (s *Service) SaveSection(ctx context.Context, actor Actor, examID int64, base, number int, sec SectionSource) error {
	return s.edit(ctx, actor, examID, base, sec.Image,
		func(next *examfile.Exam, imageName string) error {
			if !sectionInRange(number, next) {
				return ErrNotFound
			}
			typed := typedSection(sec, imageName)
			typed.Start = next.Sections[number-1].Start
			next.Sections[number-1] = typed
			return nil
		},
		func(_, _ examfile.Exam) string {
			return fmt.Sprintf("Section %d edited", number)
		})
}

// RemoveSection removes section number (1-based) from the exam's revision
// base; its questions join the section before it. Without one before it,
// the next section starts at question 1 instead.
func (s *Service) RemoveSection(ctx context.Context, actor Actor, examID int64, base, number int) error {
	return s.edit(ctx, actor, examID, base, nil,
		func(next *examfile.Exam, _ string) error {
			if !sectionInRange(number, next) {
				return ErrNotFound
			}
			next.Sections = slices.Delete(next.Sections, number-1, number)
			switch {
			case len(next.Sections) == 0:
				next.Sections = nil
			case number == 1:
				next.Sections[0].Start = 1
			}
			return nil
		},
		func(_, _ examfile.Exam) string {
			return fmt.Sprintf("Section %d removed", number)
		})
}

func sectionInRange(number int, ex *examfile.Exam) bool {
	return number >= 1 && number <= len(ex.Sections)
}

// typedSection is sec as a section to format: the title trimmed, line ends
// normalised and, for a non-empty imageName, a reference to that image as
// the instructions' new last line. Its Start is left to the caller.
func typedSection(sec SectionSource, imageName string) examfile.Section {
	text := examfile.NormalizeNewlines(sec.Text)
	if imageName != "" {
		text = withImageLine(text, imageName)
	}
	return examfile.Section{Title: strings.TrimSpace(examfile.NormalizeNewlines(sec.Title)), Source: text}
}
