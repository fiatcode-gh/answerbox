package attempt

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fiatcode-gh/answerbox/internal/shortcode"
)

const (
	codeLen   = 8
	codeTries = 5
)

// Redeem moves an in-progress attempt to a new token: the attempt's old token
// stops resolving and its code is replaced, so a code works once.
func (s *Service) Redeem(ctx context.Context, input string) (string, error) {
	code, ok := shortcode.Normalize(input, codeLen)
	if !ok {
		return "", ErrBadCode
	}
	tx, _, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM attempt WHERE resume_code = ? AND status = 'in_progress'`, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBadCode
	}
	if err != nil {
		return "", err
	}
	token, hash, err := s.newToken()
	if err != nil {
		return "", err
	}
	next, err := s.newCode(ctx, tx)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempt SET token_hash = ?, resume_code = ? WHERE id = ?`,
		hash, next, id); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// newCode returns a resume code no in-progress attempt holds.
func (s *Service) newCode(ctx context.Context, tx *sql.Tx) (string, error) {
	for range codeTries {
		code, err := shortcode.Random(s.random, codeLen)
		if err != nil {
			return "", err
		}
		var taken bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM attempt WHERE resume_code = ?)`, code).Scan(&taken)
		if err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("attempt: could not generate a unique resume code")
}

// formatCode renders a stored code as "ABCD-EFGH".
func formatCode(code string) string {
	if len(code) != codeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}
