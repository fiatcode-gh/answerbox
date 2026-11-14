// Package config reads the process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
)

// MinPasswordLength is the minimum admin password length in characters.
const MinPasswordLength = 12

// Config is the process configuration.
type Config struct {
	AdminPassword, DataDir, ListenAddr string
	TrustedProxies                     []netip.Prefix
	Location                           *time.Location
}

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		AdminPassword: getenv("GOEXAM_ADMIN_PASSWORD"),
		DataDir:       valueOr(getenv("GOEXAM_DATA_DIR"), "./data"),
		ListenAddr:    valueOr(getenv("GOEXAM_LISTEN_ADDR"), ":8080"),
	}
	if utf8.RuneCountInString(cfg.AdminPassword) < MinPasswordLength {
		return Config{}, errors.New("GOEXAM_ADMIN_PASSWORD must be set and at least 12 characters long")
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
