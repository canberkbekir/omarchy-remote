package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/cli"
)

func TestConnectTickets(t *testing.T) {
	now := time.Unix(1000, 0)
	tickets := newConnectTickets()
	tickets.now = func() time.Time { return now }

	first, err := tickets.mint()
	if err != nil || len(first) < 40 {
		t.Fatalf("mint() = %q, %v", first, err)
	}
	second, _ := tickets.mint()
	if tickets.use(first) {
		t.Error("an older ticket still works after a new one")
	}
	if tickets.use("") || tickets.use(second+"x") {
		t.Error("a wrong ticket works")
	}
	if !tickets.use(second) {
		t.Fatal("a fresh ticket fails")
	}
	if tickets.use(second) {
		t.Error("a ticket works twice")
	}

	third, _ := tickets.mint()
	now = now.Add(connectTicketLife + time.Second)
	if tickets.use(third) {
		t.Error("an expired ticket works")
	}
	if tickets.use("") {
		t.Error("an empty ticket works with none minted")
	}
}

func TestHandleConnect(t *testing.T) {
	a := &app{
		name:     "Den",
		passcode: []byte("1234567812345678"),
		tickets:  newConnectTickets(),
		connectChoices: func() []cli.ConnectChoice {
			return []cli.ConnectChoice{
				{Label: "Anywhere", URL: "https://den.tail1234.ts.net", QR: []string{"1"}},
				{Label: "At home", URL: "http://192.168.1.5:8377", QR: []string{"1"}},
			}
		},
	}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/connect", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		a.handleConnect(w, r)
		return w
	}

	ticket, _ := a.tickets.mint()
	w := post(`{"ticket":"` + ticket + `"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	var info connectInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "Den" || info.Passcode != "1234567812345678" || len(info.Addresses) != 2 ||
		info.Addresses[0].URL != "https://den.tail1234.ts.net" || info.Addresses[1].Label != "At home" {
		t.Errorf("info = %+v", info)
	}

	if w := post(`{"ticket":"` + ticket + `"}`); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "1234") {
		t.Errorf("reused ticket: status %d: %s", w.Code, w.Body)
	}
	if w := post(`not json`); w.Code != http.StatusBadRequest {
		t.Errorf("bad body: status %d", w.Code)
	}
	if w := post(`{}`); w.Code != http.StatusForbidden {
		t.Errorf("no ticket: status %d", w.Code)
	}

	ticket, _ = a.tickets.mint()
	w = httptest.NewRecorder()
	a.handleConnect(w, httptest.NewRequest("POST", "/api/connect", strings.NewReader(`{"ticket":"`+ticket+`"}`)))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: status %d", w.Code)
	}
	if !a.tickets.use(ticket) {
		t.Error("a refused text/plain request spent the ticket")
	}

	none := &app{}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/connect", strings.NewReader(`{"ticket":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	none.handleConnect(w, r)
	if w.Code != http.StatusForbidden || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("no ticket store: status %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestHandleConnectCountsFailures(t *testing.T) {
	a := &app{tickets: newConnectTickets(), guard: &loginGuard{failures: map[string]attempt{}}}
	last := 0
	for range 50 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/connect", strings.NewReader(`{"ticket":"guess"}`))
		r.Header.Set("Content-Type", "application/json")
		a.handleConnect(w, r)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after 50 wrong tickets: status %d, want 429", last)
	}
}
