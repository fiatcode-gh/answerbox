package config

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	_ "time/tzdata" // zone lookups must not depend on the host's zoneinfo
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"GOEXAM_ADMIN_PASSWORD": "123456789012"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdminPassword != "123456789012" {
		t.Errorf("AdminPassword = %q", cfg.AdminPassword)
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
}

func TestLoad_Values(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"GOEXAM_ADMIN_PASSWORD":  "a-long-password",
		"GOEXAM_DATA_DIR":        "/data",
		"GOEXAM_LISTEN_ADDR":     "127.0.0.1:9000",
		"GOEXAM_TRUSTED_PROXIES": " 10.0.0.0/8, 192.168.1.5 ,fd00::1",
		"GOEXAM_TIME_ZONE":       "Asia/Jakarta",
	}))
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
}

func TestLoad_Errors(t *testing.T) {
	const pwErr = "GOEXAM_ADMIN_PASSWORD must be set and at least 12 characters long"
	okPW := "123456789012"
	tests := []struct {
		name string
		env  map[string]string
		want string // empty = success
	}{
		{"missing password", map[string]string{}, pwErr},
		{"11 characters", map[string]string{"GOEXAM_ADMIN_PASSWORD": "12345678901"}, pwErr},
		{"12 characters", map[string]string{"GOEXAM_ADMIN_PASSWORD": okPW}, ""},
		{"bad proxy", map[string]string{"GOEXAM_ADMIN_PASSWORD": okPW, "GOEXAM_TRUSTED_PROXIES": "10.0.0.0/8,nope"},
			`GOEXAM_TRUSTED_PROXIES: invalid entry "nope"`},
		{"bad zone", map[string]string{"GOEXAM_ADMIN_PASSWORD": okPW, "GOEXAM_TIME_ZONE": "Mars/Base"},
			`GOEXAM_TIME_ZONE: unknown time zone "Mars/Base"`},
	}
	for _, tt := range tests {
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
	// Multibyte password: length counts characters, not bytes.
	if _, err := Load(env(map[string]string{"GOEXAM_ADMIN_PASSWORD": strings.Repeat("é", 11)})); err == nil {
		t.Error("11 multibyte characters accepted")
	}
}
