package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/coder/websocket"
)

const (
	evAbs = 3

	absX     = 0x00
	absY     = 0x01
	absZ     = 0x02
	absRX    = 0x03
	absRY    = 0x04
	absRZ    = 0x05
	absHat0X = 0x10
	absHat0Y = 0x11

	// ioctl numbers from linux/uinput.h.
	uiSetAbsBit = 0x40045567
	uiAbsSetup  = 0x401c5504

	busUSB = 0x03

	stickMax   = 32767
	triggerMax = 255

	// The phone resends its state four times a second while connected, so a
	// dropped connection centers the sticks and lets go of buttons quickly.
	gamepadTimeout   = time.Second
	maxLayoutBytes   = 8 << 10
	gamepadLayoutVer = 1
)

// gamepadButtons is the order of the bits in the phone's button mask, as Linux
// key codes for an Xbox 360 controller. The D-pad bits become the hat axes.
var gamepadButtons = []struct {
	name string
	code uint16
}{
	{"a", 0x130}, {"b", 0x131}, {"x", 0x133}, {"y", 0x134},
	{"lb", 0x136}, {"rb", 0x137}, {"back", 0x13a}, {"start", 0x13b},
	{"home", 0x13c}, {"l3", 0x13d}, {"r3", 0x13e},
}

const (
	dpadUp = 1 << (11 + iota)
	dpadDown
	dpadLeft
	dpadRight
)

// gamepadAxes are reported in this order after the button mask.
var gamepadAxes = []struct {
	code     uint16
	min, max int32
}{
	{absX, -stickMax, stickMax}, {absY, -stickMax, stickMax},
	{absRX, -stickMax, stickMax}, {absRY, -stickMax, stickMax},
	{absZ, 0, triggerMax}, {absRZ, 0, triggerMax},
}

type padState struct {
	buttons uint32
	axes    [6]int32 // left x, left y, right x, right y, left trigger, right trigger
}

func (s padState) hat() (x, y int32) {
	x = boolInt(s.buttons&dpadRight != 0) - boolInt(s.buttons&dpadLeft != 0)
	y = boolInt(s.buttons&dpadDown != 0) - boolInt(s.buttons&dpadUp != 0)
	return x, y
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// parsePadState reads "g <buttons> <lx> <ly> <rx> <ry> <lt> <rt>". Values out
// of range are clamped, so a bad phone can't send anything a real pad couldn't.
func parsePadState(message string) (padState, bool) {
	fields := strings.Fields(message)
	if len(fields) != 8 || fields[0] != "g" {
		return padState{}, false
	}
	buttons, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil {
		return padState{}, false
	}
	s := padState{buttons: uint32(buttons) & (1<<15 - 1)}
	for i, axis := range gamepadAxes {
		value, err := strconv.ParseInt(fields[2+i], 10, 32)
		if err != nil {
			return padState{}, false
		}
		s.axes[i] = int32(min(max(value, int64(axis.min)), int64(axis.max)))
	}
	return s, true
}

type gamepad struct {
	mu     sync.Mutex
	out    io.Writer
	closer io.Closer // removes the controller from the PC; nil in tests
	state  padState
}

// close unplugs the controller. Games see it disconnect.
func (g *gamepad) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closer != nil {
		g.closer.Close()
		g.closer = nil
	}
}

// apply sends only what changed since the last state, then one sync.
func (g *gamepad) apply(next padState) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	prev := g.state
	var buf []byte
	var event [24]byte
	add := func(eventType, code uint16, value int32) {
		writeEvent(event[:], eventType, code, value)
		buf = append(buf, event[:]...)
	}
	for i, button := range gamepadButtons {
		bit := uint32(1) << i
		if prev.buttons&bit != next.buttons&bit {
			add(evKey, button.code, boolInt(next.buttons&bit != 0))
		}
	}
	for i, axis := range gamepadAxes {
		if prev.axes[i] != next.axes[i] {
			add(evAbs, axis.code, next.axes[i])
		}
	}
	prevX, prevY := prev.hat()
	nextX, nextY := next.hat()
	if prevX != nextX {
		add(evAbs, absHat0X, nextX)
	}
	if prevY != nextY {
		add(evAbs, absHat0Y, nextY)
	}
	if len(buf) == 0 {
		return nil
	}
	add(evSyn, 0, 0)
	if _, err := g.out.Write(buf); err != nil {
		return err
	}
	g.state = next
	return nil
}

func (g *gamepad) reset() {
	if err := g.apply(padState{}); err != nil {
		log.Printf("resetting gamepad: %v", err)
	}
}

func openVirtualGamepad() (*gamepad, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*gamepad, error) {
		f.Close()
		return nil, err
	}
	fd := f.Fd()
	for _, eventType := range []uintptr{evKey, evAbs} {
		if err := ioctl(fd, uiSetEvBit, eventType); err != nil {
			return fail(err)
		}
	}
	for _, button := range gamepadButtons {
		if err := ioctl(fd, uiSetKeyBit, uintptr(button.code)); err != nil {
			return fail(err)
		}
	}
	// struct uinput_abs_setup: a code, padding, then struct input_absinfo
	// (value, minimum, maximum, fuzz, flat, resolution).
	setupAxis := func(code uint16, low, high, fuzz, flat int32) error {
		if err := ioctl(fd, uiSetAbsBit, uintptr(code)); err != nil {
			return err
		}
		var setup [28]byte
		binary.LittleEndian.PutUint16(setup[0:], code)
		binary.LittleEndian.PutUint32(setup[8:], uint32(low))
		binary.LittleEndian.PutUint32(setup[12:], uint32(high))
		binary.LittleEndian.PutUint32(setup[16:], uint32(fuzz))
		binary.LittleEndian.PutUint32(setup[20:], uint32(flat))
		return ioctl(fd, uiAbsSetup, uintptr(unsafe.Pointer(&setup)))
	}
	for _, axis := range gamepadAxes {
		fuzz, flat := int32(16), int32(128)
		if axis.max == triggerMax {
			fuzz, flat = 0, 0
		}
		if err := setupAxis(axis.code, axis.min, axis.max, fuzz, flat); err != nil {
			return fail(err)
		}
	}
	for _, code := range []uint16{absHat0X, absHat0Y} {
		if err := setupAxis(code, -1, 1, 0, 0); err != nil {
			return fail(err)
		}
	}

	// The IDs of a wired Xbox 360 controller, so Steam, SDL and games map the
	// buttons without any setup.
	var setup [92]byte
	binary.LittleEndian.PutUint16(setup[0:], busUSB)
	binary.LittleEndian.PutUint16(setup[2:], 0x045e)
	binary.LittleEndian.PutUint16(setup[4:], 0x028e)
	binary.LittleEndian.PutUint16(setup[6:], 0x0114)
	copy(setup[8:88], "PC Remote Gamepad")
	if err := ioctl(fd, uiDevSetup, uintptr(unsafe.Pointer(&setup))); err != nil {
		return fail(err)
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return fail(err)
	}
	// Give games a moment to pick up the new controller before the first press.
	time.Sleep(500 * time.Millisecond)
	return &gamepad{out: f, closer: f}, nil
}

func (a *app) gamepadDevice() (*gamepad, error) {
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
	playGamepad(session, conn, device, a.gamepadTimeout, nil)
}

// playGamepad runs one phone's controller until the phone leaves, goes quiet
// or the session is ended. It takes "g ..." state messages, answers "t ..."
// with the same text so the phone can measure the delay, and passes on the
// delay the phone reports with "l <ms>". Anything else keeps the session alive.
func playGamepad(session context.Context, conn *websocket.Conn, device *gamepad, timeout time.Duration, onDelay func(int)) {
	device.reset()
	defer device.reset()
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
		case strings.HasPrefix(text, "l "):
			if ms, err := strconv.Atoi(text[2:]); err == nil && onDelay != nil {
				onDelay(min(max(ms, 0), 9999))
			}
		default:
			if state, ok := parsePadState(text); ok {
				if err := device.apply(state); err != nil {
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
	Mode   string  `json:"mode,omitempty"` // the right stick only: "swipe" or "stick"
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
		if c.Mode != "" && (id != "rs" || (c.Mode != "swipe" && c.Mode != "stick")) {
			return false
		}
		if c.Mode != "" && (id != "rs" || (c.Mode != "swipe" && c.Mode != "stick")) {
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
