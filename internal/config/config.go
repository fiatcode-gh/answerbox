// Package config reads the process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

// GoogleIssuer is the OpenID issuer used when ANSWERBOX_OIDC_ISSUER is empty.
const GoogleIssuer = "https://accounts.google.com"

const (
	prefix = "ANSWERBOX_"
	// oldPrefix is the settings prefix from before the rename to Answerbox.
	// Any key with it refuses the start, so a stale environment is never
	// half read.
	oldPrefix = "GOEXAM_"
)

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

// Load reads the configuration from environ, the process environment in the
// form os.Environ returns.
func Load(environ []string) (Config, error) {
	vars := parseEnviron(environ)
	if err := refuseOldSettings(vars); err != nil {
		return Config{}, err
	}
	getenv := func(key string) string { return vars[key] }

	cfg := Config{
		DataDir:    valueOr(getenv("ANSWERBOX_DATA_DIR"), "./data"),
		ListenAddr: valueOr(getenv("ANSWERBOX_LISTEN_ADDR"), ":8080"),
	}
	if err := loadSignIn(&cfg, getenv); err != nil {
		return Config{}, err
	}

	proxies, err := parsePrefixes(getenv("ANSWERBOX_TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, err
	}
	cfg.TrustedProxies = proxies

	zone := valueOr(getenv("ANSWERBOX_TIME_ZONE"), "UTC")
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Config{}, fmt.Errorf("ANSWERBOX_TIME_ZONE: unknown time zone %q", zone)
	}
	cfg.Location = loc
	return cfg, nil
}

// parseEnviron splits KEY=VALUE entries into a map. An entry without "=" or
// with an empty key is skipped; the first entry of a key wins, as with
// os.Getenv.
func parseEnviron(environ []string) map[string]string {
	vars := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, seen := vars[key]; !seen {
			vars[key] = value
		}
	}
	return vars
}

// refuseOldSettings returns an error naming every key with oldPrefix and its
// new name, sorted by key. Values are never read.
func refuseOldSettings(vars map[string]string) error {
	var old []string
	for key := range vars {
		if strings.HasPrefix(key, oldPrefix) {
			old = append(old, key)
		}
	}
	if len(old) == 0 {
		return nil
	}
	slices.Sort(old)
	pairs := make([]string, len(old))
	for i, key := range old {
		pairs[i] = key + " to " + prefix + strings.TrimPrefix(key, oldPrefix)
	}
	return errors.New("the app is now Answerbox and its settings start with " + prefix +
		": rename " + strings.Join(pairs, ", ") + "; it does not start while a " + oldPrefix + " variable is set")
}

// loadSignIn reads and checks the Google sign-in settings. Error messages are
// fixed strings and never quote a value, so the client secret cannot leak.
func loadSignIn(cfg *Config, getenv func(string) string) error {
	cfg.GoogleClientID = strings.TrimSpace(getenv("ANSWERBOX_GOOGLE_CLIENT_ID"))
	if cfg.GoogleClientID == "" {
		return errors.New("ANSWERBOX_GOOGLE_CLIENT_ID must be set")
	}
	cfg.GoogleClientSecret = strings.TrimSpace(getenv("ANSWERBOX_GOOGLE_CLIENT_SECRET"))
	if cfg.GoogleClientSecret == "" {
		return errors.New("ANSWERBOX_GOOGLE_CLIENT_SECRET must be set")
	}
	for entry := range strings.SplitSeq(getenv("ANSWERBOX_ADMIN_EMAILS"), ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			cfg.AdminEmails = append(cfg.AdminEmails, entry)
		}
	}
	if len(cfg.AdminEmails) == 0 {
		return errors.New("ANSWERBOX_ADMIN_EMAILS must list at least one email address")
	}

	public := strings.TrimSpace(getenv("ANSWERBOX_PUBLIC_URL"))
	if public == "" {
		return errors.New("ANSWERBOX_PUBLIC_URL must be set, for example https://exam.example.org")
	}
	u, ok := parseHTTPURL(public)
	if !ok || (u.Path != "" && u.Path != "/") {
		return errors.New("ANSWERBOX_PUBLIC_URL: must be an http(s) address without a path, for example https://exam.example.org")
	}
	if !secureOrLoopback(u) {
		return errors.New("ANSWERBOX_PUBLIC_URL: must use https unless the host is localhost")
	}
	cfg.PublicURL = u.Scheme + "://" + u.Host

	cfg.OIDCIssuer = valueOr(strings.TrimSpace(getenv("ANSWERBOX_OIDC_ISSUER")), GoogleIssuer)
	u, ok = parseHTTPURL(cfg.OIDCIssuer)
	if !ok {
		return errors.New("ANSWERBOX_OIDC_ISSUER: must be an http(s) URL")
	}
	if !secureOrLoopback(u) {
		return errors.New("ANSWERBOX_OIDC_ISSUER: must use https unless the host is localhost")
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
			return nil, fmt.Errorf("ANSWERBOX_TRUSTED_PROXIES: invalid entry %q", entry)
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
