package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/config"
)

func TestLoginGuardReserve(t *testing.T) {
	g := &loginGuard{failures: map[string]attempt{}}
	for i := range maxFailures {
		if !g.reserve("10.0.0.5") {
			t.Fatalf("try %d refused", i+1)
		}
	}
	if g.reserve("10.0.0.5") || !g.blocked("10.0.0.5") {
		t.Fatal("tries past the limit should be refused")
	}
	if !g.reserve("10.0.0.6") {
		t.Fatal("another device should still get tries")
	}
	g.clear("10.0.0.6")
	if got := g.failures["10.0.0.6"].count; got != 0 {
		t.Fatalf("clear left %d tries", got)
	}
}

func TestLoginGuardReserveIsAtomic(t *testing.T) {
	g := &loginGuard{failures: map[string]attempt{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.reserve("10.0.0.5") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != maxFailures {
		t.Fatalf("%d tries got through together, want %d", allowed, maxFailures)
	}
}

func TestLoginGuardForgetsQuietAddresses(t *testing.T) {
	g := &loginGuard{failures: map[string]attempt{}}
	old := time.Now().Add(-lockoutDuration - time.Second)
	for i := range 100 {
		g.failures["10.0.1."+strconv.Itoa(i)] = attempt{maxFailures, old}
	}
	g.recordFailure("10.0.0.5")
	if len(g.failures) != 1 {
		t.Fatalf("%d addresses kept, want only the new one", len(g.failures))
	}
}

func TestListenScope(t *testing.T) {
	for addr, want := range map[string][2]bool{
		"0.0.0.0:8377":     {false, true},
		":8377":            {false, true},
		"[::]:8377":        {false, true},
		"127.0.0.1:8377":   {true, false},
		"[::1]:8377":       {true, false},
		"localhost:8377":   {true, false},
		"100.64.0.1:8377":  {false, false},
		"192.168.0.2:8377": {false, false},
		"no-port":          {false, false},
	} {
		loopback, everywhere := config.ListenScope(addr)
		if loopback != want[0] || everywhere != want[1] {
			t.Errorf("%s = %v %v, want %v", addr, loopback, everywhere, want)
		}
	}
}

func TestAdminAddresses(t *testing.T) {
	handler := homeNetworkOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), true)
	for remote, want := range map[string]int{
		"192.168.0.20:1":        http.StatusOK,
		"127.0.0.1:1":           http.StatusOK,
		"100.68.188.36:1":       http.StatusOK,
		"[fd7a:115c:a1e0::9]:1": http.StatusOK,
		"203.0.113.5:1":         http.StatusForbidden,
		"[2001:db8::1]:1":       http.StatusForbidden,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s = %d, want %d", remote, w.Code, want)
		}
	}
}

func TestAddressHostOnly(t *testing.T) {
	handler := addressHostOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for host, want := range map[string]int{
		"192.168.0.2:8378":  http.StatusOK,
		"192.168.0.2":       http.StatusOK,
		"[fe80::1]:8378":    http.StatusOK,
		"localhost:8378":    http.StatusOK,
		"evil.example:8378": http.StatusMisdirectedRequest,
		"evil.example":      http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s = %d, want %d", host, w.Code, want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self';", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP allows inline code: %q", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers = %v", w.Header())
	}
}

func TestLoginGuardRefund(t *testing.T) {
	g := &loginGuard{failures: map[string]attempt{}}
	g.reserve("10.0.0.5")
	g.reserve("10.0.0.5")
	g.refund("10.0.0.5")
	if got := g.failures["10.0.0.5"].count; got != 1 {
		t.Fatalf("count after refund = %d, want 1", got)
	}
	g.refund("10.0.0.5")
	g.refund("10.0.0.5")
	if _, ok := g.failures["10.0.0.5"]; ok {
		t.Fatal("refunding every try should forget the address")
	}
}

func TestFailedHandshakeKeepsTries(t *testing.T) {
	a := &app{guard: &loginGuard{failures: map[string]attempt{}}, passcode: []byte("secret")}
	for range maxFailures * 2 {
		r := httptest.NewRequest("GET", "/api/keyboard", nil)
		r.RemoteAddr = "192.168.0.20:1"
		if a.acceptSocket(httptest.NewRecorder(), r) != nil {
			t.Fatal("a plain GET should not open a socket")
		}
	}
	if a.guard.blocked("192.168.0.20") {
		t.Fatal("failed handshakes locked the phone out")
	}
}
