package adminauth

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
)

var (
	ErrInvalidEmail = errors.New("adminauth: not a valid email address")
	ErrNotFound     = errors.New("adminauth: no such teacher")
	ErrAdminEmail   = errors.New("adminauth: admin emails are set in the environment")
)

// Teachers returns everyone who may sign in, ordered by email.
func (s *Service) Teachers(ctx context.Context) ([]Teacher, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, name, last_login_at, (SELECT COUNT(*) FROM exam WHERE owner_id = teacher.id)
		 FROM teacher WHERE allowed = 1 ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Teacher
	for rows.Next() {
		var (
			t         Teacher
			lastLogin sql.NullInt64
		)
		if err := rows.Scan(&t.ID, &t.Email, &t.Name, &lastLogin, &t.ExamCount); err != nil {
			return nil, err
		}
		if lastLogin.Valid {
			t.LastLoginAt = time.UnixMilli(lastLogin.Int64).UTC()
		}
		t.Admin = slices.Contains(s.adminEmails, t.Email)
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTeacher puts email on the list. Adding a listed email changes nothing;
// adding a removed one restores it together with its exams.
func (s *Service) AddTeacher(ctx context.Context, email string) error {
	email, ok := normalizeEmail(email)
	if !ok {
		return ErrInvalidEmail
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO teacher (email, created_at) VALUES (?, ?) ON CONFLICT(email) DO UPDATE SET allowed = 1`,
		email, s.now().UnixMilli())
	return err
}

// RemoveTeacher takes the teacher off the list and ends their sessions.
func (s *Service) RemoveTeacher(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM teacher WHERE id = ? AND allowed = 1`, id).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if slices.Contains(s.adminEmails, email) {
		return ErrAdminEmail
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teacher SET allowed = 0 WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_session WHERE teacher_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
