// Package input is the virtual keyboard, mouse and game controller the phone
// drives: uinput on Linux, SendInput and ViGEmBus on Windows.
package input

import (
	"encoding/binary"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
)

const evSyn = 0

const EvKey = 1

const EvRel = 2

const relX = 0

const relY = 1

const relWheel = 8

const relWheelHiRes = 11

const // ioctl numbers from linux/uinput.h.
uiSetEvBit = 0x40045564

const uiSetKeyBit = 0x40045565

const uiSetRelBit = 0x40045566

const uiDevSetup = 0x405c5503

const uiDevCreate = 0x5501

const busVirtual = 0x06

const maxMove = 500

const maxScroll = 1200

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

type Keyboard struct {
	mu   sync.Mutex
	out  io.Writer
	held map[uint16]bool
}

// NewKeyboard is a keyboard that writes its input events to out.
func NewKeyboard(out io.Writer) *Keyboard {
	return &Keyboard{out: out, held: map[uint16]bool{}}
}

func writeEvent(buf []byte, eventType, code uint16, value int32) {
	// struct input_event on 64-bit Linux: a 16-byte timestamp, then type, code, value.
	binary.LittleEndian.PutUint16(buf[16:], eventType)
	binary.LittleEndian.PutUint16(buf[18:], code)
	binary.LittleEndian.PutUint32(buf[20:], uint32(value))
}

func (k *Keyboard) send(code uint16, value int32) error {
	var buf [48]byte
	writeEvent(buf[:24], EvKey, code, value)
	writeEvent(buf[24:], evSyn, 0, 0)
	_, err := k.out.Write(buf[:])
	return err
}

func (k *Keyboard) press(code uint16) error {
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

func (k *Keyboard) release(code uint16) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.held[code] {
		return nil
	}
	delete(k.held, code)
	return k.send(code, 0)
}

func (k *Keyboard) ReleaseAll() {
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
func (k *Keyboard) sendRelative(axes map[uint16]int32) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	buf := make([]byte, 0, 24*(len(axes)+1))
	var event [24]byte
	for axis, value := range axes {
		if value == 0 {
			continue
		}
		writeEvent(event[:], EvRel, axis, value)
		buf = append(buf, event[:]...)
	}
	if len(buf) == 0 {
		return nil
	}
	writeEvent(event[:], evSyn, 0, 0)
	_, err := k.out.Write(append(buf, event[:]...))
	return err
}

func (k *Keyboard) HandleMessage(message string) error {
	if message == "r" {
		k.ReleaseAll()
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
