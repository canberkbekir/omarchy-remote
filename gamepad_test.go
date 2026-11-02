package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func startGamepadServer(t *testing.T, timeout time.Duration) (*recorder, string) {
	t.Helper()
	events := &recorder{}
	a := &app{
		passcode:       []byte("secret"),
		guard:          &loginGuard{failures: map[string]attempt{}},
		gamepadTimeout: timeout,
		openGamepad: func() (*gamepad, error) {
			return &gamepad{out: events}, nil
		},
	}
	server := httptest.NewServer(http.HandlerFunc(a.handleGamepad))
	t.Cleanup(server.Close)
	return events, "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestParsePadState(t *testing.T) {
	s, ok := parsePadState("g 3 100 -200 0 99999 255 -5")
	if !ok {
		t.Fatal("expected a valid state")
	}
	want := padState{buttons: 3, axes: [6]int32{100, -200, 0, stickMax, triggerMax, 0}}
	if s != want {
		t.Errorf("state = %+v, want %+v", s, want)
	}
	for _, bad := range []string{"g", "g 1 2 3", "x 0 0 0 0 0 0 0", "g a 0 0 0 0 0 0", "g 0 0 0 0 0 0 0 0", "g -1 0 0 0 0 0 0"} {
		if _, ok := parsePadState(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if s, _ := parsePadState("g 4294967295 0 0 0 0 0 0"); s.buttons != 1<<15-1 {
		t.Errorf("unknown button bits should be dropped, got %b", s.buttons)
	}
}

func TestGamepadSendsOnlyChanges(t *testing.T) {
	events, url := startGamepadServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn,
		"g 1 0 0 0 0 0 0",               // A down
		"g 1 0 0 0 0 0 0",               // nothing changed
		"g 0 16000 -32767 0 0 255 0",    // A up, left stick, left trigger
		"g 2048 16000 -32767 0 0 255 0", // D-pad up
		"g 6144 16000 -32767 0 0 255 0", // D-pad up and down: cancels out
	)
	events.waitFor(t,
		"down a",
		"up a", "abs 0 16000", "abs 1 -32767", "abs 2 255",
		"abs 17 -1",
		"abs 17 0",
	)
}

func TestGamepadResetsWhenConnectionCloses(t *testing.T) {
	events, url := startGamepadServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "g 16 0 32767 0 0 0 0")
	events.waitFor(t, "down lb", "abs 1 32767")
	conn.CloseNow()
	events.waitFor(t, "down lb", "abs 1 32767", "up lb", "abs 1 0")
}

func TestGamepadResetsWhenPhoneGoesQuiet(t *testing.T) {
	events, url := startGamepadServer(t, 100*time.Millisecond)
	conn := connect(t, url, "secret")
	send(t, conn, "g 8192 0 0 0 0 0 0")
	events.waitFor(t, "abs 16 -1", "abs 16 0")
	_ = conn
}

func TestGamepadEchoesDelayPings(t *testing.T) {
	_, url := startGamepadServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "t 12345")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, reply, err := conn.Read(ctx); err != nil || string(reply) != "t 12345" {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
}

func TestGamepadNewPhoneTakesOver(t *testing.T) {
	events, url := startGamepadServer(t, time.Minute)
	first := connect(t, url, "secret")
	send(t, first, "g 1 0 0 0 0 0 0")
	events.waitFor(t, "down a")
	connect(t, url, "secret")
	events.waitFor(t, "down a", "up a")
}

func TestGamepadRejectsWrongPasscode(t *testing.T) {
	_, url := startGamepadServer(t, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	send(t, conn, "wrong")
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("err = %v", err)
	}
}

func TestGamepadLayoutSaveAndRead(t *testing.T) {
	a := &app{dir: t.TempDir()}
	read := func() string {
		w := httptest.NewRecorder()
		a.handleLayoutRead(w, httptest.NewRequest("GET", "/", nil))
		return strings.TrimSpace(w.Body.String())
	}
	if got := read(); got != `{"version":1,"controls":{}}` {
		t.Errorf("empty layout = %s", got)
	}

	body := `{"version":1,"controls":{"a":{"x":80,"y":60,"size":120,"hidden":false},"l3":{"x":10,"y":10,"size":100,"hidden":true}}}`
	w := httptest.NewRecorder()
	a.handleLayoutSave(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body)
	}
	if got := read(); got != body {
		t.Errorf("read back = %s", got)
	}
	info, err := os.Stat(filepath.Join(a.dir, "gamepad.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("layout file = %v, %v", info, err)
	}
}

func TestGamepadLayoutRejects(t *testing.T) {
	a := &app{dir: t.TempDir()}
	for _, body := range []string{
		`{"version":2,"controls":{}}`,
		`{"version":1,"controls":{"zz":{"x":1,"y":1,"size":100}}}`,
		`{"version":1,"controls":{"a":{"x":101,"y":1,"size":100}}}`,
		`{"version":1,"controls":{"a":{"x":1,"y":1,"size":20}}}`,
		`not json`,
		`{"version":1,"controls":{"a":{"x":1,"y":1,"size":100,"pad":"` + strings.Repeat("x", 9000) + `"}}}`,
	} {
		w := httptest.NewRecorder()
		a.handleLayoutSave(w, httptest.NewRequest("POST", "/", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%.40s… = %d, want 400", body, w.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(a.dir, "gamepad.json")); !os.IsNotExist(err) {
		t.Error("a rejected layout must not be written")
	}
}
