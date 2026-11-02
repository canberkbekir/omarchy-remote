package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type countCloser struct{ closes atomic.Int32 }

func (c *countCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func partyApp(t *testing.T) (*app, *recorder, *countCloser) {
	t.Helper()
	events := &recorder{}
	closer := &countCloser{}
	a := &app{
		name:           "Test PC",
		party:          newParty(),
		guestGuard:     &loginGuard{failures: map[string]attempt{}},
		gamepadTimeout: time.Minute,
		openGuestPad: func() (*gamepad, error) {
			return &gamepad{out: events, closer: closer}, nil
		},
	}
	return a, events, closer
}

func joinRequest(a *app, code, name, remote string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	r := httptest.NewRequest("POST", "/api/join", strings.NewReader(string(body)))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	a.handleJoin(w, r)
	return w
}

func TestPartyCodeUsesOnlyClearLetters(t *testing.T) {
	for range 200 {
		code, err := randomCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 4 || strings.Trim(code, partyCodeLetters) != "" || strings.ContainsAny(code, "IO0123456789") {
			t.Fatalf("code %q", code)
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"  Ayşe  ":              "Ayşe",
		"Mert\n\tK.":            "Mert K.",
		"a‮evil":                "aevil",
		strings.Repeat("x", 40): strings.Repeat("x", 20),
		"\x00\x01":              "",
		"Çağrı   the   best":    "Çağrı the best",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinFlow(t *testing.T) {
	a, _, _ := partyApp(t)
	if w := joinRequest(a, "ABCD", "Ayşe", "192.168.0.20:1"); w.Code != http.StatusNotFound {
		t.Fatalf("no party: %d", w.Code)
	}
	if err := a.party.start(); err != nil {
		t.Fatal(err)
	}
	code := a.party.view().Code
	if w := joinRequest(a, code, "  ", "192.168.0.20:1"); w.Code != http.StatusBadRequest {
		t.Fatalf("empty name: %d", w.Code)
	}
	for i, slot := range []int{2, 3, 4} {
		w := joinRequest(a, strings.ToLower(code), "Player"+strconv.Itoa(i), "192.168.0.2"+strconv.Itoa(i)+":1")
		var reply struct {
			Token string
			Slot  int
			PC    string
		}
		json.Unmarshal(w.Body.Bytes(), &reply)
		if w.Code != http.StatusOK || reply.Slot != slot || len(reply.Token) != 32 || reply.PC != "Test PC" {
			t.Fatalf("join %d: %d %s", slot, w.Code, w.Body)
		}
	}
	if w := joinRequest(a, code, "Fifth", "192.168.0.30:1"); w.Code != http.StatusConflict {
		t.Fatalf("full: %d", w.Code)
	}

	// A freed slot is reused, and a new code keeps the players.
	a.party.remove(3)
	if err := a.party.newCode(); err != nil {
		t.Fatal(err)
	}
	if a.party.view().Code != code {
		if w := joinRequest(a, code, "Old code", "192.168.0.31:1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("old code should fail: %d", w.Code)
		}
	}
	if w := joinRequest(a, a.party.view().Code, "Back", "192.168.0.32:1"); !strings.Contains(w.Body.String(), `"slot":3`) {
		t.Fatalf("rejoin: %s", w.Body)
	}
	if got := len(a.party.view().Players); got != 3 {
		t.Errorf("players = %d", got)
	}
}

func TestJoinLimitsWrongCodes(t *testing.T) {
	a, _, _ := partyApp(t)
	a.party.start()
	code := a.party.view().Code
	wrong := "ZZZZ"
	if code == wrong {
		wrong = "YYYY"
	}
	for range maxFailures {
		if w := joinRequest(a, wrong, "Guess", "192.168.0.50:1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code: %d", w.Code)
		}
	}
	if w := joinRequest(a, code, "Guess", "192.168.0.50:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("same device should be locked out, got %d", w.Code)
	}
	if w := joinRequest(a, code, "Friend", "192.168.0.51:1"); w.Code != http.StatusOK {
		t.Fatalf("another device should still join, got %d", w.Code)
	}

	// Many devices guessing together hit the shared limit.
	for i := range maxJoinFailures {
		joinRequest(a, wrong, "Guess", "192.168.1."+strconv.Itoa(i)+":1")
	}
	if w := joinRequest(a, code, "Late", "192.168.0.99:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("shared limit: %d", w.Code)
	}
}

func TestHomeNetworkOnly(t *testing.T) {
	handler := homeNetworkOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for remote, want := range map[string]int{
		"192.168.0.20:1":  http.StatusOK,
		"10.1.2.3:1":      http.StatusOK,
		"127.0.0.1:1":     http.StatusOK,
		"[fe80::1]:1":     http.StatusOK,
		"203.0.113.5:1":   http.StatusForbidden,
		"100.68.188.36:1": http.StatusForbidden,
		"[2001:db8::1]:1": http.StatusForbidden,
		"not-an-address":  http.StatusForbidden,
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

func playURL(t *testing.T, a *app) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(a.handlePlay))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func joinToken(t *testing.T, a *app, name string) string {
	t.Helper()
	w := joinRequest(a, a.party.view().Code, name, "192.168.0.20:1")
	var reply struct{ Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Token == "" {
		t.Fatalf("join: %s", w.Body)
	}
	return reply.Token
}

func readClose(t *testing.T, conn *websocket.Conn) (websocket.StatusCode, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			var closeErr websocket.CloseError
			if ok := asCloseError(err, &closeErr); ok {
				return closeErr.Code, closeErr.Reason
			}
			t.Fatalf("read: %v", err)
		}
	}
}

func asCloseError(err error, target *websocket.CloseError) bool {
	for err != nil {
		if e, ok := err.(websocket.CloseError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestGuestPlaysAndIsRemoved(t *testing.T) {
	a, events, closer := partyApp(t)
	a.party.start()
	url := playURL(t, a)
	conn := connect(t, url, joinToken(t, a, "Ayşe"))
	send(t, conn, "g 1 0 0 0 0 0 0", "l 9")
	events.waitFor(t, "down a")
	deadline := time.Now().Add(2 * time.Second)
	for a.party.view().Players[0].Delay != 9 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p := a.party.view().Players[0]; !p.Connected || p.Delay != 9 || p.Name != "Ayşe" {
		t.Fatalf("player = %+v", p)
	}

	a.party.remove(2)
	if code, reason := readClose(t, conn); code != statusPartyOver || reason != "removed" {
		t.Fatalf("close = %d %q", code, reason)
	}
	events.waitFor(t, "down a", "up a")
	deadline = time.Now().Add(2 * time.Second)
	for closer.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if closer.closes.Load() != 1 {
		t.Errorf("controller should be unplugged once, got %d", closer.closes.Load())
	}
}

func TestGuestReconnectsAndPartyEnds(t *testing.T) {
	a, events, closer := partyApp(t)
	a.party.start()
	url := playURL(t, a)
	token := joinToken(t, a, "Mert")
	first := connect(t, url, token)
	send(t, first, "g 2 0 0 0 0 0 0")
	events.waitFor(t, "down b")

	// The same phone reconnecting takes over its own slot.
	second := connect(t, url, token)
	events.waitFor(t, "down b", "up b")
	if players := a.party.view().Players; len(players) != 1 || players[0].Slot != 2 || !players[0].Connected {
		t.Fatalf("players = %+v", players)
	}

	a.party.end()
	if code, reason := readClose(t, second); code != statusPartyOver || reason != "ended" {
		t.Fatalf("close = %d %q", code, reason)
	}
	if a.party.active() {
		t.Error("party should be over")
	}
	deadline := time.Now().Add(2 * time.Second)
	for closer.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	// A phone that still has its old token is told the party is over.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	late, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer late.CloseNow()
	send(t, late, token)
	if code, reason := readClose(t, late); code != statusPartyOver || reason != "ended" {
		t.Fatalf("late close = %d %q", code, reason)
	}
}

func TestGuestRemovedWhileAwayIsUnplugged(t *testing.T) {
	a, _, closer := partyApp(t)
	a.party.start()
	url := playURL(t, a)
	token := joinToken(t, a, "Away")
	conn := connect(t, url, token)
	conn.CloseNow()
	deadline := time.Now().Add(2 * time.Second)
	for len(a.party.view().Players) == 1 && a.party.view().Players[0].Connected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	a.party.remove(2)
	if closer.closes.Load() != 1 {
		t.Errorf("closes = %d", closer.closes.Load())
	}

	// When the phone comes back, it's told it was removed, not that the party ended.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	back, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer back.CloseNow()
	send(t, back, token)
	if code, reason := readClose(t, back); code != statusPartyOver || reason != "removed" {
		t.Fatalf("close = %d %q", code, reason)
	}
}

func TestGuestPortOpensOnlyDuringParty(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	a, _, _ := partyApp(t)
	a.guestPort = port
	a.themes = &themes{dir: t.TempDir(), name: "default"}
	t.Cleanup(a.stopGuestServer)
	change := func(action string) partyView {
		r := httptest.NewRequest("POST", "/api/party", strings.NewReader(`{"action":"`+action+`"}`))
		w := httptest.NewRecorder()
		a.handlePartyChange(w, r)
		var v partyView
		json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	get := func(path string) (int, string) {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if err != nil {
			return 0, ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, _ := get("/"); code != 0 {
		t.Fatalf("guest port should be closed before a party, got %d", code)
	}
	v := change("start")
	if !v.Active || len(v.Code) != 4 || !strings.HasSuffix(v.Link, "/#"+v.Code) || len(v.QR) < 21 {
		t.Fatalf("start = %+v", v)
	}
	if code, body := get("/"); code != http.StatusOK || !strings.Contains(body, "Join the game") {
		t.Fatalf("join page = %d", code)
	}
	for _, private := range []string{"/manifest.webmanifest", "/sw.js", "/api/status", "/api/party", "/api/keyboard"} {
		if code, _ := get(private); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("guest port must not serve %s, got %d", private, code)
		}
	}
	if code, _ := get("/gamepad.js"); code != http.StatusOK {
		t.Errorf("gamepad.js = %d", code)
	}
	change("end")
	if code, _ := get("/"); code != 0 {
		t.Fatalf("guest port should close when the party ends, got %d", code)
	}
}
