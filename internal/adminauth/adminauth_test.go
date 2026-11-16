package adminauth_test

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

const (
	adminEmail   = "admin@example.test"
	teacherEmail = "teacher@example.test"
)

var start = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

type harness struct {
	svc   *adminauth.Service
	db    *sql.DB
	clock time.Time
	seeds byte // services made so far; each gets its own random stream
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: sqlitetest.Open(t), clock: start}
	h.svc = h.service(t, adminEmail)
	return h
}

// service returns another service on the harness database and clock,
// configured with adminEmails, after EnsureAdmins.
func (h *harness) service(t *testing.T, adminEmails ...string) *adminauth.Service {
	t.Helper()
	h.seeds++
	svc, err := adminauth.NewService(h.db, adminEmails, func() time.Time { return h.clock }, rand.NewChaCha8([32]byte{h.seeds}))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.EnsureAdmins(context.Background()); err != nil {
		t.Fatalf("EnsureAdmins: %v", err)
	}
	return svc
}

func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func verified(email string) adminauth.Identity {
	return adminauth.Identity{Email: email, EmailVerified: true, Name: "Some One"}
}

func mustSignIn(t *testing.T, svc *adminauth.Service, id adminauth.Identity) string {
	t.Helper()
	tok, err := svc.SignIn(context.Background(), id)
	if err != nil {
		t.Fatalf("SignIn(%s): %v", id.Email, err)
	}
	return tok
}

func session(t *testing.T, svc *adminauth.Service, token string) (adminauth.Session, bool) {
	t.Helper()
	sess, ok, err := svc.Session(context.Background(), token)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return sess, ok
}

func (h *harness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestNewService_AdminEmails(t *testing.T) {
	db := sqlitetest.Open(t)
	svc, err := adminauth.NewService(db, []string{" Head@Example.Test "}, time.Now, rand.NewChaCha8([32]byte{}))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.EnsureAdmins(context.Background()); err != nil {
		t.Fatal(err)
	}
	tok := mustSignIn(t, svc, verified("head@example.test"))
	if sess, ok := session(t, svc, tok); !ok || !sess.Teacher.Admin || sess.Teacher.Email != "head@example.test" {
		t.Fatalf("session = %+v ok=%v, want admin head@example.test", sess, ok)
	}

	_, err = adminauth.NewService(db, []string{"a@example.test", "nope"}, time.Now, rand.NewChaCha8([32]byte{}))
	if err == nil || !strings.Contains(err.Error(), "entry 2") || strings.Contains(err.Error(), "nope") {
		t.Errorf("invalid entry: err = %v, want one naming entry 2 without the value", err)
	}
	if _, err := adminauth.NewService(db, nil, time.Now, rand.NewChaCha8([32]byte{})); err == nil {
		t.Error("empty admin list accepted")
	}
}

func TestEnsureAdmins_IdempotentAndReallows(t *testing.T) {
	h := newHarness(t)
	svc, err := adminauth.NewService(h.db, []string{"first@example.test", "second@example.test"}, time.Now, rand.NewChaCha8([32]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id1, err := svc.EnsureAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE teacher SET allowed = 0 WHERE email = 'second@example.test'`); err != nil {
		t.Fatal(err)
	}
	id2, err := svc.EnsureAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("first admin id = %d then %d", id1, id2)
	}
	if got := h.count(t, `SELECT id FROM teacher WHERE email = 'first@example.test'`); int64(got) != id1 {
		t.Errorf("returned id %d, first admin row id %d", id1, got)
	}
	for _, email := range []string{"first@example.test", "second@example.test"} {
		if n := h.count(t, `SELECT COUNT(*) FROM teacher WHERE email = ? AND allowed = 1`, email); n != 1 {
			t.Errorf("%s: %d allowed rows, want 1", email, n)
		}
	}
}

func TestSignIn_AdminAndAllowedTeacher(t *testing.T) {
	h := newHarness(t)
	teacherID := sqlitetest.Teacher(t, h.db, teacherEmail)
	adminID := int64(h.count(t, `SELECT id FROM teacher WHERE email = ?`, adminEmail))

	for _, c := range []struct {
		email string
		id    int64
		admin bool
	}{{adminEmail, adminID, true}, {teacherEmail, teacherID, false}} {
		tok := mustSignIn(t, h.svc, verified(c.email))
		if len(tok) != 43 {
			t.Errorf("%s: token length %d, want 43", c.email, len(tok))
		}
		sess, ok := session(t, h.svc, tok)
		if !ok {
			t.Fatalf("%s: session not found", c.email)
		}
		if sess.Teacher.ID != c.id || sess.Teacher.Email != c.email || sess.Teacher.Admin != c.admin {
			t.Errorf("%s: teacher = %+v, want id %d admin %v", c.email, sess.Teacher, c.id, c.admin)
		}
		if !sess.ExpiresAt.Equal(start.Add(adminauth.SessionTTL)) {
			t.Errorf("%s: ExpiresAt = %v", c.email, sess.ExpiresAt)
		}
		if len(sess.CSRFToken) != 43 {
			t.Errorf("%s: CSRF token length %d, want 43", c.email, len(sess.CSRFToken))
		}
		if again := mustSignIn(t, h.svc, verified(c.email)); again == tok {
			t.Errorf("%s: two sign-ins gave the same token", c.email)
		}
	}
}

func TestSignIn_Refused(t *testing.T) {
	h := newHarness(t)
	removed := sqlitetest.Teacher(t, h.db, "removed@example.test")
	if _, err := h.db.Exec(`UPDATE teacher SET allowed = 0 WHERE id = ?`, removed); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]adminauth.Identity{
		"unlisted":       verified("stranger@example.test"),
		"unverified":     {Email: adminEmail, EmailVerified: false},
		"removed":        verified("removed@example.test"),
		"not an address": verified("not an email"),
	} {
		if _, err := h.svc.SignIn(context.Background(), id); !errors.Is(err, adminauth.ErrNotAllowed) {
			t.Errorf("%s: err = %v, want ErrNotAllowed", name, err)
		}
	}
	if n := h.count(t, `SELECT COUNT(*) FROM admin_session`); n != 0 {
		t.Errorf("admin_session rows = %d, want 0", n)
	}
}

func TestSignIn_EmailCaseInsensitive(t *testing.T) {
	h := newHarness(t)
	tok := mustSignIn(t, h.svc, verified("ADMIN@Example.Test"))
	if sess, ok := session(t, h.svc, tok); !ok || sess.Teacher.Email != adminEmail {
		t.Fatalf("session = %+v ok=%v", sess, ok)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM teacher WHERE email = ?`, adminEmail); n != 1 {
		t.Errorf("stored admin rows = %d, want 1", n)
	}
}

func TestSignIn_UpdatesNameAndLastLogin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	stored := func() (string, int64) {
		t.Helper()
		var (
			name string
			last sql.NullInt64
		)
		if err := h.db.QueryRow(`SELECT name, last_login_at FROM teacher WHERE email = ?`, adminEmail).Scan(&name, &last); err != nil {
			t.Fatal(err)
		}
		return name, last.Int64
	}
	if _, last := stored(); last != 0 {
		t.Fatalf("last_login_at before sign-in = %d", last)
	}

	tok := mustSignIn(t, h.svc, adminauth.Identity{Email: adminEmail, EmailVerified: true, Name: "  Ada Admin "})
	if name, last := stored(); name != "Ada Admin" || last != start.UnixMilli() {
		t.Errorf("after first sign-in: %q %d", name, last)
	}
	sess, _ := session(t, h.svc, tok)
	if sess.Teacher.Name != "Ada Admin" || !sess.Teacher.LastLoginAt.Equal(start) {
		t.Errorf("session teacher = %+v", sess.Teacher)
	}

	for _, blank := range []string{"", "   "} {
		h.advance(time.Minute)
		mustSignIn(t, h.svc, adminauth.Identity{Email: adminEmail, EmailVerified: true, Name: blank})
		if name, last := stored(); name != "Ada Admin" || last != h.clock.UnixMilli() {
			t.Errorf("blank name %q: %q %d", blank, name, last)
		}
	}

	if _, err := h.svc.SignIn(ctx, adminauth.Identity{Email: adminEmail, EmailVerified: true, Name: strings.Repeat("é", 300)}); err != nil {
		t.Fatal(err)
	}
	if name, _ := stored(); utf8.RuneCountInString(name) != 200 || name != strings.Repeat("é", 200) {
		t.Errorf("long name stored as %d runes", utf8.RuneCountInString(name))
	}
}

func TestSession_ExpiryAndLogout(t *testing.T) {
	h := newHarness(t)
	tok := mustSignIn(t, h.svc, verified(adminEmail))

	h.advance(adminauth.SessionTTL - time.Millisecond)
	if _, ok := session(t, h.svc, tok); !ok {
		t.Fatal("session invalid before now+12h")
	}
	h.advance(time.Millisecond)
	if _, ok := session(t, h.svc, tok); ok {
		t.Fatal("session still valid at now+12h")
	}

	tok2 := mustSignIn(t, h.svc, verified(adminEmail))
	if rows := h.count(t, `SELECT COUNT(*) FROM admin_session`); rows != 1 {
		t.Errorf("admin_session rows = %d, want 1 (expired rows deleted at sign-in)", rows)
	}
	tok3 := mustSignIn(t, h.svc, verified(adminEmail))

	if err := h.svc.Logout(context.Background(), tok2); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, ok := session(t, h.svc, tok2); ok {
		t.Fatal("session valid after Logout")
	}
	if _, ok := session(t, h.svc, tok3); !ok {
		t.Fatal("Logout ended another session")
	}
	if err := h.svc.Logout(context.Background(), "unknown"); err != nil {
		t.Fatalf("Logout(unknown) = %v, want nil", err)
	}
}

func TestSession_AdminFollowsConfiguredEmails(t *testing.T) {
	h := newHarness(t)
	a := h.service(t, "a@example.test")
	tokA := mustSignIn(t, a, verified("a@example.test"))
	if sess, _ := session(t, a, tokA); !sess.Teacher.Admin {
		t.Fatal("a is not admin under {a}")
	}

	b := h.service(t, "b@example.test")
	sess, ok := session(t, b, tokA)
	if !ok || sess.Teacher.Admin {
		t.Fatalf("a's session under {b}: ok=%v admin=%v, want valid and not admin", ok, sess.Teacher.Admin)
	}
	tokB := mustSignIn(t, b, verified("b@example.test"))
	if sess, ok := session(t, b, tokB); !ok || !sess.Teacher.Admin {
		t.Fatalf("b under {b}: ok=%v admin=%v", ok, sess.Teacher.Admin)
	}
}

func TestSession_SurvivesNewService(t *testing.T) {
	h := newHarness(t)
	tok := mustSignIn(t, h.svc, verified(adminEmail))
	if sess, ok := session(t, h.service(t, adminEmail), tok); !ok || sess.Teacher.Email != adminEmail {
		t.Fatalf("session in a new service: ok=%v %+v", ok, sess)
	}
}

func TestSession_RemovedTeacherIsSignedOut(t *testing.T) {
	h := newHarness(t)
	id := sqlitetest.Teacher(t, h.db, teacherEmail)
	tok := mustSignIn(t, h.svc, verified(teacherEmail))
	adminTok := mustSignIn(t, h.svc, verified(adminEmail))
	if _, err := h.db.Exec(`UPDATE teacher SET allowed = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := session(t, h.svc, tok); ok {
		t.Fatal("removed teacher's session still valid")
	}
	if _, ok := session(t, h.svc, adminTok); !ok {
		t.Fatal("another teacher's session ended")
	}
}
