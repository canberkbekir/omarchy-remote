package main

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/coder/websocket"
)

const (
	evSyn = 0
	evKey = 1
	evRel = 2

	relX          = 0
	relY          = 1
	relWheel      = 8
	relWheelHiRes = 11

	// ioctl numbers from linux/uinput.h.
	uiSetEvBit  = 0x40045564
	uiSetKeyBit = 0x40045565
	uiSetRelBit = 0x40045566
	uiDevSetup  = 0x405c5503
	uiDevCreate = 0x5501

	busVirtual = 0x06

	maxMove   = 500
	maxScroll = 1200

	// Keys are released if the phone goes quiet for this long, so a dropped
	// connection can't leave one held down. The phone pings every second.
	keyboardTimeout     = 3 * time.Second
	keyboardAuthTimeout = 5 * time.Second
)

// keyCodes lists every key the phone may press, as Linux input key codes.
var keyCodes = map[string]uint16{
	"esc": 1, "1": 2, "2": 3, "3": 4, "4": 5, "5": 6, "6": 7, "7": 8, "8": 9, "9": 10, "0": 11,
	"minus": 12, "equal": 13, "backspace": 14, "tab": 15,
	"q": 16, "w": 17, "e": 18, "r": 19, "t": 20, "y": 21, "u": 22, "i": 23, "o": 24, "p": 25,
	"leftbrace": 26, "rightbrace": 27, "enter": 28, "leftctrl": 29,
	"a": 30, "s": 31, "d": 32, "f": 33, "g": 34, "h": 35, "j": 36, "k": 37, "l": 38,
	"semicolon": 39, "apostrophe": 40, "grave": 41, "leftshift": 42, "backslash": 43,
	"z": 44, "x": 45, "c": 46, "v": 47, "b": 48, "n": 49, "m": 50,
	"comma": 51, "dot": 52, "slash": 53, "rightshift": 54, "leftalt": 56, "space": 57, "capslock": 58,
	"f1": 59, "f2": 60, "f3": 61, "f4": 62, "f5": 63, "f6": 64, "f7": 65, "f8": 66, "f9": 67, "f10": 68,
	"btnleft": 0x110, "btnright": 0x111, "f11": 87, "f12": 88, "rightctrl": 97, "home": 102, "up": 103, "pageup": 104, "left": 105,
	"right": 106, "end": 107, "down": 108, "pagedown": 109, "delete": 111, "leftmeta": 125,
}

type keyboard struct {
	mu   sync.Mutex
	out  io.Writer
	held map[uint16]bool
}

func writeEvent(buf []byte, eventType, code uint16, value int32) {
	// struct input_event on 64-bit Linux: a 16-byte timestamp, then type, code, value.
	binary.LittleEndian.PutUint16(buf[16:], eventType)
	binary.LittleEndian.PutUint16(buf[18:], code)
	binary.LittleEndian.PutUint32(buf[20:], uint32(value))
}

func (k *keyboard) send(code uint16, value int32) error {
	var buf [48]byte
	writeEvent(buf[:24], evKey, code, value)
	writeEvent(buf[24:], evSyn, 0, 0)
	_, err := k.out.Write(buf[:])
	return err
}

func (k *keyboard) press(code uint16) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.held[code] {
		return nil
	}
	if err := k.send(code, 1); err != nil {
		return err
	}
	k.held[code] = true
	return nil
}

func (k *keyboard) release(code uint16) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.held[code] {
		return nil
	}
	delete(k.held, code)
	return k.send(code, 0)
}

func (k *keyboard) releaseAll() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for code := range k.held {
		if err := k.send(code, 0); err != nil {
			log.Printf("releasing key %d: %v", code, err)
		}
		delete(k.held, code)
	}
}

func clamp(value, limit int) int32 {
	return int32(min(max(value, -limit), limit))
}

// sendRelative writes pointer motion, skipping axes that didn't change.
func (k *keyboard) sendRelative(axes map[uint16]int32) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	buf := make([]byte, 0, 24*(len(axes)+1))
	var event [24]byte
	for axis, value := range axes {
		if value == 0 {
			continue
		}
		writeEvent(event[:], evRel, axis, value)
		buf = append(buf, event[:]...)
	}
	if len(buf) == 0 {
		return nil
	}
	writeEvent(event[:], evSyn, 0, 0)
	_, err := k.out.Write(append(buf, event[:]...))
	return err
}

func ioctl(fd, request, value uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, value); errno != 0 {
		return errno
	}
	return nil
}

func openVirtualKeyboard() (*keyboard, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	for _, eventType := range []uintptr{evKey, evRel} {
		if err := ioctl(fd, uiSetEvBit, eventType); err != nil {
			f.Close()
			return nil, err
		}
	}
	for _, axis := range []uintptr{relX, relY, relWheel, relWheelHiRes} {
		if err := ioctl(fd, uiSetRelBit, axis); err != nil {
			f.Close()
			return nil, err
		}
	}
	for _, code := range keyCodes {
		if err := ioctl(fd, uiSetKeyBit, uintptr(code)); err != nil {
			f.Close()
			return nil, err
		}
	}

	// struct uinput_setup: bustype, vendor, product, version, then an 80-byte name.
	var setup [92]byte
	binary.LittleEndian.PutUint16(setup[0:], busVirtual)
	binary.LittleEndian.PutUint16(setup[6:], 1)
	copy(setup[8:88], "PC Remote Input")
	if err := ioctl(fd, uiDevSetup, uintptr(unsafe.Pointer(&setup))); err != nil {
		f.Close()
		return nil, err
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, err
	}
	// Give the desktop a moment to pick up the new device before the first key.
	time.Sleep(200 * time.Millisecond)
	return &keyboard{out: f, held: map[uint16]bool{}}, nil
}

func (a *app) keyboardDevice() (*keyboard, error) {
	a.keyboardMu.Lock()
	defer a.keyboardMu.Unlock()
	if a.keyboard == nil {
		device, err := a.openKeyboard()
		if err != nil {
			return nil, err
		}
		a.keyboard = device
	}
	return a.keyboard, nil
}

// startKeyboardSession ends any older session, so one connection owns the keys.
func (a *app) startKeyboardSession(cancel context.CancelFunc) {
	a.keyboardMu.Lock()
	defer a.keyboardMu.Unlock()
	if a.endKeyboardSession != nil {
		a.endKeyboardSession()
	}
	a.endKeyboardSession = cancel
}

func (k *keyboard) handleMessage(message string) error {
	if message == "r" {
		k.releaseAll()
		return nil
	}
	fields := strings.Fields(message)
	if len(fields) < 2 {
		return nil
	}
	switch fields[0] {
	case "d", "u":
		code, known := keyCodes[fields[1]]
		if !known {
			return nil
		}
		if fields[0] == "d" {
			return k.press(code)
		}
		return k.release(code)
	case "m":
		if len(fields) != 3 {
			return nil
		}
		dx, errX := strconv.Atoi(fields[1])
		dy, errY := strconv.Atoi(fields[2])
		if errX != nil || errY != nil {
			return nil
		}
		return k.sendRelative(map[uint16]int32{relX: clamp(dx, maxMove), relY: clamp(dy, maxMove)})
	case "s":
		amount, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil
		}
		return k.sendRelative(map[uint16]int32{relWheelHiRes: clamp(amount, maxScroll)})
	}
	return nil
}

// acceptSocket opens a WebSocket and checks the passcode. Browsers can't set
// headers on a WebSocket, so the passcode is the first message. On failure it
// returns nil and has already answered.
func (a *app) acceptSocket(w http.ResponseWriter, r *http.Request) *websocket.Conn {
	// Counted now: the passcode only arrives after the socket opens.
	address := clientAddress(r, a.trustProxy)
	if !a.guard.reserve(address) {
		writeError(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute.")
		return nil
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		a.guard.refund(address)
		return nil
	}
	conn.SetReadLimit(maxBodyBytes)

	authContext, cancelAuth := context.WithTimeout(context.Background(), keyboardAuthTimeout)
	defer cancelAuth()
	_, supplied, err := conn.Read(authContext)
	if err != nil {
		a.guard.refund(address)
		conn.CloseNow()
		return nil
	}
	if subtle.ConstantTimeCompare(supplied, a.passcode) != 1 {
		conn.Close(websocket.StatusPolicyViolation, "Wrong passcode.")
		return nil
	}
	a.guard.clear(address)
	return conn
}

func (a *app) handleKeyboard(w http.ResponseWriter, r *http.Request) {
	conn := a.acceptSocket(w, r)
	if conn == nil {
		return
	}
	defer conn.CloseNow()

	device, err := a.keyboardDevice()
	if err != nil {
		log.Printf("opening keyboard: %v", err)
		conn.Close(websocket.StatusInternalError, "Could not create keyboard.")
		return
	}
	session, end := context.WithCancel(context.Background())
	defer end()
	a.startKeyboardSession(end)
	device.releaseAll()
	defer device.releaseAll()
	defer a.pointer.endSession()

	if err := conn.Write(session, websocket.MessageText, []byte("ok")); err != nil {
		return
	}
	for {
		readContext, cancelRead := context.WithTimeout(session, a.keyboardTimeout)
		_, message, err := conn.Read(readContext)
		cancelRead()
		if err != nil {
			return
		}
		if err := device.handleMessage(string(message)); err != nil {
			log.Printf("sending key: %v", err)
			return
		}
	}
}

// clientAddress names the device for the passcode lockout. Behind a local proxy
// such as `tailscale serve`, every request comes from loopback, so the device is
// read from the X-Forwarded-For entry the proxy appended. That needs trustProxy
// and a loopback connection: without a proxy, loopback is a local program.
func clientAddress(r *http.Request, trustProxy bool) string {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if ip := net.ParseIP(address); !trustProxy || ip == nil || !ip.IsLoopback() {
		return address
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return address
	}
	entries := strings.Split(forwarded[len(forwarded)-1], ",")
	if ip := net.ParseIP(strings.TrimSpace(entries[len(entries)-1])); ip != nil {
		return ip.String()
	}
	return address
}
