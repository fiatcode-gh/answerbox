package oidcfake_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/fiatcode-gh/go-exam/internal/oidcfake"
	"golang.org/x/oauth2"
)

const (
	testClientID = "test-client"
	// The secret holds characters that HTTP Basic authentication escapes
	// (RFC 6749 §2.3.1), so the provider has to unescape it.
	testSecret   = "s3cr3t+/="
	testRedirect = "http://app.test/admin/auth/callback"
)

// clock is shared by the provider and the verifier; tests move it forward.
type clock struct{ unixNano atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.unixNano.Store(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *clock) Now() time.Time          { return time.Unix(0, c.unixNano.Load()).UTC() }
func (c *clock) Advance(d time.Duration) { c.unixNano.Add(int64(d)) }

type env struct {
	p        *oidcfake.Provider
	srv      *httptest.Server
	clock    *clock
	ctx      context.Context
	provider *oidc.Provider
}

func newEnv(t *testing.T) *env {
	t.Helper()
	c := newClock()
	p := oidcfake.New(oidcfake.Config{
		ClientID:     testClientID,
		ClientSecret: testSecret,
		RedirectURL:  testRedirect,
		Now:          c.Now,
	})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	p.SetIssuer(srv.URL)
	ctx := oidc.ClientContext(t.Context(), srv.Client())
	provider, err := oidc.NewProvider(ctx, srv.URL)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	return &env{p: p, srv: srv, clock: c, ctx: ctx, provider: provider}
}

func (e *env) oauthConfig(style oauth2.AuthStyle) *oauth2.Config {
	ep := e.provider.Endpoint()
	ep.AuthStyle = style
	return &oauth2.Config{
		ClientID:     testClientID,
		ClientSecret: testSecret,
		Endpoint:     ep,
		RedirectURL:  testRedirect,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

func (e *env) verifier() *oidc.IDTokenVerifier {
	return e.provider.Verifier(&oidc.Config{ClientID: testClientID, Now: e.clock.Now})
}

// approve runs the browser half of the flow and returns the code.
func (e *env) approve(t *testing.T, cfg *oauth2.Config, state, nonce, verifier string, id oidcfake.Identity) string {
	t.Helper()
	authURL := cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	cb, err := oidcfake.Approve(e.ctx, e.srv.Client(), authURL, id)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !strings.HasPrefix(cb, testRedirect+"?") {
		t.Fatalf("callback %q does not start with %q", cb, testRedirect)
	}
	u, err := url.Parse(cb)
	if err != nil {
		t.Fatalf("callback URL: %v", err)
	}
	if got := u.Query().Get("state"); got != state {
		t.Fatalf("callback state = %q, want %q", got, state)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("callback %q has no code", cb)
	}
	return code
}

type claims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// signIn runs the whole flow and returns the raw ID token.
func (e *env) signIn(t *testing.T, style oauth2.AuthStyle, id oidcfake.Identity) *oauth2.Token {
	t.Helper()
	cfg := e.oauthConfig(style)
	v := oauth2.GenerateVerifier()
	code := e.approve(t, cfg, "state-1", "nonce-1", v, id)
	tok, err := cfg.Exchange(e.ctx, code, oauth2.VerifierOption(v))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return tok
}

func rawIDToken(t *testing.T, tok *oauth2.Token) string {
	t.Helper()
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		t.Fatalf("token response has no id_token")
	}
	return raw
}

var alice = oidcfake.Identity{Email: "alice@example.org", Name: "Alice Teacher"}

func TestProvider_CodeFlowRoundTrip(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn(t, oauth2.AuthStyleInParams, alice)
	idt, err := e.verifier().Verify(e.ctx, rawIDToken(t, tok))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if idt.Nonce != "nonce-1" {
		t.Errorf("nonce = %q, want nonce-1", idt.Nonce)
	}
	var c claims
	if err := idt.Claims(&c); err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if c.Email != alice.Email || !c.EmailVerified || c.Name != alice.Name {
		t.Errorf("claims = %+v, want email %q verified, name %q", c, alice.Email, alice.Name)
	}
	if got := e.p.TokenCalls(); got != 1 {
		t.Errorf("TokenCalls = %d, want 1", got)
	}
}

func TestProvider_BasicAuthAccepted(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn(t, oauth2.AuthStyleInHeader, alice)
	if _, err := e.verifier().Verify(e.ctx, rawIDToken(t, tok)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := e.p.TokenCalls(); got != 1 {
		t.Errorf("TokenCalls = %d, want 1", got)
	}
}

func noRedirectClient(srv *httptest.Server) *http.Client {
	c := *srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func validAuthParams() url.Values {
	return url.Values{
		"client_id":             {testClientID},
		"redirect_uri":          {testRedirect},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {"state-1"},
		"nonce":                 {"nonce-1"},
		"code_challenge":        {"challenge"},
		"code_challenge_method": {"S256"},
	}
}

func TestProvider_AuthRejectsBadRequests(t *testing.T) {
	e := newEnv(t)
	client := noRedirectClient(e.srv)

	get := func(v url.Values) *http.Response {
		t.Helper()
		resp, err := client.Get(e.srv.URL + "/auth?" + v.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	post := func(v url.Values) *http.Response {
		t.Helper()
		resp, err := client.PostForm(e.srv.URL+"/auth", v)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	withEmail := func(v url.Values) url.Values {
		v.Set("email", "alice@example.org")
		return v
	}

	// The valid request works, so each rejection below is caused by its one change.
	if resp := get(validAuthParams()); resp.StatusCode != http.StatusOK {
		t.Fatalf("valid GET /auth = %d, want 200", resp.StatusCode)
	}
	if resp := post(withEmail(validAuthParams())); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid POST /auth = %d, want 303", resp.StatusCode)
	}

	cases := []struct {
		name   string
		change func(url.Values)
	}{
		{"wrong client_id", func(v url.Values) { v.Set("client_id", "other") }},
		{"wrong redirect_uri", func(v url.Values) { v.Set("redirect_uri", testRedirect+"/x") }},
		{"response_type token", func(v url.Values) { v.Set("response_type", "token") }},
		{"scope without openid", func(v url.Values) { v.Set("scope", "email profile") }},
		{"missing state", func(v url.Values) { v.Del("state") }},
		{"missing nonce", func(v url.Values) { v.Del("nonce") }},
		{"missing code_challenge", func(v url.Values) { v.Del("code_challenge") }},
		{"plain challenge method", func(v url.Values) { v.Set("code_challenge_method", "plain") }},
	}
	for _, tc := range cases {
		t.Run("GET/"+tc.name, func(t *testing.T) {
			v := validAuthParams()
			tc.change(v)
			resp := get(v)
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Errorf("status %d, Location %q; want 400 without Location", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
		t.Run("POST/"+tc.name, func(t *testing.T) {
			v := withEmail(validAuthParams())
			tc.change(v)
			resp := post(v)
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Errorf("status %d, Location %q; want 400 without Location", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
	t.Run("POST/missing email", func(t *testing.T) {
		resp := post(validAuthParams())
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("status %d, Location %q; want 400 without Location", resp.StatusCode, resp.Header.Get("Location"))
		}
	})
}

type tokenRequest struct {
	form       url.Values
	basicUser  string
	basicPass  string
	paramsAuth bool
}

// postToken sends a raw token request and returns the status and the
// "error" field of the JSON body.
func (e *env) postToken(t *testing.T, req tokenRequest) (int, string) {
	t.Helper()
	form := url.Values{}
	for k, v := range req.form {
		form[k] = v
	}
	if req.paramsAuth {
		form.Set("client_id", testClientID)
		form.Set("client_secret", testSecret)
	}
	r, err := http.NewRequestWithContext(e.ctx, http.MethodPost, e.srv.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if req.basicUser != "" {
		r.SetBasicAuth(url.QueryEscape(req.basicUser), url.QueryEscape(req.basicPass))
	}
	resp, err := e.srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &body)
	return resp.StatusCode, body.Error
}

func TestProvider_TokenRejects(t *testing.T) {
	type grant struct {
		code, verifier string
	}
	// fresh starts a flow and returns a live code and its verifier.
	fresh := func(t *testing.T, e *env) grant {
		t.Helper()
		v := oauth2.GenerateVerifier()
		code := e.approve(t, e.oauthConfig(oauth2.AuthStyleInParams), "state-1", "nonce-1", v, alice)
		return grant{code, v}
	}
	exchangeForm := func(g grant) url.Values {
		return url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {g.code},
			"redirect_uri":  {testRedirect},
			"code_verifier": {g.verifier},
		}
	}
	// correct is the honest exchange for g.
	correct := func(g grant) tokenRequest {
		return tokenRequest{form: exchangeForm(g), paramsAuth: true}
	}

	cases := []struct {
		name      string
		attempt   func(t *testing.T, e *env, g grant) tokenRequest
		status    int
		errCode   string
		codeAlive bool // whether the honest exchange still works afterwards
	}{
		{
			name: "wrong secret",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				return tokenRequest{form: exchangeForm(g), basicUser: testClientID, basicPass: "wrong"}
			},
			status: http.StatusUnauthorized, errCode: "invalid_client", codeAlive: true,
		},
		{
			name: "wrong verifier",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				r := correct(g)
				r.form.Set("code_verifier", oauth2.GenerateVerifier())
				return r
			},
			status: http.StatusBadRequest, errCode: "invalid_grant",
		},
		{
			name: "missing verifier",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				r := correct(g)
				r.form.Del("code_verifier")
				return r
			},
			status: http.StatusBadRequest, errCode: "invalid_grant",
		},
		{
			name: "unknown code",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				r := correct(g)
				r.form.Set("code", "not-a-code")
				return r
			},
			status: http.StatusBadRequest, errCode: "invalid_grant", codeAlive: true,
		},
		{
			name: "reused code",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				if status, _ := e.postToken(t, correct(g)); status != http.StatusOK {
					t.Fatalf("first exchange = %d, want 200", status)
				}
				return correct(g)
			},
			status: http.StatusBadRequest, errCode: "invalid_grant",
		},
		{
			name: "wrong redirect_uri",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				r := correct(g)
				r.form.Set("redirect_uri", testRedirect+"/x")
				return r
			},
			status: http.StatusBadRequest, errCode: "invalid_grant",
		},
		{
			name: "code older than 60 s",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				e.clock.Advance(61 * time.Second)
				return correct(g)
			},
			status: http.StatusBadRequest, errCode: "invalid_grant",
		},
		{
			name: "refresh_token grant",
			attempt: func(t *testing.T, e *env, g grant) tokenRequest {
				r := correct(g)
				r.form.Set("grant_type", "refresh_token")
				return r
			},
			status: http.StatusBadRequest, errCode: "unsupported_grant_type", codeAlive: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := fresh(t, e)
			status, errCode := e.postToken(t, tc.attempt(t, e, g))
			if status != tc.status || errCode != tc.errCode {
				t.Fatalf("got %d %q, want %d %q", status, errCode, tc.status, tc.errCode)
			}
			status, errCode = e.postToken(t, correct(g))
			if tc.codeAlive {
				if status != http.StatusOK {
					t.Errorf("honest exchange afterwards = %d %q, want 200", status, errCode)
				}
			} else if status != http.StatusBadRequest || errCode != "invalid_grant" {
				t.Errorf("honest exchange afterwards = %d %q, want 400 invalid_grant (code dead)", status, errCode)
			}
		})
	}
}

func TestProvider_Tamper(t *testing.T) {
	t.Run("Nonce", func(t *testing.T) {
		e := newEnv(t)
		e.p.SetTamper(oidcfake.Tamper{Nonce: "other-nonce"})
		idt, err := e.verifier().Verify(e.ctx, rawIDToken(t, e.signIn(t, oauth2.AuthStyleInParams, alice)))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if idt.Nonce != "other-nonce" {
			t.Errorf("nonce = %q, want other-nonce", idt.Nonce)
		}
	})
	for name, tamper := range map[string]oidcfake.Tamper{
		"Audience": {Audience: "other-client"},
		"Issuer":   {Issuer: "https://issuer.invalid"},
		"WrongKey": {WrongKey: true},
		"Expired":  {Expired: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.p.SetTamper(tamper)
			raw := rawIDToken(t, e.signIn(t, oauth2.AuthStyleInParams, alice))
			if _, err := e.verifier().Verify(e.ctx, raw); err == nil {
				t.Errorf("Verify accepted a token tampered with %+v", tamper)
			}
		})
	}
	t.Run("OmitIDToken", func(t *testing.T) {
		e := newEnv(t)
		e.p.SetTamper(oidcfake.Tamper{OmitIDToken: true})
		if got := e.signIn(t, oauth2.AuthStyleInParams, alice).Extra("id_token"); got != nil {
			t.Errorf("id_token = %v, want nil", got)
		}
	})
	t.Run("ExtraAudience", func(t *testing.T) {
		e := newEnv(t)
		e.p.SetTamper(oidcfake.Tamper{ExtraAudience: "other-client"})
		idt, err := e.verifier().Verify(e.ctx, rawIDToken(t, e.signIn(t, oauth2.AuthStyleInParams, alice)))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if len(idt.Audience) != 2 || idt.Audience[0] != testClientID || idt.Audience[1] != "other-client" {
			t.Errorf("audience = %q, want [%s other-client]", idt.Audience, testClientID)
		}
	})
	t.Run("OmitEmail", func(t *testing.T) {
		e := newEnv(t)
		e.p.SetTamper(oidcfake.Tamper{OmitEmail: true})
		idt, err := e.verifier().Verify(e.ctx, rawIDToken(t, e.signIn(t, oauth2.AuthStyleInParams, alice)))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		var all map[string]any
		if err := idt.Claims(&all); err != nil {
			t.Fatalf("Claims: %v", err)
		}
		if v, ok := all["email"]; ok {
			t.Errorf("email claim = %v, want none", v)
		}
	})
}

func TestApprove_Unverified(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn(t, oauth2.AuthStyleInParams, oidcfake.Identity{Email: alice.Email, Name: alice.Name, Unverified: true})
	idt, err := e.verifier().Verify(e.ctx, rawIDToken(t, tok))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var c claims
	if err := idt.Claims(&c); err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if c.EmailVerified {
		t.Errorf("email_verified = true, want false")
	}
}

func TestAuthPage_Form(t *testing.T) {
	e := newEnv(t)
	v := validAuthParams()
	v.Set("state", `x"><script>alert(1)</script>`)
	resp, err := e.srv.Client().Get(e.srv.URL + "/auth?" + v.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth = %d, want 200", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
		t.Errorf("Content-Security-Policy = %q, want none", csp)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	want := []string{
		`id="fake-signin"`,
		`rel="icon" href="data:,"`,
		`name="email"`,
		`name="name"`,
		`name="email_verified"`,
	}
	for _, p := range []string{"client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "code_challenge", "code_challenge_method"} {
		want = append(want, `type="hidden" name="`+p+`"`)
	}
	for _, w := range want {
		if !strings.Contains(page, w) {
			t.Errorf("page lacks %s", w)
		}
	}
	if strings.Contains(page, "<script>") {
		t.Errorf("state came back unescaped:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("escaped state missing from page:\n%s", page)
	}
}
