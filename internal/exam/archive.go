package exam

import (
	"context"
	"database/sql"
	"errors"
)

// Archive closes examID in actor's scope, removes its schedule and marks it
// archived, in one statement. Archiving an archived exam keeps the first
// date. Attempts are not touched.
func (s *Service) Archive(ctx context.Context, actor Actor, examID int64) error {
	args := append([]any{s.now().UnixMilli()}, scoped(examID, actor)...)
	res, err := s.db.ExecContext(ctx,
		`UPDATE exam SET is_open = 0, opens_at = NULL, closes_at = NULL, archived_at = COALESCE(archived_at, ?) WHERE id = ? AND `+inScope, args...)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// Unarchive clears the archive mark of examID in actor's scope. The exam
// stays closed without a schedule; on an exam that is not archived it is a
// no-op.
func (s *Service) Unarchive(ctx context.Context, actor Actor, examID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE exam SET archived_at = NULL WHERE id = ? AND `+inScope, scoped(examID, actor)...)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// requireUnarchived returns ErrNotFound when examID is outside actor's scope
// and ErrArchived when it is archived. It reads through tx only, so the
// caller's write sees the same state.
func requireUnarchived(ctx context.Context, tx *sql.Tx, actor Actor, examID int64) error {
	var archived bool
	err := tx.QueryRowContext(ctx, `SELECT archived_at IS NOT NULL FROM exam WHERE id = ? AND `+inScope, scoped(examID, actor)...).Scan(&archived)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived {
		return ErrArchived
	}
	return nil
}
