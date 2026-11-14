package exam

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// Import validates an uploaded exam file, stores its images and inserts it
// closed. Invalid uploads return *examfile.Error and store nothing.
func (s *Service) Import(ctx context.Context, data []byte) (int64, error) {
	ex, key, err := s.prepare(data)
	if err != nil {
		return 0, err
	}
	id, err := s.insertExam(ctx, ex, key)
	if err != nil {
		s.media.RemoveAll(key)
		return 0, err
	}
	return id, nil
}

func (s *Service) insertExam(ctx context.Context, ex examfile.Exam, key string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO exam (title, shuffle, show_result, is_open, media_key, created_at) VALUES (?, ?, ?, 0, ?, ?)`,
		ex.Title, string(ex.Shuffle), string(ex.ShowResult), key, s.now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("exam: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertQuestions(ctx, tx, id, ex.Questions); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// Replace swaps the content, title and header settings of an exam that has
// no attempts. ID, open state and creation time are kept.
func (s *Service) Replace(ctx context.Context, examID int64, data []byte) error {
	ex, key, err := s.prepare(data)
	if err != nil {
		return err
	}
	oldKey, err := s.replaceExam(ctx, examID, ex, key)
	if err != nil {
		s.media.RemoveAll(key)
		return err
	}
	// The replace has succeeded; a leftover old folder is harmless.
	s.media.RemoveAll(oldKey)
	return nil
}

func (s *Service) replaceExam(ctx context.Context, examID int64, ex examfile.Exam, key string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var oldKey string
	var frozen bool
	err = tx.QueryRowContext(ctx,
		`SELECT media_key, EXISTS(SELECT 1 FROM attempt WHERE exam_id = exam.id) FROM exam WHERE id = ?`,
		examID).Scan(&oldKey, &frozen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if frozen {
		return "", ErrFrozen
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM question WHERE exam_id = ?`, examID); err != nil {
		return "", fmt.Errorf("exam: delete questions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE exam SET title = ?, shuffle = ?, show_result = ?, media_key = ? WHERE id = ?`,
		ex.Title, string(ex.Shuffle), string(ex.ShowResult), key, examID); err != nil {
		return "", fmt.Errorf("exam: update: %w", err)
	}
	if err := insertQuestions(ctx, tx, examID, ex.Questions); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return oldKey, nil
}

// prepare validates the upload, then writes its images under a fresh media
// key. Nothing is written unless the whole upload is valid.
func (s *Service) prepare(data []byte) (examfile.Exam, string, error) {
	u, err := examfile.ReadUpload(data)
	if err != nil {
		return examfile.Exam{}, "", err
	}
	var raw [16]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return examfile.Exam{}, "", fmt.Errorf("exam: media key: %w", err)
	}
	key := hex.EncodeToString(raw[:])
	ex, err := examfile.Parse(u, "/media/"+key)
	if err != nil {
		return examfile.Exam{}, "", err
	}
	if len(ex.Images) == 0 {
		return ex, key, nil
	}
	if err := s.writeImages(key, ex.Images); err != nil {
		s.media.RemoveAll(key)
		return examfile.Exam{}, "", err
	}
	return ex, key, nil
}

func (s *Service) writeImages(key string, images []examfile.Image) error {
	if err := s.media.Mkdir(key, 0o750); err != nil {
		return fmt.Errorf("exam: media folder: %w", err)
	}
	for _, img := range images {
		if err := s.media.WriteFile(key+"/"+img.Name, img.Data, 0o640); err != nil {
			return fmt.Errorf("exam: write image: %w", err)
		}
	}
	return nil
}

func insertQuestions(ctx context.Context, tx *sql.Tx, examID int64, questions []examfile.Question) error {
	for i, q := range questions {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO question (exam_id, position, body_html) VALUES (?, ?, ?)`,
			examID, i+1, string(q.BodyHTML))
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
