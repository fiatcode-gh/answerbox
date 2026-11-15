package adminauth

import (
	"net/netip"
	"sync"
	"time"
)

// Login throttle schedule: four failures are free, then each further failure
// locks the address for 30 s doubling up to 15 min; an address idle for an
// hour is forgotten, and at most maxKeys addresses are remembered.
const (
	freeFailures = 4
	baseLock     = 30 * time.Second
	maxLock      = 15 * time.Minute
	idleAfter    = time.Hour
	maxKeys      = 4096
)

type throttleEntry struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

// throttle counts consecutive login failures per client key, in memory.
type throttle struct {
	mu      sync.Mutex
	entries map[string]*throttleEntry
}

// throttleKey is the IPv4 address, the /64 prefix of an IPv6 address, or
// "unknown" for an invalid address.
func throttleKey(ip netip.Addr) string {
	switch {
	case !ip.IsValid():
		return "unknown"
	case ip.Is4():
		return ip.String()
	default:
		return netip.PrefixFrom(ip.WithZone(""), 64).Masked().String()
	}
}

func (e *throttleEntry) idle(now time.Time) bool {
	return now.Sub(e.lastFailure) >= idleAfter
}

// entry returns the live state for key, forgetting it when idle.
func (t *throttle) entry(key string, now time.Time) *throttleEntry {
	e, ok := t.entries[key]
	if !ok {
		return nil
	}
	if e.idle(now) {
		delete(t.entries, key)
		return nil
	}
	return e
}

// admit returns the remaining lock time for key, or 0 after counting one
// failure in advance. Checking and counting under one lock means concurrent
// attempts cannot all pass the check before any of them is counted; a
// successful login then calls clear, which undoes the count.
func (t *throttle) admit(key string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entry(key, now)
	if e != nil && now.Before(e.lockedUntil) {
		return e.lockedUntil.Sub(now)
	}
	if e == nil {
		if t.entries == nil {
			t.entries = make(map[string]*throttleEntry)
		}
		e = &throttleEntry{}
		t.entries[key] = e
	}
	e.failures++
	e.lastFailure = now
	if e.failures > freeFailures {
		lock := maxLock
		if shift := e.failures - freeFailures - 1; shift < 5 {
			lock = min(baseLock<<shift, maxLock)
		}
		e.lockedUntil = now.Add(lock)
	}
	t.prune(key, now)
	return 0
}

// clear forgets key after a successful login.
func (t *throttle) clear(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// prune bounds the map: above maxKeys it drops idle entries, then the entry
// with the oldest last failure (never keep's) until the bound holds.
func (t *throttle) prune(keep string, now time.Time) {
	if len(t.entries) <= maxKeys {
		return
	}
	for k, e := range t.entries {
		if e.idle(now) {
			delete(t.entries, k)
		}
	}
	for len(t.entries) > maxKeys {
		oldest, oldestAt := "", time.Time{}
		for k, e := range t.entries {
			if k != keep && (oldest == "" || e.lastFailure.Before(oldestAt)) {
				oldest, oldestAt = k, e.lastFailure
			}
		}
		delete(t.entries, oldest)
	}
}
