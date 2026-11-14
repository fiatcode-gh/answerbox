// Package clientip resolves the client address of a request, honouring
// X-Forwarded-For only when the peer is a trusted proxy.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver resolves client addresses. The zero value trusts no proxy.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver trusting the given proxy prefixes.
func New(trusted []netip.Prefix) Resolver {
	return Resolver{trusted: trusted}
}

// ClientIP returns the client address of req: the peer address, or, when the
// peer is a trusted proxy, the right-most X-Forwarded-For entry that is not
// itself trusted. An unparsable RemoteAddr yields the zero Addr.
func (r Resolver) ClientIP(req *http.Request) netip.Addr {
	remote, ok := parseAddr(hostPart(req.RemoteAddr))
	if !ok {
		return netip.Addr{}
	}
	if !r.isTrusted(remote) {
		return remote
	}
	entries := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	candidate := remote
	for i := len(entries) - 1; i >= 0; i-- {
		addr, ok := parseAddr(strings.TrimSpace(entries[i]))
		if !ok {
			break
		}
		candidate = addr
		if !r.isTrusted(addr) {
			break
		}
	}
	return candidate
}

func (r Resolver) isTrusted(addr netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func hostPart(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func parseAddr(s string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}
