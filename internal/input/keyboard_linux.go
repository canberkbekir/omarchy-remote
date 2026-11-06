package input

import (
	"encoding/binary"
	"os"
	"syscall"
	"time"
	"unsafe"
)

func ioctl(fd, request, value uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, value); errno != 0 {
		return errno
	}
	return nil
}

func OpenKeyboard() (*Keyboard, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	for _, eventType := range []uintptr{EvKey, EvRel} {
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
	return &Keyboard{out: f, held: map[uint16]bool{}}, nil
}
