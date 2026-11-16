package exam

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fiatcode-gh/go-exam/internal/shortcode"
)

const (
	codeLen   = 6
	codeTries = 5
)

// newCode returns an exam code no exam holds.
func (s *Service) newCode(ctx context.Context, tx *sql.Tx) (string, error) {
	for range codeTries {
		code, err := shortcode.Random(s.random, codeLen)
		if err != nil {
			return "", err
		}
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM exam WHERE code = ?)`, code).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("exam: could not generate a unique exam code")
}

// NormalizeCode reads an exam code as a student types it: any case, with or
// without spaces or a dash. It reports whether the result is well formed.
func NormalizeCode(input string) (string, bool) {
	return shortcode.Normalize(input, codeLen)
}

// OpenByCode returns the open exam with the code in input, typed as
// NormalizeCode accepts. A malformed, unknown or closed code is reported
// the same way: ok is false.
func (s *Service) OpenByCode(ctx context.Context, input string) (Exam, bool, error) {
	code, ok := NormalizeCode(input)
	if !ok {
		return Exam{}, false, nil
	}
	e, err := scanExam(s.db.QueryRowContext(ctx, examColumns+` WHERE code = ? AND is_open = 1`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return Exam{}, false, nil
	}
	if err != nil {
		return Exam{}, false, err
	}
	return e, true, nil
}

// AssignMissingCodes gives every exam without a code a new one and returns
// how many it assigned. Exams that have a code are never touched.
func (s *Service) AssignMissingCodes(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ids, err := missingCodeIDs(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		code, err := s.newCode(ctx, tx)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE exam SET code = ? WHERE id = ?`, code, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

// missingCodeIDs reads the ids of exams without a code. The rows are closed
// before it returns, so the single connection is free for the next statement.
func missingCodeIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM exam WHERE code IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
