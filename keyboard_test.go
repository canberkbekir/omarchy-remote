package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func keyName(code uint16) string {
	for name, value := range keyCodes {
		if value == code {
			return name
		}
	}
	for _, button := range gamepadButtons {
		if button.code == code {
			return button.name
		}
	}
	return "unknown"
}

// recorder keeps the key events a real device would have received.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for offset := 0; offset < len(data); offset += 24 {
		event := data[offset:]
		eventType := binary.LittleEndian.Uint16(event[16:])
		code := binary.LittleEndian.Uint16(event[18:])
		value := binary.LittleEndian.Uint32(event[20:])
		switch eventType {
		case evKey:
			r.events = append(r.events, map[uint32]string{0: "up ", 1: "down "}[value]+keyName(code))
		case evRel:
			r.events = append(r.events, fmt.Sprintf("rel %d %d", code, int32(value)))
		case evAbs:
			r.events = append(r.events, fmt.Sprintf("abs %d %d", code, int32(value)))
		}
	}
	return len(data), nil
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recorder) waitFor(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Equal(r.list(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("events = %v, want %v", r.list(), want)
}

func startKeyboardServer(t *testing.T, timeout time.Duration) (*recorder, string) {
	t.Helper()
	events := &recorder{}
	a := &app{
		passcode:        []byte("secret"),
		guard:           &loginGuard{failures: map[string]attempt{}},
		keyboardTimeout: timeout,
		openKeyboard: func() (*keyboard, error) {
			return &keyboard{out: events, held: map[uint16]bool{}}, nil
		},
	}
	server := httptest.NewServer(http.HandlerFunc(a.handleKeyboard))
	t.Cleanup(server.Close)
	return events, "ws" + strings.TrimPrefix(server.URL, "http")
}

func connect(t *testing.T, url, passcode string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Write(ctx, websocket.MessageText, []byte(passcode)); err != nil {
		t.Fatal(err)
	}
	if _, reply, err := conn.Read(ctx); err != nil || string(reply) != "ok" {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
	return conn
}

func send(t *testing.T, conn *websocket.Conn, messages ...string) {
	t.Helper()
	for _, message := range messages {
		if err := conn.Write(context.Background(), websocket.MessageText, []byte(message)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHoldsSeveralKeysTogether(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "d leftctrl", "d leftshift", "d t", "u t", "u leftshift", "u leftctrl")
	events.waitFor(t, "down leftctrl", "down leftshift", "down t", "up t", "up leftshift", "up leftctrl")
}

func TestReleasesKeysWhenConnectionCloses(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "d leftshift")
	events.waitFor(t, "down leftshift")
	conn.CloseNow()
	events.waitFor(t, "down leftshift", "up leftshift")
}

func TestReleasesKeysWhenPhoneGoesQuiet(t *testing.T) {
	events, url := startKeyboardServer(t, 100*time.Millisecond)
	conn := connect(t, url, "secret")
	send(t, conn, "d leftctrl")
	events.waitFor(t, "down leftctrl")
	events.waitFor(t, "down leftctrl", "up leftctrl")
	_ = conn
}

func TestPingsKeepHeldKeysDown(t *testing.T) {
	events, url := startKeyboardServer(t, 150*time.Millisecond)
	conn := connect(t, url, "secret")
	send(t, conn, "d leftshift")
	for range 6 {
		time.Sleep(60 * time.Millisecond)
		send(t, conn, "p")
	}
	if got := events.list(); !slices.Equal(got, []string{"down leftshift"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestNewConnectionReleasesOldConnectionKeys(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
	first := connect(t, url, "secret")
	send(t, first, "d leftalt")
	events.waitFor(t, "down leftalt")
	connect(t, url, "secret")
	events.waitFor(t, "down leftalt", "up leftalt")
}

func TestRejectsWrongPasscodeAndUnknownKeys(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
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

	good := connect(t, url, "secret")
	send(t, good, "d sysrq", "x y", "d", "d a", "u a")
	events.waitFor(t, "down a", "up a")
}

func TestMovesPointerAndScrolls(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "m 0 -7", "m 3 0", "s 90", "m 0 0")
	events.waitFor(t, "rel 1 -7", "rel 0 3", "rel 11 90")
}

func TestLimitsPointerJumps(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "m 99999 0", "s -99999", "m a b", "m 1", "s x")
	events.waitFor(t, "rel 0 500", "rel 11 -1200")
}

func TestReleasesHeldClickWhenConnectionCloses(t *testing.T) {
	events, url := startKeyboardServer(t, time.Minute)
	conn := connect(t, url, "secret")
	send(t, conn, "d btnleft")
	events.waitFor(t, "down btnleft")
	conn.CloseNow()
	events.waitFor(t, "down btnleft", "up btnleft")
}
