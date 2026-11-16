package config

import (
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"
	_ "time/tzdata" // zone lookups must not depend on the host's zoneinfo
)

const (
	errClientID     = "GOEXAM_GOOGLE_CLIENT_ID must be set"
	errClientSecret = "GOEXAM_GOOGLE_CLIENT_SECRET must be set"
	errAdminEmails  = "GOEXAM_ADMIN_EMAILS must list at least one email address"
	errPublicURLSet = "GOEXAM_PUBLIC_URL must be set, for example https://exam.example.org"
	errPublicURL    = "GOEXAM_PUBLIC_URL: must be an http(s) address without a path, for example https://exam.example.org"
	errPublicHTTPS  = "GOEXAM_PUBLIC_URL: must use https unless the host is localhost"
	errIssuer       = "GOEXAM_OIDC_ISSUER: must be an http(s) URL"
	errIssuerHTTPS  = "GOEXAM_OIDC_ISSUER: must use https unless the host is localhost"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// baseEnv returns the minimum valid environment merged with extra; an extra
// value of "" removes the key.
func baseEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"GOEXAM_GOOGLE_CLIENT_ID":     "client-id",
		"GOEXAM_GOOGLE_CLIENT_SECRET": "client-secret",
		"GOEXAM_ADMIN_EMAILS":         "admin@example.test",
		"GOEXAM_PUBLIC_URL":           "https://exam.example.org",
	}
	for k, v := range extra {
		if v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(env(baseEnv(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("DataDir = %q, want ./data", cfg.DataDir)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want empty", cfg.TrustedProxies)
	}
	if cfg.Location == nil || cfg.Location.String() != "UTC" {
		t.Errorf("Location = %v, want UTC", cfg.Location)
	}
	if cfg.GoogleClientID != "client-id" || cfg.GoogleClientSecret != "client-secret" {
		t.Errorf("GoogleClientID/GoogleClientSecret not as given")
	}
	if cfg.OIDCIssuer != "https://accounts.google.com" {
		t.Errorf("OIDCIssuer = %q, want https://accounts.google.com", cfg.OIDCIssuer)
	}
	if cfg.PublicURL != "https://exam.example.org" {
		t.Errorf("PublicURL = %q, want https://exam.example.org", cfg.PublicURL)
	}
	if want := []string{"admin@example.test"}; !slices.Equal(cfg.AdminEmails, want) {
		t.Errorf("AdminEmails = %q, want %q", cfg.AdminEmails, want)
	}

	// A blank issuer (an .env line without a value) means the default.
	cfg, err = Load(env(baseEnv(map[string]string{"GOEXAM_OIDC_ISSUER": "  "})))
	if err != nil {
		t.Fatalf("Load with blank issuer: %v", err)
	}
	if cfg.OIDCIssuer != "https://accounts.google.com" {
		t.Errorf("blank issuer: OIDCIssuer = %q, want https://accounts.google.com", cfg.OIDCIssuer)
	}
}

func TestLoad_Values(t *testing.T) {
	cfg, err := Load(env(baseEnv(map[string]string{
		"GOEXAM_DATA_DIR":        "/data",
		"GOEXAM_LISTEN_ADDR":     "127.0.0.1:9000",
		"GOEXAM_TRUSTED_PROXIES": " 10.0.0.0/8, 192.168.1.5 ,fd00::1",
		"GOEXAM_TIME_ZONE":       "Asia/Jakarta",
		"GOEXAM_ADMIN_EMAILS":    " Head@Example.Test , ,second@example.test,",
		"GOEXAM_PUBLIC_URL":      "http://localhost:8080/",
		"GOEXAM_OIDC_ISSUER":     "http://127.0.0.1:9096",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "/data" || cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("DataDir/ListenAddr = %q/%q", cfg.DataDir, cfg.ListenAddr)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.168.1.5/32"),
		netip.MustParsePrefix("fd00::1/128"),
	}
	if !slices.Equal(cfg.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	if cfg.Location.String() != "Asia/Jakarta" {
		t.Errorf("Location = %v", cfg.Location)
	}
	if want := []string{"Head@Example.Test", "second@example.test"}; !slices.Equal(cfg.AdminEmails, want) {
		t.Errorf("AdminEmails = %q, want %q", cfg.AdminEmails, want)
	}
	if cfg.PublicURL != "http://localhost:8080" {
		t.Errorf("PublicURL = %q, want http://localhost:8080", cfg.PublicURL)
	}
	if cfg.OIDCIssuer != "http://127.0.0.1:9096" {
		t.Errorf("OIDCIssuer = %q, want http://127.0.0.1:9096", cfg.OIDCIssuer)
	}

	// An issuer path is kept byte for byte.
	cfg, err = Load(env(baseEnv(map[string]string{"GOEXAM_OIDC_ISSUER": "https://id.example.org/realms/x"})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDCIssuer != "https://id.example.org/realms/x" {
		t.Errorf("OIDCIssuer = %q, want https://id.example.org/realms/x", cfg.OIDCIssuer)
	}
}

type errorCase struct {
	name string
	env  map[string]string
	want string // empty = success
}

func errorCases() []errorCase {
	return []errorCase{
		{"minimum environment", baseEnv(nil), ""},
		{"bad proxy", baseEnv(map[string]string{"GOEXAM_TRUSTED_PROXIES": "10.0.0.0/8,nope"}),
			`GOEXAM_TRUSTED_PROXIES: invalid entry "nope"`},
		{"bad zone", baseEnv(map[string]string{"GOEXAM_TIME_ZONE": "Mars/Base"}),
			`GOEXAM_TIME_ZONE: unknown time zone "Mars/Base"`},

		{"missing client id", baseEnv(map[string]string{"GOEXAM_GOOGLE_CLIENT_ID": ""}), errClientID},
		{"blank client id", baseEnv(map[string]string{"GOEXAM_GOOGLE_CLIENT_ID": "  "}), errClientID},
		{"missing client secret", baseEnv(map[string]string{"GOEXAM_GOOGLE_CLIENT_SECRET": ""}), errClientSecret},
		{"blank client secret", baseEnv(map[string]string{"GOEXAM_GOOGLE_CLIENT_SECRET": "  "}), errClientSecret},
		{"missing admin emails", baseEnv(map[string]string{"GOEXAM_ADMIN_EMAILS": ""}), errAdminEmails},
		{"blank admin emails", baseEnv(map[string]string{"GOEXAM_ADMIN_EMAILS": "  "}), errAdminEmails},
		{"empty admin email entries", baseEnv(map[string]string{"GOEXAM_ADMIN_EMAILS": " , "}), errAdminEmails},
		{"missing public url", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": ""}), errPublicURLSet},
		{"blank public url", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "  "}), errPublicURLSet},

		{"public url without scheme", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "exam.example.org"}), errPublicURL},
		{"public url ftp", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "ftp://exam.example.org"}), errPublicURL},
		{"public url with path", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "https://exam.example.org/app"}), errPublicURL},
		{"public url with query", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "https://exam.example.org?x=1"}), errPublicURL},
		{"public url with user info", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "https://u:p@exam.example.org"}), errPublicURL},
		{"public url plain http", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "http://exam.example.org"}), errPublicHTTPS},
		{"public url http ::1", baseEnv(map[string]string{"GOEXAM_PUBLIC_URL": "http://[::1]:8080"}), ""},

		{"issuer without scheme", baseEnv(map[string]string{"GOEXAM_OIDC_ISSUER": "accounts.google.com"}), errIssuer},
		{"issuer with query", baseEnv(map[string]string{"GOEXAM_OIDC_ISSUER": "https://id.example.org?x=1"}), errIssuer},
		{"issuer plain http", baseEnv(map[string]string{"GOEXAM_OIDC_ISSUER": "http://id.example.org"}), errIssuerHTTPS},
		{"issuer http localhost", baseEnv(map[string]string{"GOEXAM_OIDC_ISSUER": "http://localhost:9096"}), ""},
	}
}

func TestLoad_Errors(t *testing.T) {
	for _, tt := range errorCases() {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoad_ErrorOrder(t *testing.T) {
	m := map[string]string{}
	steps := []struct {
		add, value, want string
	}{
		{"", "", errClientID},
		{"GOEXAM_GOOGLE_CLIENT_ID", "client-id", errClientSecret},
		{"GOEXAM_GOOGLE_CLIENT_SECRET", "client-secret", errAdminEmails},
		{"GOEXAM_ADMIN_EMAILS", "admin@example.test", errPublicURLSet},
	}
	for _, s := range steps {
		if s.add != "" {
			m[s.add] = s.value
		}
		_, err := Load(env(maps.Clone(m)))
		if err == nil || err.Error() != s.want {
			t.Fatalf("after adding %q: err = %v, want %q", s.add, err, s.want)
		}
	}
}

func TestLoad_ErrorsDoNotLeakSecret(t *testing.T) {
	checked := 0
	for _, tt := range errorCases() {
		if tt.want == "" || tt.env["GOEXAM_GOOGLE_CLIENT_SECRET"] != "client-secret" {
			continue
		}
		_, err := Load(env(tt.env))
		if err == nil {
			t.Fatalf("%s: Load succeeded", tt.name)
		}
		if strings.Contains(err.Error(), "client-secret") {
			t.Errorf("%s: error quotes the client secret: %v", tt.name, err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no failing case carried the client secret")
	}
}
