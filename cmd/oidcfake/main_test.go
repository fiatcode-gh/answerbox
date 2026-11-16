package main

import "testing"

func TestLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:9096": true,
		"localhost:1":    true,
		"[::1]:80":       true,
		"0.0.0.0:9096":   false,
		":9096":          false,
		"192.168.1.5:80": false,
		"example.org:80": false,
	}
	for addr, want := range cases {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
