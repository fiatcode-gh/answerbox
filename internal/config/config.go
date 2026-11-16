// Package config reads the process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// GoogleIssuer is the OpenID issuer used when GOEXAM_OIDC_ISSUER is empty.
const GoogleIssuer = "https://accounts.google.com"

// Config is the process configuration.
type Config struct {
	DataDir, ListenAddr string
	TrustedProxies      []netip.Prefix
	Location            *time.Location

	GoogleClientID, GoogleClientSecret string
	OIDCIssuer                         string   // provider issuer, exactly as configured
	PublicURL                          string   // scheme://host[:port], no trailing slash
	AdminEmails                        []string // trimmed, empty entries dropped, order kept
}

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		DataDir:    valueOr(getenv("GOEXAM_DATA_DIR"), "./data"),
		ListenAddr: valueOr(getenv("GOEXAM_LISTEN_ADDR"), ":8080"),
	}
	if err := loadSignIn(&cfg, getenv); err != nil {
		return Config{}, err
	}

	proxies, err := parsePrefixes(getenv("GOEXAM_TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, err
	}
	cfg.TrustedProxies = proxies

	zone := valueOr(getenv("GOEXAM_TIME_ZONE"), "UTC")
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Config{}, fmt.Errorf("GOEXAM_TIME_ZONE: unknown time zone %q", zone)
	}
	cfg.Location = loc
	return cfg, nil
}

// loadSignIn reads and checks the Google sign-in settings. Error messages are
// fixed strings and never quote a value, so the client secret cannot leak.
func loadSignIn(cfg *Config, getenv func(string) string) error {
	cfg.GoogleClientID = strings.TrimSpace(getenv("GOEXAM_GOOGLE_CLIENT_ID"))
	if cfg.GoogleClientID == "" {
		return errors.New("GOEXAM_GOOGLE_CLIENT_ID must be set")
	}
	cfg.GoogleClientSecret = strings.TrimSpace(getenv("GOEXAM_GOOGLE_CLIENT_SECRET"))
	if cfg.GoogleClientSecret == "" {
		return errors.New("GOEXAM_GOOGLE_CLIENT_SECRET must be set")
	}
	for entry := range strings.SplitSeq(getenv("GOEXAM_ADMIN_EMAILS"), ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			cfg.AdminEmails = append(cfg.AdminEmails, entry)
		}
	}
	if len(cfg.AdminEmails) == 0 {
		return errors.New("GOEXAM_ADMIN_EMAILS must list at least one email address")
	}

	public := strings.TrimSpace(getenv("GOEXAM_PUBLIC_URL"))
	if public == "" {
		return errors.New("GOEXAM_PUBLIC_URL must be set, for example https://exam.example.org")
	}
	u, ok := parseHTTPURL(public)
	if !ok || (u.Path != "" && u.Path != "/") {
		return errors.New("GOEXAM_PUBLIC_URL: must be an http(s) address without a path, for example https://exam.example.org")
	}
	if !secureOrLoopback(u) {
		return errors.New("GOEXAM_PUBLIC_URL: must use https unless the host is localhost")
	}
	cfg.PublicURL = u.Scheme + "://" + u.Host

	cfg.OIDCIssuer = valueOr(strings.TrimSpace(getenv("GOEXAM_OIDC_ISSUER")), GoogleIssuer)
	u, ok = parseHTTPURL(cfg.OIDCIssuer)
	if !ok {
		return errors.New("GOEXAM_OIDC_ISSUER: must be an http(s) URL")
	}
	if !secureOrLoopback(u) {
		return errors.New("GOEXAM_OIDC_ISSUER: must use https unless the host is localhost")
	}
	return nil
}

// parseHTTPURL parses an absolute http or https URL with a host and without
// user info, query or fragment. The path is left for the caller to judge.
func parseHTTPURL(s string) (*url.URL, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" ||
		u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(s, "#") {
		return nil, false
	}
	return u, true
}

// secureOrLoopback reports whether u uses https or points at the local host.
func secureOrLoopback(u *url.URL) bool {
	if u.Scheme == "https" {
		return true
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// parsePrefixes parses a comma-separated list of CIDRs or bare IPs; a bare
// IP becomes a single-address prefix.
func parsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for entry := range strings.SplitSeq(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if p, err := netip.ParsePrefix(entry); err == nil {
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("GOEXAM_TRUSTED_PROXIES: invalid entry %q", entry)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
