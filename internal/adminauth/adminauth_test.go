package adminauth_test

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
	"github.com/fiatcode-gh/go-exam/internal/platform/sqlite/sqlitetest"
)

const password = "correct-horse-battery"

var start = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

type harness struct {
	svc   *adminauth.Service
	db    *sql.DB
	clock time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: sqlitetest.Open(t), clock: start}
	h.svc = adminauth.NewService(h.db, password, func() time.Time { return h.clock }, rand.NewChaCha8([32]byte{7}))
	return h
}

func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *harness) login(pw string, ip netip.Addr) (string, error) {
	return h.svc.Login(context.Background(), pw, ip)
}

func (h *harness) mustLogin(t *testing.T, ip netip.Addr) string {
	t.Helper()
	tok, err := h.login(password, ip)
	if err != nil {
		t.Fatalf("Login(right password, %v): %v", ip, err)
	}
	return tok
}

// fail records one wrong password and requires ErrWrongPassword.
func (h *harness) fail(t *testing.T, ip netip.Addr) {
	t.Helper()
	if _, err := h.login("wrong", ip); !errors.Is(err, adminauth.ErrWrongPassword) {
		t.Fatalf("Login(wrong, %v) = %v, want ErrWrongPassword", ip, err)
	}
}

// requireLocked asserts that any password is refused with the given wait.
func (h *harness) requireLocked(t *testing.T, ip netip.Addr, pw string, want time.Duration) {
	t.Helper()
	_, err := h.login(pw, ip)
	var te *adminauth.ThrottledError
	if !errors.As(err, &te) {
		t.Fatalf("Login(%q, %v) = %v, want *ThrottledError", pw, ip, err)
	}
	if te.RetryAfter != want {
		t.Fatalf("RetryAfter = %v, want %v", te.RetryAfter, want)
	}
}

func (h *harness) valid(t *testing.T, token string) bool {
	t.Helper()
	_, ok, err := h.svc.Session(context.Background(), token)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return ok
}

var ipA = netip.MustParseAddr("198.51.100.10")

func TestLogin_RightAndWrong(t *testing.T) {
	h := newHarness(t)
	tok := h.mustLogin(t, ipA)
	if tok == "" {
		t.Fatal("empty token")
	}
	sess, ok, err := h.svc.Session(context.Background(), tok)
	if err != nil || !ok {
		t.Fatalf("Session(token) = %v, %v", ok, err)
	}
	if sess.CSRFToken == "" || sess.CSRFToken == tok {
		t.Errorf("CSRFToken = %q, want a separate random token", sess.CSRFToken)
	}
	if !sess.ExpiresAt.Equal(start.Add(adminauth.SessionTTL)) {
		t.Errorf("ExpiresAt = %v, want %v", sess.ExpiresAt, start.Add(adminauth.SessionTTL))
	}

	h.fail(t, ipA)
	if h.valid(t, "not-a-token") {
		t.Error("unknown token is a valid session")
	}

	var stored int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM admin_session WHERE token_hash = ? OR CAST(token_hash AS TEXT) = ? OR csrf_token = ?`,
		[]byte(tok), tok, tok).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Error("the raw session token is stored")
	}
}

func TestLogin_ThrottleSchedule(t *testing.T) {
	h := newHarness(t)
	for range 4 {
		h.fail(t, ipA)
	}
	h.mustLogin(t, ipA) // failures 1–4 cost nothing; success clears

	for range 5 {
		h.fail(t, ipA)
	}
	h.requireLocked(t, ipA, "wrong", 30*time.Second)
	h.advance(10 * time.Second)
	h.requireLocked(t, ipA, password, 20*time.Second)
	h.requireLocked(t, ipA, "wrong", 20*time.Second) // not counted, not extended

	h.advance(20 * time.Second)
	h.fail(t, ipA) // 6th
	h.requireLocked(t, ipA, password, 60*time.Second)
	h.advance(60 * time.Second)
	h.fail(t, ipA) // 7th
	h.requireLocked(t, ipA, password, 120*time.Second)

	for _, want := range []time.Duration{240 * time.Second, 480 * time.Second, 15 * time.Minute, 15 * time.Minute} {
		h.advance(time.Hour - time.Second) // below the idle limit since the last failure
		h.fail(t, ipA)
		h.requireLocked(t, ipA, password, want)
	}

	h.advance(15 * time.Minute)
	h.mustLogin(t, ipA)
	for range 4 {
		h.fail(t, ipA)
	}
	h.mustLogin(t, ipA) // success cleared the lock history

	for range 4 {
		h.fail(t, ipA)
	}
	h.advance(time.Hour)
	h.fail(t, ipA) // the 4 earlier failures are idle and forgotten
	h.mustLogin(t, ipA)
}

// The four free failures plus the one that locks are the only wrong
// passwords checked, however many requests race for the same address.
func TestLogin_ThrottleConcurrent(t *testing.T) {
	h := newHarness(t)
	const n = 200
	errs := make(chan error, n)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			<-ready
			_, err := h.login("wrong", ipA)
			errs <- err
		})
	}
	close(ready)
	wg.Wait()
	close(errs)

	var wrong, throttled int
	for err := range errs {
		var te *adminauth.ThrottledError
		switch {
		case errors.Is(err, adminauth.ErrWrongPassword):
			wrong++
		case errors.As(err, &te):
			throttled++
		default:
			t.Fatalf("Login = %v, want ErrWrongPassword or *ThrottledError", err)
		}
	}
	if wrong != 5 || throttled != n-5 {
		t.Fatalf("%d wrong-password and %d throttled results, want 5 and %d", wrong, throttled, n-5)
	}
}

func TestLogin_ThrottleIdleLockedEntryForgotten(t *testing.T) {
	h := newHarness(t)
	for range 5 {
		h.fail(t, ipA)
	}
	h.advance(time.Hour)
	h.fail(t, ipA) // counts as the first failure again: no lock
	h.mustLogin(t, ipA)
}

func TestLogin_ThrottleKeys(t *testing.T) {
	h := newHarness(t)
	for range 5 {
		h.fail(t, ipA)
	}
	h.requireLocked(t, ipA, password, 30*time.Second)
	h.mustLogin(t, netip.MustParseAddr("198.51.100.11"))

	v6a := netip.MustParseAddr("2001:db8:1:2::1")
	v6b := netip.MustParseAddr("2001:db8:1:2:ffff::9")
	v6other := netip.MustParseAddr("2001:db8:1:3::1")
	for range 5 {
		h.fail(t, v6a)
	}
	h.requireLocked(t, v6b, password, 30*time.Second)
	h.mustLogin(t, v6other)

	var unknown netip.Addr
	for range 5 {
		h.fail(t, unknown)
	}
	h.requireLocked(t, unknown, password, 30*time.Second)
}

func TestThrottle_Bounded(t *testing.T) {
	h := newHarness(t)
	for i := range 5000 {
		ip := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		h.fail(t, ip)
		h.advance(time.Millisecond)
	}
	if n := adminauth.ThrottleSize(h.svc); n > 4096 {
		t.Fatalf("throttle holds %d keys, want ≤ 4096", n)
	}
	// The newest keys survive pruning; the oldest were dropped.
	newest := netip.AddrFrom4([4]byte{10, 0, byte(4999 >> 8), byte(4999 & 0xff)})
	for range 4 {
		h.fail(t, newest)
	}
	h.requireLocked(t, newest, password, 30*time.Second)
}

func TestSession_ExpiryAndLogout(t *testing.T) {
	h := newHarness(t)
	tok := h.mustLogin(t, ipA)

	h.advance(adminauth.SessionTTL - time.Millisecond)
	if !h.valid(t, tok) {
		t.Fatal("session invalid before now+12h")
	}
	h.advance(time.Millisecond)
	if h.valid(t, tok) {
		t.Fatal("session still valid at now+12h")
	}

	tok2 := h.mustLogin(t, ipA)
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM admin_session`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("admin_session rows = %d, want 1 (expired rows deleted at login)", rows)
	}

	if err := h.svc.Logout(context.Background(), tok2); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if h.valid(t, tok2) {
		t.Fatal("session valid after Logout")
	}
	if err := h.svc.Logout(context.Background(), "unknown"); err != nil {
		t.Fatalf("Logout(unknown) = %v, want nil", err)
	}
}
