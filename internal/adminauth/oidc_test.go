package adminauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiatcode-gh/go-exam/internal/adminauth"
	"github.com/fiatcode-gh/go-exam/internal/oidcfake"
)

const (
	oidcClientID = "test-client"
	oidcSecret   = "test-secret"
	oidcRedirect = "http://app.test/admin/auth/callback"
)

var oidcNow = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

func fixedNow() time.Time { return oidcNow }

type oidcEnv struct {
	fake *oidcfake.Provider
	srv  *httptest.Server
	// down makes the server answer 503 to every request while set.
	down atomic.Bool
}

func newOIDCEnv(t *testing.T) *oidcEnv {
	t.Helper()
	e := &oidcEnv{fake: oidcfake.New(oidcfake.Config{
		ClientID:     oidcClientID,
		ClientSecret: oidcSecret,
		RedirectURL:  oidcRedirect,
		Now:          fixedNow,
	})}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.down.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		e.fake.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	e.fake.SetIssuer(e.srv.URL)
	return e
}

func (e *oidcEnv) provider(secret string, seed byte) *adminauth.Provider {
	return adminauth.NewProvider(adminauth.ProviderConfig{
		Issuer:       e.srv.URL,
		ClientID:     oidcClientID,
		ClientSecret: secret,
		RedirectURL:  oidcRedirect,
		HTTPClient:   e.srv.Client(),
		Now:          fixedNow,
		Random:       rand.NewChaCha8([32]byte{seed}),
	})
}

// signIn runs Begin and the provider's form for id and returns the flow and
// the state and code from the callback URL.
func (e *oidcEnv) signIn(t *testing.T, p *adminauth.Provider, id oidcfake.Identity) (adminauth.Flow, string, string) {
	t.Helper()
	flow, authURL, err := p.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	cb, err := oidcfake.Approve(t.Context(), e.srv.Client(), authURL, id)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	u, err := url.Parse(cb)
	if err != nil {
		t.Fatalf("callback URL: %v", err)
	}
	return flow, u.Query().Get("state"), u.Query().Get("code")
}

var bob = oidcfake.Identity{Email: "bob@example.org", Name: "Bob Teacher"}

func TestProvider_BeginBuildsAuthURL(t *testing.T) {
	e := newOIDCEnv(t)
	p := e.provider(oidcSecret, 1)
	flow, authURL, err := p.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.HasPrefix(authURL, e.srv.URL+"/auth?") {
		t.Fatalf("auth URL %q does not start with %q", authURL, e.srv.URL+"/auth?")
	}
	if strings.Contains(authURL, oidcSecret) {
		t.Errorf("auth URL contains the client secret")
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	sum := sha256.Sum256([]byte(flow.Verifier))
	want := map[string]string{
		"client_id":             oidcClientID,
		"redirect_uri":          oidcRedirect,
		"response_type":         "code",
		"state":                 flow.State,
		"nonce":                 flow.Nonce,
		"code_challenge":        base64.RawURLEncoding.EncodeToString(sum[:]),
		"code_challenge_method": "S256",
		"prompt":                "select_account",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	scopes := strings.Fields(q.Get("scope"))
	for _, s := range []string{"openid", "email", "profile"} {
		found := false
		for _, got := range scopes {
			found = found || got == s
		}
		if !found {
			t.Errorf("scope %q lacks %q", q.Get("scope"), s)
		}
	}
	vals := []string{flow.State, flow.Nonce, flow.Verifier}
	for i, v := range vals {
		if len(v) != 43 {
			t.Errorf("flow value %d has length %d, want 43", i, len(v))
		}
		for _, w := range vals[i+1:] {
			if v == w {
				t.Errorf("flow values repeat: %+v", flow)
			}
		}
	}
	flow2, _, err := p.Begin(t.Context())
	if err != nil {
		t.Fatalf("second Begin: %v", err)
	}
	if flow2 == flow {
		t.Errorf("two Begin calls gave the same flow")
	}
}

func TestProvider_CompleteReturnsIdentity(t *testing.T) {
	t.Run("verified", func(t *testing.T) {
		e := newOIDCEnv(t)
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, bob)
		id, err := p.Complete(t.Context(), flow, state, code)
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		want := adminauth.Identity{Email: bob.Email, EmailVerified: true, Name: bob.Name}
		if id != want {
			t.Errorf("identity = %+v, want %+v", id, want)
		}
	})
	t.Run("unverified", func(t *testing.T) {
		e := newOIDCEnv(t)
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, oidcfake.Identity{Email: bob.Email, Unverified: true})
		id, err := p.Complete(t.Context(), flow, state, code)
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if id.Email != bob.Email || id.EmailVerified {
			t.Errorf("identity = %+v, want %s unverified", id, bob.Email)
		}
	})
}

type refusal struct {
	name   string
	reason string
	detail string
	// run performs the failing Complete and returns the strings that must
	// not appear in the error text, plus the error.
	run refusalRun
}

type refusalRun func(t *testing.T, e *oidcEnv) ([]string, error)

func secretsOf(flow adminauth.Flow, code string) []string {
	return []string{code, oidcSecret, bob.Email, flow.State, flow.Nonce, flow.Verifier, "eyJ"}
}

// tampered signs in with the fake set to t and completes honestly.
func tampered(tm oidcfake.Tamper) refusalRun {
	return func(t *testing.T, e *oidcEnv) ([]string, error) {
		e.fake.SetTamper(tm)
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, bob)
		_, err := p.Complete(t.Context(), flow, state, code)
		return secretsOf(flow, code), err
	}
}

var refusals = []refusal{
	{name: "wrong state", reason: "state", run: func(t *testing.T, e *oidcEnv) ([]string, error) {
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, bob)
		_, err := p.Complete(t.Context(), flow, state+"x", code)
		if n := e.fake.TokenCalls(); n != 0 {
			t.Errorf("TokenCalls = %d, want 0", n)
		}
		return secretsOf(flow, code), err
	}},
	{name: "empty flow", reason: "state", run: func(t *testing.T, e *oidcEnv) ([]string, error) {
		p := e.provider(oidcSecret, 1)
		flow, _, code := e.signIn(t, p, bob)
		_, err := p.Complete(t.Context(), adminauth.Flow{}, "", code)
		return secretsOf(flow, code), err
	}},
	{name: "wrong nonce", reason: "nonce", run: tampered(oidcfake.Tamper{Nonce: "other"})},
	{name: "bad signature", reason: "id_token", run: tampered(oidcfake.Tamper{WrongKey: true})},
	{name: "wrong audience", reason: "id_token", run: tampered(oidcfake.Tamper{Audience: "someone-else"})},
	{name: "second audience", reason: "id_token", run: tampered(oidcfake.Tamper{ExtraAudience: "someone-else"})},
	{name: "wrong issuer", reason: "id_token", run: tampered(oidcfake.Tamper{Issuer: "https://evil.example"})},
	{name: "expired token", reason: "id_token", run: tampered(oidcfake.Tamper{Expired: true})},
	{name: "no id token", reason: "id_token", run: tampered(oidcfake.Tamper{OmitIDToken: true})},
	{name: "no email claim", reason: "claims", run: tampered(oidcfake.Tamper{OmitEmail: true})},
	{name: "wrong verifier", reason: "exchange", detail: "400 invalid_grant", run: func(t *testing.T, e *oidcEnv) ([]string, error) {
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, bob)
		other, _, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		bad := flow
		bad.Verifier = other.Verifier
		_, err = p.Complete(t.Context(), bad, state, code)
		return append(secretsOf(flow, code), other.Verifier), err
	}},
	{name: "reused code", reason: "exchange", detail: "400 invalid_grant", run: func(t *testing.T, e *oidcEnv) ([]string, error) {
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, bob)
		if _, err := p.Complete(t.Context(), flow, state, code); err != nil {
			t.Fatalf("first Complete: %v", err)
		}
		_, err := p.Complete(t.Context(), flow, state, code)
		return secretsOf(flow, code), err
	}},
	{name: "wrong client secret", reason: "exchange", detail: "401 invalid_client", run: func(t *testing.T, e *oidcEnv) ([]string, error) {
		p := e.provider("other-secret", 1)
		flow, state, code := e.signIn(t, p, bob)
		_, err := p.Complete(t.Context(), flow, state, code)
		return append(secretsOf(flow, code), "other-secret"), err
	}},
	{name: "empty code", reason: "exchange", run: func(t *testing.T, e *oidcEnv) ([]string, error) {
		p := e.provider(oidcSecret, 1)
		flow, state, code := e.signIn(t, p, bob)
		_, err := p.Complete(t.Context(), flow, state, "")
		return secretsOf(flow, code), err
	}},
}

func TestProvider_CompleteRefusals(t *testing.T) {
	for _, r := range refusals {
		t.Run(r.name, func(t *testing.T) {
			_, err := r.run(t, newOIDCEnv(t))
			var fe *adminauth.FlowError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v, want *FlowError", err)
			}
			if fe.Reason != r.reason || fe.Detail != r.detail {
				t.Errorf("reason, detail = %q, %q; want %q, %q", fe.Reason, fe.Detail, r.reason, r.detail)
			}
		})
	}
}

func TestProvider_DiscoveryIsLazyAndRetried(t *testing.T) {
	e := newOIDCEnv(t)
	e.down.Store(true)
	p := e.provider(oidcSecret, 1)
	_, _, err := p.Begin(t.Context())
	var fe *adminauth.FlowError
	if !errors.As(err, &fe) || fe.Reason != "discovery" {
		t.Fatalf("Begin while down: err = %v, want reason discovery", err)
	}
	e.down.Store(false)
	if _, _, err := p.Begin(t.Context()); err != nil {
		t.Fatalf("Begin after recovery: %v", err)
	}
}

func TestFlowError_HidesSecrets(t *testing.T) {
	for _, r := range refusals {
		t.Run(r.name, func(t *testing.T) {
			secrets, err := r.run(t, newOIDCEnv(t))
			if err == nil {
				t.Fatal("Complete succeeded")
			}
			msg := err.Error()
			for _, s := range secrets {
				if s != "" && strings.Contains(msg, s) {
					t.Errorf("error %q contains %q", msg, s)
				}
			}
		})
	}
}
