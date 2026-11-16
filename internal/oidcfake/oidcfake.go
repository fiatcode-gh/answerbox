// Package oidcfake is a fake OpenID provider for tests and local runs.
//
// It signs in any identity typed into its form, so it is test equipment
// only: never import it from cmd/go-exam.
package oidcfake

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

const (
	keyID       = "fake-1"
	maxBody     = 64 << 10
	codeTTL     = 60 * time.Second
	idTokenTTL  = time.Hour
	accessTTL   = 3600 // seconds, as sent in expires_in
	formFieldOn = "on"
)

type Config struct {
	ClientID, ClientSecret string
	RedirectURL            string           // the only redirect_uri accepted, compared exactly
	Now                    func() time.Time // nil means time.Now
}

// Identity is what the "user" types into the provider's form.
type Identity struct {
	Email, Name string
	Unverified  bool // email_verified=false in the ID token
}

// Tamper makes ID tokens dishonest; the zero value is honest.
type Tamper struct {
	Nonce, Audience, Issuer        string // non-empty replaces the claim
	ExtraAudience                  string // non-empty adds a second audience after the client ID
	WrongKey, Expired, OmitIDToken bool
	OmitEmail                      bool // drops the email claim
}

// Provider implements the authorization-code flow with PKCE on /auth and
// /token; discovery and keys come from an embedded oidctest.Server.
type Provider struct {
	cfg      Config
	key      *ecdsa.PrivateKey
	wrongKey *ecdsa.PrivateKey
	disc     *oidctest.Server

	mu         sync.Mutex
	issuer     string
	grants     map[string]grant
	tamper     Tamper
	tokenCalls int
}

type grant struct {
	email, name string
	verified    bool
	nonce       string
	challenge   string
	expires     time.Time
}

// New returns a provider with fresh ES256 keys. It panics if key generation fails.
func New(cfg Config) *Provider {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	key := newKey()
	return &Provider{
		cfg:      cfg,
		key:      key,
		wrongKey: newKey(),
		disc: &oidctest.Server{
			PublicKeys: []oidctest.PublicKey{{PublicKey: key.Public(), KeyID: keyID, Algorithm: oidc.ES256}},
			Algorithms: []string{oidc.ES256},
		},
		grants: map[string]grant{},
	}
}

func newKey() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("oidcfake: generating key: " + err.Error())
	}
	return k
}

// SetIssuer sets the issuer URL; call it before serving, e.g. with httptest.Server.URL.
func (p *Provider) SetIssuer(issuer string) {
	p.disc.SetIssuer(issuer)
	p.mu.Lock()
	p.issuer = issuer
	p.mu.Unlock()
}

// SetTamper changes how later ID tokens are made.
func (p *Provider) SetTamper(t Tamper) {
	p.mu.Lock()
	p.tamper = t
	p.mu.Unlock()
}

// TokenCalls reports the POST /token requests seen so far.
func (p *Provider) TokenCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenCalls
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/auth" && r.Method == http.MethodGet:
		p.authPage(w, r)
	case r.URL.Path == "/auth" && r.Method == http.MethodPost:
		p.authSubmit(w, r)
	case r.URL.Path == "/token" && r.Method == http.MethodPost:
		p.token(w, r)
	case r.URL.Path == "/auth" || r.URL.Path == "/token":
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		p.disc.ServeHTTP(w, r)
	}
}

// badAuthParam names the first invalid authorization parameter in v, or
// returns "" when the request is acceptable.
func (p *Provider) badAuthParam(v url.Values) string {
	switch {
	case v.Get("client_id") != p.cfg.ClientID:
		return "client_id"
	case v.Get("redirect_uri") != p.cfg.RedirectURL:
		return "redirect_uri"
	case v.Get("response_type") != "code":
		return "response_type"
	case !slices.Contains(strings.Fields(v.Get("scope")), oidc.ScopeOpenID):
		return "scope"
	case v.Get("state") == "":
		return "state"
	case v.Get("nonce") == "":
		return "nonce"
	case v.Get("code_challenge") == "":
		return "code_challenge"
	case v.Get("code_challenge_method") != "S256":
		return "code_challenge_method"
	}
	return ""
}

var authParams = []string{"client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "code_challenge", "code_challenge_method"}

type hiddenField struct{ Name, Value string }

var pageTmpl = template.Must(template.New("auth").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" href="data:,">
<title>Fake OpenID provider</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 28rem; margin: 3rem auto; padding: 0 1rem; }
label { display: block; margin: 0.75rem 0; }
input[type=email], input[type=text] { display: block; width: 100%; padding: 0.4rem; box-sizing: border-box; }
</style>
</head>
<body>
<h1>Fake OpenID provider</h1>
<p>Fake OpenID provider for tests. It signs in any identity.</p>
<form id="fake-signin" method="post" action="/auth">
{{range .}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}<label>Email <input type="email" name="email" required></label>
<label>Name <input type="text" name="name"></label>
<label><input type="checkbox" name="email_verified" checked> Email verified</label>
<button type="submit">Sign in</button>
</form>
</body>
</html>
`))

func (p *Provider) authPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if bad := p.badAuthParam(q); bad != "" {
		http.Error(w, "invalid parameter: "+bad, http.StatusBadRequest)
		return
	}
	fields := make([]hiddenField, len(authParams))
	for i, name := range authParams {
		fields[i] = hiddenField{name, q.Get(name)}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTmpl.Execute(w, fields)
}

func (p *Provider) authSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	if bad := p.badAuthParam(f); bad != "" {
		http.Error(w, "invalid parameter: "+bad, http.StatusBadRequest)
		return
	}
	if f.Get("email") == "" {
		http.Error(w, "invalid parameter: email", http.StatusBadRequest)
		return
	}

	code := randomString()
	now := p.cfg.Now()
	p.mu.Lock()
	for c, g := range p.grants {
		if !now.Before(g.expires) {
			delete(p.grants, c)
		}
	}
	p.grants[code] = grant{
		email:     f.Get("email"),
		name:      f.Get("name"),
		verified:  f.Has("email_verified"),
		nonce:     f.Get("nonce"),
		challenge: f.Get("code_challenge"),
		expires:   now.Add(codeTTL),
	}
	p.mu.Unlock()

	target, err := url.Parse(p.cfg.RedirectURL)
	if err != nil {
		http.Error(w, "invalid configured redirect URL", http.StatusInternalServerError)
		return
	}
	cq := target.Query()
	cq.Set("code", code)
	cq.Set("state", f.Get("state"))
	target.RawQuery = cq.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.tokenCalls++
	p.mu.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f := r.PostForm
	if !p.clientAuthenticated(r, f) {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if f.Get("grant_type") != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}

	now := p.cfg.Now()
	p.mu.Lock()
	code := f.Get("code")
	g, found := p.grants[code]
	delete(p.grants, code)
	tamper := p.tamper
	issuer := p.issuer
	p.mu.Unlock()

	if !found || !now.Before(g.expires) {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	verifier := f.Get("code_verifier")
	if f.Get("redirect_uri") != p.cfg.RedirectURL || verifier == "" || s256(verifier) != g.challenge {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}

	resp := map[string]any{
		"access_token": randomString(),
		"token_type":   "Bearer",
		"expires_in":   accessTTL,
	}
	if !tamper.OmitIDToken {
		resp["id_token"] = p.idToken(g, now, issuer, tamper)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// clientAuthenticated accepts HTTP Basic credentials, which are
// form-urlencoded before encoding (RFC 6749 §2.3.1), or the form fields
// client_id and client_secret.
func (p *Provider) clientAuthenticated(r *http.Request, f url.Values) bool {
	id, secret, basic := r.BasicAuth()
	if basic {
		var errID, errSecret error
		id, errID = url.QueryUnescape(id)
		secret, errSecret = url.QueryUnescape(secret)
		if errID != nil || errSecret != nil {
			return false
		}
	} else {
		id, secret = f.Get("client_id"), f.Get("client_secret")
	}
	return id == p.cfg.ClientID && secret == p.cfg.ClientSecret
}

func (p *Provider) idToken(g grant, now time.Time, issuer string, t Tamper) string {
	claims := map[string]any{
		"iss":            issuer,
		"aud":            p.cfg.ClientID,
		"sub":            "fake-" + g.email,
		"iat":            now.Unix(),
		"exp":            now.Add(idTokenTTL).Unix(),
		"nonce":          g.nonce,
		"email":          g.email,
		"email_verified": g.verified,
		"name":           g.name,
	}
	if t.Issuer != "" {
		claims["iss"] = t.Issuer
	}
	if t.Audience != "" {
		claims["aud"] = t.Audience
	}
	if t.ExtraAudience != "" {
		claims["aud"] = []string{p.cfg.ClientID, t.ExtraAudience}
	}
	if t.OmitEmail {
		delete(claims, "email")
	}
	if t.Nonce != "" {
		claims["nonce"] = t.Nonce
	}
	if t.Expired {
		claims["exp"] = now.Add(-idTokenTTL).Unix()
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		panic("oidcfake: encoding claims: " + err.Error())
	}
	key := p.key
	if t.WrongKey {
		key = p.wrongKey
	}
	return oidctest.SignIDToken(key, keyID, oidc.ES256, string(raw))
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

func tokenError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Approve plays the user: it posts id to the provider's form for authURL
// (the URL the application redirected to) and returns the callback URL the
// provider redirects to. client must not follow redirects for this call;
// Approve sets that itself on a shallow copy of client.
func Approve(ctx context.Context, client *http.Client, authURL string, id Identity) (callbackURL string, err error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", fmt.Errorf("oidcfake: approve: parse auth URL: %w", err)
	}
	form := u.Query()
	form.Set("email", id.Email)
	form.Set("name", id.Name)
	if !id.Unverified {
		form.Set("email_verified", formFieldOn)
	}
	target := *u
	target.RawQuery = ""
	target.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oidcfake: approve: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidcfake: approve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", fmt.Errorf("oidcfake: approve: provider answered %s: %q", resp.Status, body)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("oidcfake: approve: 303 without Location")
	}
	return loc, nil
}
