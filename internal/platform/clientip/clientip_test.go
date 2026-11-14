package clientip

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::/8"),
	}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string // empty = zero Addr
	}{
		{"untrusted remote ignores XFF", "198.51.100.1:5000", []string{"1.2.3.4"}, "198.51.100.1"},
		{"trusted remote no XFF", "10.0.0.2:5000", nil, "10.0.0.2"},
		{"trusted remote takes right-most", "10.0.0.2:5000", []string{"1.2.3.4, 203.0.113.7"}, "203.0.113.7"},
		{"trusted chain skips trusted hops", "10.0.0.2:5000", []string{"203.0.113.7, 10.0.0.9"}, "203.0.113.7"},
		{"two header lines are joined", "10.0.0.2:5000", []string{"1.2.3.4", "203.0.113.7, 10.0.0.9"}, "203.0.113.7"},
		{"garbage entry stops the walk", "10.0.0.2:5000", []string{"1.2.3.4, garbage, 10.0.0.9"}, "10.0.0.9"},
		{"garbage right-most keeps remote", "10.0.0.2:5000", []string{"1.2.3.4, garbage"}, "10.0.0.2"},
		{"all-trusted chain returns left-most", "10.0.0.2:5000", []string{"10.1.1.1, 10.0.0.9"}, "10.1.1.1"},
		{"IPv4-mapped remote is unmapped", "[::ffff:198.51.100.1]:5000", []string{"1.2.3.4"}, "198.51.100.1"},
		{"IPv4-mapped trusted remote", "[::ffff:10.0.0.2]:5000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"IPv6 trusted remote", "[fd00::1]:5000", []string{"2001:db8::1"}, "2001:db8::1"},
		{"zone dropped", "[fe80::1%eth0]:5000", nil, "fe80::1"},
		{"unparsable remote", "nonsense", []string{"1.2.3.4"}, ""},
	}
	r := New(trusted)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			got := r.ClientIP(req)
			var want netip.Addr
			if tt.want != "" {
				want = netip.MustParseAddr(tt.want)
			}
			if got != want {
				t.Fatalf("ClientIP = %v, want %v", got, want)
			}
		})
	}
}

func TestClientIP_NoTrustedProxies(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := New(nil).ClientIP(req); got != netip.MustParseAddr("10.0.0.2") {
		t.Fatalf("ClientIP = %v, want 10.0.0.2", got)
	}
}
