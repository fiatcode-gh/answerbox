package adminauth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is what a verified ID token says about the person signing in.
type Identity struct {
	Email         string
	EmailVerified bool
	Name          string
}

// ProviderConfig describes the OpenID provider and this application's client.
type ProviderConfig struct {
	Issuer, ClientID, ClientSecret, RedirectURL string
	HTTPClient                                  *http.Client     // discovery, keys and the token exchange
	Now                                         func() time.Time // token expiry check
	Random                                      io.Reader
}

// Flow holds the secrets of one sign-in between its start and its callback.
type Flow struct{ State, Nonce, Verifier string }

// FlowError is a sign-in step that failed. Reason and Detail are fixed words
// that are safe to log; Err is kept for tests and is never part of Error().
type FlowError struct {
	Reason string // discovery | state | exchange | id_token | nonce | claims
	Detail string // exchange only: "<http status> <oauth error code>", else ""
	Err    error
}

func (e *FlowError) Error() string {
	msg := "adminauth: sign-in failed: " + e.Reason
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

func (e *FlowError) Unwrap() error { return e.Err }

// Provider runs the authorization-code flow (with nonce and PKCE) against
// one OpenID provider. Discovery happens on first use and is retried after
// a failure, so the application starts even when the provider is unreachable.
type Provider struct {
	cfg ProviderConfig

	mu         sync.Mutex
	discovered *discovered
}

type discovered struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewProvider returns a Provider for cfg without contacting the provider.
func NewProvider(cfg ProviderConfig) *Provider {
	return &Provider{cfg: cfg}
}

func (p *Provider) discover(ctx context.Context) (*discovered, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discovered != nil {
		return p.discovered, nil
	}
	op, err := oidc.NewProvider(oidc.ClientContext(ctx, p.cfg.HTTPClient), p.cfg.Issuer)
	if err != nil {
		return nil, &FlowError{Reason: "discovery", Err: err}
	}
	ep := op.Endpoint()
	ep.AuthStyle = oauth2.AuthStyleInParams
	p.discovered = &discovered{
		oauth: &oauth2.Config{
			ClientID:     p.cfg.ClientID,
			ClientSecret: p.cfg.ClientSecret,
			RedirectURL:  p.cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
			Endpoint:     ep,
		},
		verifier: op.Verifier(&oidc.Config{ClientID: p.cfg.ClientID, Now: p.cfg.Now}),
	}
	return p.discovered, nil
}

// Begin starts a sign-in: it returns the flow secrets to keep until the
// callback and the provider URL to send the browser to.
func (p *Provider) Begin(ctx context.Context) (flow Flow, authURL string, err error) {
	d, err := p.discover(ctx)
	if err != nil {
		return Flow{}, "", err
	}
	for _, v := range []*string{&flow.State, &flow.Nonce, &flow.Verifier} {
		if *v, err = randomToken(p.cfg.Random); err != nil {
			return Flow{}, "", err
		}
	}
	authURL = d.oauth.AuthCodeURL(flow.State,
		oidc.Nonce(flow.Nonce),
		oauth2.S256ChallengeOption(flow.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"))
	return flow, authURL, nil
}

var oauthErrorCode = regexp.MustCompile(`^[a-z_]{1,40}$`)

// Complete turns the callback's state and code into a verified identity.
// An unverified email is returned as such; refusing it is the caller's job.
func (p *Provider) Complete(ctx context.Context, flow Flow, state, code string) (Identity, error) {
	if flow.State == "" || flow.Nonce == "" || flow.Verifier == "" ||
		subtle.ConstantTimeCompare([]byte(state), []byte(flow.State)) != 1 {
		return Identity{}, &FlowError{Reason: "state"}
	}
	d, err := p.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	if code == "" {
		return Identity{}, &FlowError{Reason: "exchange"}
	}
	token, err := d.oauth.Exchange(oidc.ClientContext(ctx, p.cfg.HTTPClient), code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		fe := &FlowError{Reason: "exchange", Err: err}
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil {
			errCode := re.ErrorCode
			if !oauthErrorCode.MatchString(errCode) {
				errCode = "other"
			}
			fe.Detail = fmt.Sprintf("%d %s", re.Response.StatusCode, errCode)
		}
		return Identity{}, fe
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, &FlowError{Reason: "id_token", Err: errors.New("no id_token in token response")}
	}
	idToken, err := d.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, &FlowError{Reason: "id_token", Err: err}
	}
	if len(idToken.Audience) != 1 {
		return Identity{}, &FlowError{Reason: "id_token", Err: errors.New("more than one audience")}
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
		return Identity{}, &FlowError{Reason: "nonce"}
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, &FlowError{Reason: "claims", Err: err}
	}
	if claims.Email == "" {
		return Identity{}, &FlowError{Reason: "claims", Err: errors.New("no email claim")}
	}
	return Identity{Email: claims.Email, EmailVerified: claims.EmailVerified, Name: claims.Name}, nil
}

// randomToken returns 32 random bytes, base64url without padding, so tokens
// are unguessable and safe in a cookie or URL.
func randomToken(r io.Reader) (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
