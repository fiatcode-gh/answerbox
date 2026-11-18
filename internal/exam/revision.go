package exam

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

const (
	summaryImported = "Imported"
	summaryReplaced = "Replaced from file"
	summaryCreated  = "Created in the editor"
)

// revisionContent is the JSON document of exam_revision.content: the source
// text and rendered HTML of every question and option, the answer key, the
// folder of the text file inside the upload and its image files.
type revisionContent struct {
	Dir       string             `json:"dir"`
	Images    map[string]string  `json:"images,omitempty"`
	Questions []revisionQuestion `json:"questions"`
	// Sections is absent for an exam without sections.
	Sections []revisionSection `json:"sections,omitempty"`
}

// revisionSection is a section's title, instructions as written and
// rendered, and the 1-based number of its first question.
type revisionSection struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	HTML   string `json:"html"`
	Start  int    `json:"start"`
}

type revisionQuestion struct {
	Source  string           `json:"source"`
	HTML    string           `json:"html"`
	Options []revisionOption `json:"options"`
}

type revisionOption struct {
	Source  string `json:"source"`
	HTML    string `json:"html"`
	Correct bool   `json:"correct"`
}

// insertRevision appends the next revision of examID inside tx and returns
// its number. It is the only writer of exam_revision.
func insertRevision(ctx context.Context, tx *sql.Tx, examID, teacherID int64, at time.Time,
	summary string, ex examfile.Exam, mediaKey string) (int, error) {
	content := revisionContent{Dir: ex.Dir, Images: ex.ImagePaths, Questions: make([]revisionQuestion, len(ex.Questions))}
	for i, q := range ex.Questions {
		rq := revisionQuestion{Source: q.Source, HTML: string(q.BodyHTML), Options: make([]revisionOption, len(q.Options))}
		for j, o := range q.Options {
			rq.Options[j] = revisionOption{Source: o.Source, HTML: string(o.BodyHTML), Correct: o.Correct}
		}
		content.Questions[i] = rq
	}
	if len(ex.Sections) > 0 {
		content.Sections = make([]revisionSection, len(ex.Sections))
		for i, s := range ex.Sections {
			content.Sections[i] = revisionSection{Title: s.Title, Source: s.Source, HTML: string(s.BodyHTML), Start: s.Start}
		}
	}
	doc, err := json.Marshal(content)
	if err != nil {
		return 0, fmt.Errorf("exam: revision content: %w", err)
	}
	var number int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number), 0) + 1 FROM exam_revision WHERE exam_id = ?`, examID).Scan(&number); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO exam_revision (exam_id, number, created_at, teacher_id, summary, title, shuffle, show_result, monitor, duration_minutes, media_key, content)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		examID, number, at.UnixMilli(), teacherID, summary, ex.Title, string(ex.Shuffle), string(ex.ShowResult),
		ex.Monitor, ex.DurationMinutes, mediaKey, string(doc)); err != nil {
		return 0, fmt.Errorf("exam: insert revision: %w", err)
	}
	return number, nil
}

const revisionColumns = `SELECT r.number, r.created_at, t.email, r.summary, r.title, r.shuffle, r.show_result, r.monitor, r.duration_minutes`

const revisionFrom = ` FROM exam_revision r JOIN teacher t ON t.id = r.teacher_id WHERE r.exam_id = ?`

// scanRevision reads the revisionColumns of one row, followed by extra.
func scanRevision(row interface{ Scan(...any) error }, extra ...any) (Revision, error) {
	var r Revision
	var created int64
	err := row.Scan(append([]any{&r.Number, &created, &r.TeacherEmail, &r.Summary, &r.Title,
		&r.Shuffle, &r.ShowResult, &r.Monitor, &r.DurationMinutes}, extra...)...)
	r.CreatedAt = time.UnixMilli(created).UTC()
	return r, err
}

// Revisions returns the exam's revisions, newest first, without questions.
// Like Questions, callers resolve the exam through Get first.
func (s *Service) Revisions(ctx context.Context, examID int64) ([]Revision, error) {
	rows, err := s.db.QueryContext(ctx, revisionColumns+revisionFrom+` ORDER BY r.number DESC`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Revision returns one revision with its questions; an unknown number is ErrNotFound.
func (s *Service) Revision(ctx context.Context, examID int64, number int) (Revision, error) {
	var doc string
	r, err := scanRevision(s.db.QueryRowContext(ctx, revisionColumns+`, r.content`+revisionFrom+` AND r.number = ?`, examID, number), &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, err
	}
	var content revisionContent
	if err := json.Unmarshal([]byte(doc), &content); err != nil {
		return Revision{}, fmt.Errorf("exam: revision %d of exam %d: %w", number, examID, err)
	}
	r.Questions = make([]Question, len(content.Questions))
	for i, q := range content.Questions {
		qq := Question{Position: i + 1, BodyHTML: template.HTML(q.HTML), Options: make([]Option, len(q.Options))}
		for j, o := range q.Options {
			qq.Options[j] = Option{Position: j + 1, BodyHTML: template.HTML(o.HTML), Correct: o.Correct}
		}
		r.Questions[i] = qq
	}
	for _, s := range content.Sections {
		if s.Start < 1 || s.Start > len(r.Questions) {
			return Revision{}, fmt.Errorf("exam: revision %d of exam %d: section %q starts at question %d of %d",
				number, examID, s.Title, s.Start, len(r.Questions))
		}
		r.Questions[s.Start-1].Section = &Section{Title: s.Title, BodyHTML: template.HTML(s.HTML)}
	}
	return r, nil
}
