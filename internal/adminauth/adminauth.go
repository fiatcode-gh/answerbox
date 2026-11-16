// Package adminauth owns sign-in for the teacher area under /admin: the
// OpenID client, the teacher allow-list and sessions in admin_session.
// "admin" in package and route names means that area; the admin role is
// Teacher.Admin.
package adminauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
)

// SessionTTL is how long a session lasts after sign-in.
const SessionTTL = 12 * time.Hour

// ErrNotAllowed refuses a sign-in whose email is unverified, invalid or not
// on the teacher list.
var ErrNotAllowed = errors.New("adminauth: this account is not allowed to sign in")

// maxNameRunes caps the display name kept from the provider.
const maxNameRunes = 200

// Teacher is a teacher on the list.
type Teacher struct {
	ID          int64
	Email, Name string
	Admin       bool      // the email is one of the configured admin emails
	LastLoginAt time.Time // zero when the teacher never signed in
	ExamCount   int       // exams the teacher owns; set by Teachers only
}

// Session is a valid session.
type Session struct {
	Teacher   Teacher
	CSRFToken string
	ExpiresAt time.Time
}

// Service keeps the teacher list and the sessions.
type Service struct {
	db          *sql.DB
	adminEmails []string // normalized, without repeats, in configured order
	now         func() time.Time
	random      io.Reader
}

// NewService returns a Service whose admins are adminEmails. Every entry must
// be a valid email address; at least one is required.
func NewService(db *sql.DB, adminEmails []string, now func() time.Time, random io.Reader) (*Service, error) {
	var admins []string
	for i, raw := range adminEmails {
		email, ok := normalizeEmail(raw)
		if !ok {
			return nil, fmt.Errorf("adminauth: admin email entry %d is not a valid email address", i+1)
		}
		if !slices.Contains(admins, email) {
			admins = append(admins, email)
		}
	}
	if len(admins) == 0 {
		return nil, errors.New("adminauth: at least one admin email is required")
	}
	return &Service{db: db, adminEmails: admins, now: now, random: random}, nil
}

// EnsureAdmins puts every admin email on the teacher list (allowing it again
// if it was removed) and returns the id of the first one. It is idempotent.
func (s *Service) EnsureAdmins(ctx context.Context) (firstAdminID int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	for _, email := range s.adminEmails {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO teacher (email, created_at) VALUES (?, ?) ON CONFLICT(email) DO UPDATE SET allowed = 1`,
			email, now); err != nil {
			return 0, err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM teacher WHERE email = ?`, s.adminEmails[0]).Scan(&firstAdminID); err != nil {
		return 0, err
	}
	return firstAdminID, tx.Commit()
}

// SignIn creates a session for a verified identity whose email is on the
// teacher list and returns its token; any other identity gets ErrNotAllowed.
func (s *Service) SignIn(ctx context.Context, id Identity) (string, error) {
	email, ok := normalizeEmail(id.Email)
	if !id.EmailVerified || !ok {
		return "", ErrNotAllowed
	}
	token, err := randomToken(s.random)
	if err != nil {
		return "", err
	}
	csrf, err := randomToken(s.random)
	if err != nil {
		return "", err
	}
	now := s.now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var teacherID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM teacher WHERE email = ? AND allowed = 1`, email).Scan(&teacherID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotAllowed
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE teacher SET name = COALESCE(NULLIF(?, ''), name), last_login_at = ? WHERE id = ?`,
		cleanName(id.Name), now.UnixMilli(), teacherID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_session WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO admin_session (token_hash, teacher_id, csrf_token, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), teacherID, csrf, now.Add(SessionTTL).UnixMilli()); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// Session returns the unexpired session for token, if any. A teacher taken
// off the list has no session.
func (s *Service) Session(ctx context.Context, token string) (Session, bool, error) {
	var (
		sess      Session
		expires   int64
		lastLogin sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT s.csrf_token, s.expires_at, t.id, t.email, t.name, t.last_login_at
		 FROM admin_session s JOIN teacher t ON t.id = s.teacher_id
		 WHERE s.token_hash = ? AND s.expires_at > ? AND t.allowed = 1`,
		hashToken(token), s.now().UnixMilli()).
		Scan(&sess.CSRFToken, &expires, &sess.Teacher.ID, &sess.Teacher.Email, &sess.Teacher.Name, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess.ExpiresAt = time.UnixMilli(expires).UTC()
	if lastLogin.Valid {
		sess.Teacher.LastLoginAt = time.UnixMilli(lastLogin.Int64).UTC()
	}
	sess.Teacher.Admin = slices.Contains(s.adminEmails, sess.Teacher.Email)
	return sess, true, nil
}

// Logout deletes the session for token; an unknown token is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_session WHERE token_hash = ?`, hashToken(token))
	return err
}

// normalizeEmail is the one email rule: trimmed and lower case; at most 254
// bytes, no space or control character, one "@" after a non-empty local part,
// and a dotted domain that neither starts nor ends with a dot.
func normalizeEmail(raw string) (email string, ok bool) {
	email = strings.ToLower(strings.TrimSpace(raw))
	if len(email) > 254 || strings.ContainsFunc(email, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", false
	}
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" || strings.Contains(domain, "@") ||
		!strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return email, true
}

// cleanName trims name and cuts it to maxNameRunes runes.
func cleanName(name string) string {
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxNameRunes {
		name = string(r[:maxNameRunes])
	}
	return name
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
