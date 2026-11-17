package exam

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// File is an exam as a file a teacher can import again.
type File struct {
	Name        string // "exam-<CODE>.md" or "exam-<CODE>.zip"
	ContentType string // "text/markdown; charset=utf-8" or "application/zip"
	Data        []byte
}

// Download returns the current revision of an exam in actor's scope as an
// exam file: text alone without images, a zip with them. An exam outside the
// scope is ErrNotFound; one without a revision is ErrNoRevision.
func (s *Service) Download(ctx context.Context, actor Actor, examID int64) (File, error) {
	cur, err := s.currentUpload(ctx, actor, examID)
	if err != nil {
		return File{}, err
	}
	data, zipped, err := examfile.EncodeUpload(cur.Upload)
	if err != nil {
		return File{}, fmt.Errorf("exam: encode exam %d: %w", examID, err)
	}
	if zipped {
		return File{Name: "exam-" + cur.Code + ".zip", ContentType: "application/zip", Data: data}, nil
	}
	return File{Name: "exam-" + cur.Code + ".md", ContentType: "text/markdown; charset=utf-8", Data: data}, nil
}

// current is an exam's current revision as an upload.
type current struct {
	Upload      examfile.Upload
	Title, Code string
	Number      int
}

// currentUpload loads the current revision of an exam in actor's scope and
// its image files. Copy uses it too.
func (s *Service) currentUpload(ctx context.Context, actor Actor, examID int64) (current, error) {
	var (
		cur      current
		number   sql.NullInt64
		title    sql.NullString
		shuffle  sql.NullString
		show     sql.NullString
		monitor  sql.NullBool
		duration sql.NullInt64
		mediaKey sql.NullString
		doc      sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT exam.code, r.number, r.title, r.shuffle, r.show_result, r.monitor, r.duration_minutes, r.media_key, r.content
		FROM exam LEFT JOIN exam_revision r ON r.exam_id = exam.id
			AND r.number = (SELECT MAX(number) FROM exam_revision WHERE exam_id = exam.id)
		WHERE exam.id = ? AND `+inScope, scoped(examID, actor)...).
		Scan(&cur.Code, &number, &title, &shuffle, &show, &monitor, &duration, &mediaKey, &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return current{}, ErrNotFound
	}
	if err != nil {
		return current{}, err
	}
	if !number.Valid {
		return current{}, ErrNoRevision
	}
	var content revisionContent
	if err := json.Unmarshal([]byte(doc.String), &content); err != nil {
		return current{}, fmt.Errorf("exam: revision %d of exam %d: %w", number.Int64, examID, err)
	}
	ex := examfile.Exam{
		Title:           title.String,
		Shuffle:         examfile.Shuffle(shuffle.String),
		ShowResult:      examfile.ShowResult(show.String),
		Monitor:         monitor.Bool,
		DurationMinutes: int(duration.Int64),
		Questions:       make([]examfile.Question, len(content.Questions)),
	}
	for i, q := range content.Questions {
		eq := examfile.Question{Source: q.Source, Options: make([]examfile.Option, len(q.Options))}
		for j, o := range q.Options {
			eq.Options[j] = examfile.Option{Source: o.Source, Correct: o.Correct}
		}
		ex.Questions[i] = eq
	}
	cur.Title, cur.Number = title.String, int(number.Int64)
	cur.Upload = examfile.Upload{Text: examfile.Format(ex), Dir: content.Dir}
	if len(content.Images) > 0 {
		cur.Upload.Files = make(map[string][]byte, len(content.Images))
		for p, name := range content.Images {
			data, err := s.media.ReadFile(mediaKey.String + "/" + name)
			if err != nil {
				return current{}, fmt.Errorf("exam: image %s of exam %d: %w", p, examID, err)
			}
			cur.Upload.Files[p] = data
		}
	}
	return cur, nil
}
