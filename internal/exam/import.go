package exam

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// Import validates an uploaded exam file, stores its images and inserts it
// closed, owned by actor (also when actor is an admin), with revision 1
// "Imported" recording its content. Invalid uploads return *examfile.Error
// and store nothing.
func (s *Service) Import(ctx context.Context, actor Actor, data []byte) (int64, error) {
	ex, key, err := s.prepare(data)
	if err != nil {
		return 0, err
	}
	id, err := s.insertExam(ctx, actor.TeacherID, summaryImported, ex, key)
	if err != nil {
		s.media.RemoveAll(key)
		return 0, err
	}
	return id, nil
}

func (s *Service) insertExam(ctx context.Context, ownerID int64, summary string, ex examfile.Exam, key string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	code, err := s.newCode(ctx, tx)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO exam (title, shuffle, show_result, monitor, duration_minutes, is_open, media_key, created_at, owner_id, code) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		ex.Title, string(ex.Shuffle), string(ex.ShowResult), ex.Monitor, ex.DurationMinutes, key, s.now().UnixMilli(), ownerID, code)
	if err != nil {
		return 0, fmt.Errorf("exam: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := insertRevision(ctx, tx, id, ownerID, s.now(), summary, ex, key); err != nil {
		return 0, err
	}
	if err := insertQuestions(ctx, tx, id, ex); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// Replace swaps the content, title and header settings (including the exam
// length) of an exam in actor's scope that has no attempts, recording the
// new content as the next revision "Replaced from file". ID, code, owner,
// open state, schedule and creation time are kept, and so are the images of
// earlier revisions.
func (s *Service) Replace(ctx context.Context, actor Actor, examID int64, data []byte) error {
	ex, key, err := s.prepare(data)
	if err != nil {
		return err
	}
	if err := s.replaceContent(ctx, actor, examID, 0, summaryReplaced, ex, key); err != nil {
		s.media.RemoveAll(key)
		return err
	}
	return nil
}

// replaceContent stores ex as the next revision of an exam in actor's scope
// and makes it the exam's content, in one transaction. A base above 0 must
// be the current revision's number, else ErrStale; Replace passes 0.
func (s *Service) replaceContent(ctx context.Context, actor Actor, examID int64, base int, summary string, ex examfile.Exam, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var (
		frozen bool
		number int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM attempt WHERE exam_id = exam.id),
			(SELECT COALESCE(MAX(number), 0) FROM exam_revision WHERE exam_id = exam.id)
		FROM exam WHERE id = ? AND `+inScope,
		scoped(examID, actor)...).Scan(&frozen, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if frozen {
		return ErrFrozen
	}
	if base > 0 && base != number {
		return ErrStale
	}
	// The revision comes first: exam_content_matches_revision refuses an
	// exam update that does not match the latest revision.
	if _, err := insertRevision(ctx, tx, examID, actor.TeacherID, s.now(), summary, ex, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM question WHERE exam_id = ?`, examID); err != nil {
		return fmt.Errorf("exam: delete questions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE exam SET title = ?, shuffle = ?, show_result = ?, monitor = ?, duration_minutes = ?, media_key = ? WHERE id = ?`,
		ex.Title, string(ex.Shuffle), string(ex.ShowResult), ex.Monitor, ex.DurationMinutes, key, examID); err != nil {
		return fmt.Errorf("exam: update: %w", err)
	}
	if err := insertQuestions(ctx, tx, examID, ex); err != nil {
		return err
	}
	return tx.Commit()
}

// prepare validates the upload, then stores it. Nothing is written unless
// the whole upload is valid.
func (s *Service) prepare(data []byte) (examfile.Exam, string, error) {
	u, err := examfile.ReadUpload(data)
	if err != nil {
		return examfile.Exam{}, "", err
	}
	return s.store(u)
}

// store renders u under a fresh media key and writes its images. Nothing is
// written unless u is valid.
func (s *Service) store(u examfile.Upload) (examfile.Exam, string, error) {
	key, err := s.newMediaKey()
	if err != nil {
		return examfile.Exam{}, "", err
	}
	ex, err := examfile.Parse(u, "/media/"+key)
	if err != nil {
		return examfile.Exam{}, "", err
	}
	if len(ex.Images) == 0 {
		return ex, key, nil
	}
	if _, err := s.writeImages(key, ex.Images); err != nil {
		s.media.RemoveAll(key)
		return examfile.Exam{}, "", err
	}
	return ex, key, nil
}

// newMediaKey draws a fresh media folder name: 16 random bytes in hex.
func (s *Service) newMediaKey() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return "", fmt.Errorf("exam: media key: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// writeImages writes every image the media folder key does not hold yet,
// creating the folder when it is missing, and returns the names it wrote.
// A failed write leaves no partial file behind.
func (s *Service) writeImages(key string, images []examfile.Image) (created []string, err error) {
	if err := s.media.Mkdir(key, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("exam: media folder: %w", err)
	}
	for _, img := range images {
		p := key + "/" + img.Name
		_, err := s.media.Stat(p)
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return created, fmt.Errorf("exam: image %s: %w", p, err)
		}
		if err := s.media.WriteFile(p, img.Data, 0o640); err != nil {
			s.media.Remove(p)
			return created, fmt.Errorf("exam: write image: %w", err)
		}
		created = append(created, img.Name)
	}
	return created, nil
}

// insertQuestions inserts the questions and options of ex; a question that
// starts a section carries its title and HTML, every other one NULL.
func insertQuestions(ctx context.Context, tx *sql.Tx, examID int64, ex examfile.Exam) error {
	next := 0 // ex.Sections are in Start order
	for i, q := range ex.Questions {
		var title, html sql.NullString
		if next < len(ex.Sections) && ex.Sections[next].Start == i+1 {
			s := ex.Sections[next]
			title = sql.NullString{String: s.Title, Valid: true}
			html = sql.NullString{String: string(s.BodyHTML), Valid: true}
			next++
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO question (exam_id, position, body_html, section_title, section_html) VALUES (?, ?, ?, ?, ?)`,
			examID, i+1, string(q.BodyHTML), title, html)
		if err != nil {
			return fmt.Errorf("exam: insert question: %w", err)
		}
		qid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for j, o := range q.Options {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO question_option (question_id, position, body_html, is_correct) VALUES (?, ?, ?, ?)`,
				qid, j+1, string(o.BodyHTML), o.Correct); err != nil {
				return fmt.Errorf("exam: insert option: %w", err)
			}
		}
	}
	return nil
}
