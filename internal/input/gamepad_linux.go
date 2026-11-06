package input

import (
	"encoding/binary"
	"os"
	"time"
	"unsafe"
)

func OpenGamepad() (*Gamepad, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Gamepad, error) {
		f.Close()
		return nil, err
	}
	fd := f.Fd()
	for _, eventType := range []uintptr{EvKey, EvAbs} {
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
	return &Gamepad{out: f, closer: f}, nil
}
