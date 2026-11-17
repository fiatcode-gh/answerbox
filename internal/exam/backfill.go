package exam

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// Unconverted is an exam BackfillRevisions left without a revision.
type Unconverted struct {
	ExamID int64
	Err    error
}

// BackfillRevisions gives every exam without a revision its revision 1,
// rebuilt from the stored HTML, and sets its attempts to revision 1. Exams
// that have a revision are never touched. An exam whose stored content cannot
// be rebuilt exactly is skipped and returned; err is for database failures.
func (s *Service) BackfillRevisions(ctx context.Context) (created int, skipped []Unconverted, err error) {
	ids, err := examIDs(ctx, s.db,
		`SELECT id, title FROM exam WHERE NOT EXISTS (SELECT 1 FROM exam_revision WHERE exam_id = exam.id) ORDER BY id`)
	if err != nil {
		return 0, nil, err
	}
	media := s.media.FS()
	for _, e := range ids {
		ex, key, err := rebuildStored(ctx, s.db, media, e.id)
		if re := (*examfile.RebuildError)(nil); errors.As(err, &re) {
			skipped = append(skipped, Unconverted{ExamID: e.id, Err: err})
			continue
		}
		if err != nil {
			return created, skipped, err
		}
		if err := s.backfillOne(ctx, e.id, ex, key); err != nil {
			return created, skipped, err
		}
		created++
	}
	return created, skipped, nil
}

// backfillOne stores ex as revision 1 "Imported" of examID, dated at the
// exam's creation by its owner, and puts its attempts on that revision.
func (s *Service) backfillOne(ctx context.Context, examID int64, ex examfile.Exam, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var createdAt, ownerID int64
	if err := tx.QueryRowContext(ctx, `SELECT created_at, owner_id FROM exam WHERE id = ?`, examID).Scan(&createdAt, &ownerID); err != nil {
		return fmt.Errorf("exam %d: %w", examID, err)
	}
	if _, err := insertRevision(ctx, tx, examID, ownerID, time.UnixMilli(createdAt), summaryImported, ex, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempt SET revision = 1 WHERE exam_id = ? AND revision IS NULL`, examID); err != nil {
		return fmt.Errorf("exam: attempts of exam %d: %w", examID, err)
	}
	return tx.Commit()
}

// RebuildResult is the outcome of rebuilding one exam's source text.
type RebuildResult struct {
	ExamID             int64
	Title              string
	Questions, Options int
	Err                error // nil when the rebuilt text renders back exactly
}

// CheckRebuild rebuilds every exam readable through q with the images under
// media and reports each outcome. It writes nothing. Questions and Options
// are counted only for an exam that rebuilds exactly.
func CheckRebuild(ctx context.Context, q Queryer, media fs.FS) ([]RebuildResult, error) {
	ids, err := examIDs(ctx, q, `SELECT id, title FROM exam ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]RebuildResult, 0, len(ids))
	for _, e := range ids {
		r := RebuildResult{ExamID: e.id, Title: e.title}
		ex, _, err := rebuildStored(ctx, q, media, e.id)
		if re := (*examfile.RebuildError)(nil); errors.As(err, &re) {
			r.Err = err
		} else if err != nil {
			return nil, err
		}
		r.Questions = len(ex.Questions)
		for _, qq := range ex.Questions {
			r.Options += len(qq.Options)
		}
		out = append(out, r)
	}
	return out, nil
}

type examRef struct {
	id    int64
	title string
}

// examIDs runs query, which selects id and title, and reads every row before
// returning, so the caller may query again on a single connection.
func examIDs(ctx context.Context, q Queryer, query string) ([]examRef, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []examRef
	for rows.Next() {
		var e examRef
		if err := rows.Scan(&e.id, &e.title); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// rebuildStored reads exam examID as the database and media hold it and
// returns it with rebuilt source text, plus its media key. An error that is a
// *examfile.RebuildError means this exam cannot be rebuilt exactly; any other
// error is a database failure.
func rebuildStored(ctx context.Context, q Queryer, media fs.FS, examID int64) (ex examfile.Exam, mediaKey string, err error) {
	var stored examfile.Exam
	var key string
	if err := q.QueryRowContext(ctx,
		`SELECT title, shuffle, show_result, monitor, duration_minutes, media_key FROM exam WHERE id = ?`, examID).
		Scan(&stored.Title, &stored.Shuffle, &stored.ShowResult, &stored.Monitor, &stored.DurationMinutes, &key); err != nil {
		return examfile.Exam{}, "", fmt.Errorf("exam %d: %w", examID, err)
	}
	questions, err := LoadQuestions(ctx, q, examID)
	if err != nil {
		return examfile.Exam{}, "", err
	}
	stored.Questions = make([]examfile.Question, len(questions))
	for i, qq := range questions {
		sq := examfile.Question{BodyHTML: qq.BodyHTML, Options: make([]examfile.Option, len(qq.Options))}
		for j, o := range qq.Options {
			sq.Options[j] = examfile.Option{BodyHTML: o.BodyHTML, Correct: o.Correct}
		}
		stored.Questions[i] = sq
	}
	if stored.Images, err = readImages(media, key); err != nil {
		return examfile.Exam{}, "", err
	}
	ex, err = examfile.Rebuild(stored, "/media/"+key)
	if err != nil {
		return examfile.Exam{}, "", err
	}
	return ex, key, nil
}

// readImages returns every regular file of the media folder key; a folder
// that does not exist holds no images.
func readImages(media fs.FS, key string) ([]examfile.Image, error) {
	unreadable := &examfile.RebuildError{Reason: "image files could not be read"}
	entries, err := fs.ReadDir(media, key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unreadable
	}
	var images []examfile.Image
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := fs.ReadFile(media, key+"/"+e.Name())
		if err != nil {
			return nil, unreadable
		}
		images = append(images, examfile.Image{Name: e.Name(), Data: data})
	}
	return images, nil
}
