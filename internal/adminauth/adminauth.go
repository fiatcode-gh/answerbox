// Package adminauth owns teacher sign-in: password check, login throttle and
// admin sessions stored in admin_session.
package adminauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"
)

// SessionTTL is how long an admin session lasts after login.
const SessionTTL = 12 * time.Hour

var ErrWrongPassword = errors.New("adminauth: wrong password")

// ThrottledError is returned while the client's address is locked out.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("adminauth: too many attempts, retry after %v", e.RetryAfter)
}

// Session is a valid admin session.
type Session struct {
	CSRFToken string
	ExpiresAt time.Time
}

// Service checks the admin password and manages admin sessions.
type Service struct {
	db       *sql.DB
	password string
	now      func() time.Time
	random   io.Reader
	throttle throttle
}

// NewService returns a Service for the configured admin password.
func NewService(db *sql.DB, password string, now func() time.Time, random io.Reader) *Service {
	return &Service{db: db, password: password, now: now, random: random}
}

// Login checks password for the client at ip and, when it is right, creates
// a session and returns its token. A locked-out client gets *ThrottledError
// without its password being checked; a wrong password gets ErrWrongPassword.
func (s *Service) Login(ctx context.Context, password string, ip netip.Addr) (string, error) {
	key := throttleKey(ip)
	now := s.now()
	if wait := s.throttle.locked(key, now); wait > 0 {
		return "", &ThrottledError{RetryAfter: wait}
	}
	given, want := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(s.password))
	if subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
		s.throttle.fail(key, now)
		return "", ErrWrongPassword
	}
	s.throttle.clear(key)

	token, err := s.randomToken()
	if err != nil {
		return "", err
	}
	csrf, err := s.randomToken()
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_session WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_session (token_hash, csrf_token, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), csrf, now.Add(SessionTTL).UnixMilli()); err != nil {
		return "", err
	}
	return token, nil
}

// Session returns the unexpired session for token, if any.
func (s *Service) Session(ctx context.Context, token string) (Session, bool, error) {
	var (
		sess    Session
		expires int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT csrf_token, expires_at FROM admin_session WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), s.now().UnixMilli()).Scan(&sess.CSRFToken, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess.ExpiresAt = time.UnixMilli(expires).UTC()
	return sess, true, nil
}

// Logout deletes the session for token; an unknown token is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_session WHERE token_hash = ?`, hashToken(token))
	return err
}

// randomToken returns 32 random bytes, base64url without padding (D4).
func (s *Service) randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(s.random, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
