package main

import (
	"encoding/binary"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows there is no uinput. The keyboard still writes Linux input
// events, and inputWriter turns them into SendInput calls, so the phone
// protocol and the key table stay the same on both systems.

const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfUnicode     = 0x0004
	keyeventfScanCode    = 0x0008

	mouseeventfMove      = 0x0001
	mouseeventfLeftDown  = 0x0002
	mouseeventfLeftUp    = 0x0004
	mouseeventfRightDown = 0x0008
	mouseeventfRightUp   = 0x0010
	mouseeventfWheel     = 0x0800

	btnLeft  = 0x110
	btnRight = 0x111
)

var procSendInput = windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")

// mouseInput and keyInput are the two shapes of the Win32 INPUT struct. The
// union inside INPUT is pointer aligned, which the uintptr fields reproduce;
// the padding brings keyInput up to the size of the larger mouse variant.
type mouseInput struct {
	kind uint32
	mi   struct {
		dx, dy                 int32
		mouseData, flags, time uint32
		extraInfo              uintptr
	}
}

type keyInput struct {
	kind uint32
	ki   struct {
		vk, scan    uint16
		flags, time uint32
		extraInfo   uintptr
	}
	_ [8]byte
}

// Fails to compile unless both shapes are the same size.
var _ = [1]struct{}{}[unsafe.Sizeof(keyInput{})-unsafe.Sizeof(mouseInput{})]

var errInputBlocked = errors.New("Windows blocked the input")

// sendInput reports an error when Windows refuses the input, as it does on
// the lock screen or for an app running as administrator when this one isn't.
func sendInput(sent uintptr, want int, err error) error {
	if int(sent) == want {
		return nil
	}
	if err != nil && err != windows.ERROR_SUCCESS {
		return err
	}
	return errInputBlocked
}

func sendKeys(inputs []keyInput) error {
	if len(inputs) == 0 {
		return nil
	}
	sent, _, err := procSendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	return sendInput(sent, len(inputs), err)
}

func sendMouse(input mouseInput) error {
	input.kind = inputMouse
	sent, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	return sendInput(sent, 1, err)
}

func scanKey(scan uint16, extended, up bool) keyInput {
	var k keyInput
	k.kind = inputKeyboard
	k.ki.scan = scan
	k.ki.flags = keyeventfScanCode
	if extended {
		k.ki.flags |= keyeventfExtendedKey
	}
	if up {
		k.ki.flags |= keyeventfKeyUp
	}
	return k
}

func virtualKey(vk uint16, up bool) keyInput {
	var k keyInput
	k.kind = inputKeyboard
	k.ki.vk = vk
	if up {
		k.ki.flags = keyeventfKeyUp
	}
	return k
}

// extendedScanCodes are the keys whose scan code differs from the Linux key
// code: they carry the E0 prefix. Every other key in keyCodes, Esc to F12,
// has a Linux code equal to its scan code.
var extendedScanCodes = map[uint16]uint16{
	97:  0x1D, // right ctrl
	102: 0x47, // home
	103: 0x48, // up
	104: 0x49, // page up
	105: 0x4B, // left
	106: 0x4D, // right
	107: 0x4F, // end
	108: 0x50, // down
	109: 0x51, // page down
	111: 0x53, // delete
	125: 0x5B, // left Windows key
}

// inputWriter reads the Linux input events the keyboard writes and replays
// them with SendInput. Pointer motion is gathered until the sync event, so
// one phone message moves the pointer once.
type inputWriter struct {
	dx, dy int32
}

func (w *inputWriter) Write(p []byte) (int, error) {
	for event := p; len(event) >= 24; event = event[24:] {
		kind := binary.LittleEndian.Uint16(event[16:])
		code := binary.LittleEndian.Uint16(event[18:])
		value := int32(binary.LittleEndian.Uint32(event[20:]))
		var err error
		switch kind {
		case evKey:
			err = w.key(code, value != 0)
		case evRel:
			err = w.relative(code, value)
		case evSyn:
			err = w.flush()
		}
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *inputWriter) key(code uint16, down bool) error {
	var m mouseInput
	switch code {
	case btnLeft:
		m.mi.flags = mouseeventfLeftUp
		if down {
			m.mi.flags = mouseeventfLeftDown
		}
		return sendMouse(m)
	case btnRight:
		m.mi.flags = mouseeventfRightUp
		if down {
			m.mi.flags = mouseeventfRightDown
		}
		return sendMouse(m)
	}
	if scan, extended := extendedScanCodes[code]; extended {
		return sendKeys([]keyInput{scanKey(scan, true, !down)})
	}
	return sendKeys([]keyInput{scanKey(code, false, !down)})
}

func (w *inputWriter) relative(axis uint16, value int32) error {
	switch axis {
	case relX:
		w.dx += value
	case relY:
		w.dy += value
	case relWheelHiRes:
		// Both systems count 120 per notch, positive away from the user.
		var m mouseInput
		m.mi.flags = mouseeventfWheel
		m.mi.mouseData = uint32(value)
		return sendMouse(m)
	}
	return nil
}

func (w *inputWriter) flush() error {
	if w.dx == 0 && w.dy == 0 {
		return nil
	}
	var m mouseInput
	m.mi.dx, m.mi.dy = w.dx, w.dy
	m.mi.flags = mouseeventfMove
	w.dx, w.dy = 0, 0
	return sendMouse(m)
}

func openVirtualKeyboard() (*keyboard, error) {
	return &keyboard{out: &inputWriter{}, held: map[uint16]bool{}}, nil
}
