package attempt

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
)

// codeAlphabet has 30 symbols without look-alikes (no I, L, O, U, 0, 1).
const (
	codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	codeLen      = 8
	codeTries    = 5
)

// Redeem moves an in-progress attempt to a new token: the attempt's old token
// stops resolving and its code is replaced, so a code works once.
func (s *Service) Redeem(ctx context.Context, input string) (string, error) {
	code, ok := normalizeCode(input)
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

// newCode returns a resume code no in-progress attempt holds. Bytes ≥ 240
// are skipped so every symbol is equally likely.
func (s *Service) newCode(ctx context.Context, tx *sql.Tx) (string, error) {
	for range codeTries {
		code, err := randomCode(s.random)
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

func randomCode(random io.Reader) (string, error) {
	code := make([]byte, 0, codeLen)
	buf := make([]byte, 2*codeLen)
	for len(code) < codeLen {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < 240 && len(code) < codeLen {
				code = append(code, codeAlphabet[b%30])
			}
		}
	}
	return string(code), nil
}

// formatCode renders a stored code as "ABCD-EFGH".
func formatCode(code string) string {
	if len(code) != codeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// normalizeCode uppercases input and drops everything outside [A-Z0-9]. It
// reports whether the result is a well-formed code.
func normalizeCode(input string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	code := b.String()
	if len(code) != codeLen {
		return "", false
	}
	for _, r := range code {
		if !strings.ContainsRune(codeAlphabet, r) {
			return "", false
		}
	}
	return code, true
}
