package main

import (
	"net/http/httptest"
	"testing"
)

func TestClientAddress(t *testing.T) {
	tests := []struct {
		name, remote string
		forwarded    []string
		want         string
	}{
		{"direct", "192.168.0.20:5000", nil, "192.168.0.20"},
		{"direct ignores forwarded", "192.168.0.20:5000", []string{"100.64.0.9"}, "192.168.0.20"},
		{"proxy without header", "127.0.0.1:5000", nil, "127.0.0.1"},
		{"proxy", "127.0.0.1:5000", []string{"100.64.0.9"}, "100.64.0.9"},
		{"proxy over ipv6", "[::1]:5000", []string{"fd7a:115c:a1e0::9"}, "fd7a:115c:a1e0::9"},
		{"proxy appends to forged entry", "127.0.0.1:5000", []string{"1.2.3.4, 100.64.0.9"}, "100.64.0.9"},
		{"proxy with second header", "127.0.0.1:5000", []string{"1.2.3.4", "100.64.0.9"}, "100.64.0.9"},
		{"proxy with junk", "127.0.0.1:5000", []string{"not-an-ip"}, "127.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = test.remote
			for _, value := range test.forwarded {
				r.Header.Add("X-Forwarded-For", value)
			}
			if got := clientAddress(r); got != test.want {
				t.Errorf("clientAddress = %q, want %q", got, test.want)
			}
		})
	}
}
