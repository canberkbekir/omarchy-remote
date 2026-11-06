package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/canberkbekir/omarchy-remote/internal/input"
)

func startGamepadServer(t *testing.T, timeout time.Duration) (*recorder, string) {
	t.Helper()
	events, _, url := startGamepadServerWithMouse(t, timeout)
	return events, url
}

func startGamepadServerWithMouse(t *testing.T, timeout time.Duration) (*recorder, *recorder, string) {
	t.Helper()
	events, mouse := &recorder{}, &recorder{}
	a := &app{
		passcode:       []byte("secret"),
		guard:          &loginGuard{failures: map[string]attempt{}},
		gamepadTimeout: timeout,
		openGamepad: func() (*input.Gamepad, error) {
			return input.NewGamepad(events, nil), nil
		},
		openKeyboard: func() (*input.Keyboard, error) {
			return input.NewKeyboard(mouse), nil
		},
	}
	server := httptest.NewServer(http.HandlerFunc(a.handleGamepad))
	t.Cleanup(server.Close)
	return events, mouse, "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestGamepadSwipeMovesTheMouse(t *testing.T) {
	events, mouse, url := startGamepadServerWithMouse(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "m 12 0", "m 0 -5", "m 99999 0", "m 1", "g 1 0 0 0 0 0 0")
	events.waitFor(t, "down a")
	mouse.waitFor(t, "rel 0 12", "rel 1 -5", "rel 0 500")
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

	body := `{"version":1,"controls":{"a":{"x":80,"y":60,"size":120,"hidden":false},"l3":{"x":10,"y":10,"size":100,"hidden":true},"rs":{"x":64,"y":75,"size":100,"hidden":false,"aim":"swipe","speed":150}}}`
	w := httptest.NewRecorder()
	a.handleLayoutSave(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body)
	}
	if got := read(); got != body {
		t.Errorf("read back = %s", got)
	}
	info, err := os.Stat(filepath.Join(a.dir, "gamepad.json"))
	if err != nil {
		t.Fatalf("layout file: %v", err)
	}
	// Windows keeps files private with ACLs, not mode bits.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("layout file mode = %v", info.Mode().Perm())
	}
}

func TestGamepadLayoutRejects(t *testing.T) {
	a := &app{dir: t.TempDir()}
	for _, body := range []string{
		`{"version":2,"controls":{}}`,
		`{"version":1,"controls":{"zz":{"x":1,"y":1,"size":100}}}`,
		`{"version":1,"controls":{"a":{"x":101,"y":1,"size":100}}}`,
		`{"version":1,"controls":{"a":{"x":1,"y":1,"size":20}}}`,
		`{"version":1,"controls":{"a":{"x":1,"y":1,"size":100,"aim":"swipe","speed":100}}}`,
		`{"version":1,"controls":{"rs":{"x":1,"y":1,"size":100,"aim":"gyro","speed":100}}}`,
		`{"version":1,"controls":{"rs":{"x":1,"y":1,"size":100,"aim":"swipe","speed":1000}}}`,
		`{"version":1,"controls":{"rs":{"x":1,"y":1,"size":100,"aim":"swipe"}}}`,
		`{"version":1,"controls":{"rs":{"x":1,"y":1,"size":100,"speed":100}}}`,
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
