package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/canberkbekir/omarchy-remote/internal/input"
)

const (

	// The phone resends its state four times a second while connected, so a
	// dropped connection centers the sticks and lets go of buttons quickly.
	gamepadTimeout   = time.Second
	maxLayoutBytes   = 8 << 10
	gamepadLayoutVer = 1
)

func (a *app) gamepadDevice() (*input.Gamepad, error) {
	a.gamepadMu.Lock()
	defer a.gamepadMu.Unlock()
	if a.gamepad == nil {
		device, err := a.openGamepad()
		if err != nil {
			return nil, err
		}
		a.gamepad = device
	}
	return a.gamepad, nil
}

// startGamepadSession ends any older session, so one phone is player 1.
func (a *app) startGamepadSession(cancel context.CancelFunc) {
	a.gamepadMu.Lock()
	defer a.gamepadMu.Unlock()
	if a.endGamepadSession != nil {
		a.endGamepadSession()
	}
	a.endGamepadSession = cancel
}

// handleGamepad is the admin's controller, always player 1.
func (a *app) handleGamepad(w http.ResponseWriter, r *http.Request) {
	conn := a.acceptSocket(w, r)
	if conn == nil {
		return
	}
	defer conn.CloseNow()

	device, err := a.gamepadDevice()
	if err != nil {
		log.Printf("opening gamepad: %v", err)
		conn.Close(websocket.StatusInternalError, "Could not create gamepad.")
		return
	}
	session, end := context.WithCancel(context.Background())
	defer end()
	a.startGamepadSession(end)
	// Swipe-to-aim moves the PC's mouse. Only the admin's controller may; a
	// guest's never does.
	mouse := func(message string) {
		k, err := a.keyboardDevice()
		if err == nil {
			err = k.HandleMessage(message)
		}
		if err != nil {
			log.Printf("moving the mouse: %v", err)
		}
	}
	playGamepad(session, conn, device, a.gamepadTimeout, nil, mouse)
}

// playGamepad runs one phone's controller until the phone leaves, goes quiet
// or the session is ended. It takes "g ..." state messages, answers "t ..."
// with the same text so the phone can measure the delay, and passes on the
// delay the phone reports with "l <ms>". Swipe-to-aim's mouse moves,
// "m <dx> <dy>", go to onMouse, or nowhere when it is nil. Anything else keeps
// the session alive.
func playGamepad(session context.Context, conn *websocket.Conn, device *input.Gamepad, timeout time.Duration, onDelay func(int), onMouse func(string)) {
	device.Reset()
	defer device.Reset()
	if err := conn.Write(session, websocket.MessageText, []byte("ok")); err != nil {
		return
	}
	for {
		readContext, cancelRead := context.WithTimeout(session, timeout)
		_, message, err := conn.Read(readContext)
		cancelRead()
		if err != nil {
			return
		}
		text := string(message)
		switch {
		case strings.HasPrefix(text, "t "):
			if err := conn.Write(session, websocket.MessageText, message); err != nil {
				return
			}
		case strings.HasPrefix(text, "m "):
			if onMouse != nil {
				onMouse(text)
			}
		case strings.HasPrefix(text, "l "):
			if ms, err := strconv.Atoi(text[2:]); err == nil && onDelay != nil {
				onDelay(min(max(ms, 0), 9999))
			}
		default:
			if state, ok := input.ParsePadState(text); ok {
				if err := device.Apply(state); err != nil {
					log.Printf("sending gamepad state: %v", err)
					return
				}
			}
		}
	}
}

// gamepadLayout is ~/.config/pc-remote/gamepad.json: where each control sits,
// as a share of the screen, so one layout fits any phone.
type gamepadLayout struct {
	Version  int                      `json:"version"`
	Controls map[string]controlLayout `json:"controls"`
}

type controlLayout struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Size   float64 `json:"size"`
	Hidden bool    `json:"hidden"`
	// The right stick only: Aim is "stick" or "swipe", which aims by moving
	// the mouse at Speed percent.
	Aim   string  `json:"aim,omitempty"`
	Speed float64 `json:"speed,omitempty"`
}

// layoutControls must match the controls the page draws.
var layoutControls = map[string]bool{
	"lt": true, "lb": true, "rb": true, "rt": true, "ls": true, "rs": true, "dpad": true,
	"back": true, "home": true, "start": true, "l3": true, "r3": true,
	"a": true, "b": true, "x": true, "y": true,
}

func (l gamepadLayout) valid() bool {
	if l.Version != gamepadLayoutVer || len(l.Controls) > len(layoutControls) {
		return false
	}
	inRange := func(v, low, high float64) bool { return !math.IsNaN(v) && v >= low && v <= high }
	for id, c := range l.Controls {
		if !layoutControls[id] || !inRange(c.X, 0, 100) || !inRange(c.Y, 0, 100) || !inRange(c.Size, 60, 160) {
			return false
		}
		if (c.Aim != "" || c.Speed != 0) && (id != "rs" || (c.Aim != "stick" && c.Aim != "swipe") || !inRange(c.Speed, 25, 400)) {
			return false
		}
	}
	return true
}

func (a *app) layoutPath() string {
	return filepath.Join(a.dir, "gamepad.json")
}

func (a *app) handleLayoutRead(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(a.layoutPath())
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, gamepadLayout{Version: gamepadLayoutVer, Controls: map[string]controlLayout{}})
		return
	}
	var layout gamepadLayout
	if err == nil {
		err = json.Unmarshal(data, &layout)
	}
	if err != nil || !layout.valid() {
		log.Printf("reading %s: not a valid layout", a.layoutPath())
		writeJSON(w, http.StatusOK, gamepadLayout{Version: gamepadLayoutVer, Controls: map[string]controlLayout{}})
		return
	}
	writeJSON(w, http.StatusOK, layout)
}

// handleLayoutSave writes the whole layout; the rename keeps a half-written
// file from ever replacing a good one.
func (a *app) handleLayoutSave(w http.ResponseWriter, r *http.Request) {
	var layout gamepadLayout
	if !decodeBodyLimit(w, r, &layout, maxLayoutBytes) {
		return
	}
	if !layout.valid() {
		writeError(w, http.StatusBadRequest, "Not a valid layout.")
		return
	}
	data, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the layout.")
		return
	}
	temp := a.layoutPath() + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the layout.")
		return
	}
	if err := os.Rename(temp, a.layoutPath()); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the layout.")
		return
	}
	writeJSON(w, http.StatusOK, layout)
}
