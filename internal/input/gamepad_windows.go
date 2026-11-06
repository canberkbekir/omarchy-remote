package input

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ViGEmBusURL = "https://github.com/nefarius/ViGEmBus/releases"
	// ViGEmBusPackage is the driver's winget package, which setup installs.
	ViGEmBusPackage = "ViGEm.ViGEmBus"
	vigemClientDLL  = "ViGEmClient.dll"

	vigemErrorNone               = 0x20000000
	ViGEmErrorBusNotFound        = 0xE0000001
	vigemErrorBusVersionMismatch = 0xE0000008
	vigemErrorBusAccessFailed    = 0xE0000009

	loadLibrarySearchDllLoadDir = 0x00000100
	loadLibrarySearchSystem32   = 0x00000800

	xusbDpadUp    = 0x0001
	xusbDpadDown  = 0x0002
	xusbDpadLeft  = 0x0004
	xusbDpadRight = 0x0008
)

var ErrNoViGEmBus = errors.New("the ViGEmBus driver isn't installed: install it from " + ViGEmBusURL + ", then try again")

// xusbButtonBits are the XUSB_REPORT button bits for the names in
// gamepadButtons.
var xusbButtonBits = map[string]uint16{
	"start": 0x0010, "back": 0x0020, "l3": 0x0040, "r3": 0x0080,
	"lb": 0x0100, "rb": 0x0200, "home": 0x0400,
	"a": 0x1000, "b": 0x2000, "x": 0x4000, "y": 0x8000,
}

// xusbButtons maps the Linux key codes the gamepad writes to XUSB bits.
var xusbButtons = func() map[uint16]uint16 {
	m := map[uint16]uint16{}
	for _, button := range gamepadButtons {
		bit, ok := xusbButtonBits[button.name]
		if !ok {
			panic("no XUSB bit for gamepad button " + button.name)
		}
		m[button.code] = bit
	}
	return m
}()

// xusbReport is XUSB_REPORT from ViGEm/Common.h.
type xusbReport struct {
	buttons                            uint16
	leftTrigger, rightTrigger          uint8
	thumbLX, thumbLY, thumbRX, thumbRY int16
}

// xusbWriter reads the Linux input events the gamepad writes into one report
// and sends it at each sync event, so one phone message is one update.
type xusbWriter struct {
	report xusbReport
	update func(xusbReport) error
}

func (w *xusbWriter) Write(p []byte) (int, error) {
	for event := p; len(event) >= 24; event = event[24:] {
		kind := binary.LittleEndian.Uint16(event[16:])
		code := binary.LittleEndian.Uint16(event[18:])
		value := int32(binary.LittleEndian.Uint32(event[20:]))
		switch kind {
		case EvKey:
			if bit, ok := xusbButtons[code]; ok {
				w.report.buttons &^= bit
				if value != 0 {
					w.report.buttons |= bit
				}
			}
		case EvAbs:
			w.axis(code, value)
		case evSyn:
			if err := w.update(w.report); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

func (w *xusbWriter) axis(code uint16, value int32) {
	stick := func(v int32) int16 { return int16(min(max(v, -stickMax), stickMax)) }
	trigger := func(v int32) uint8 { return uint8(min(max(v, 0), triggerMax)) }
	// Each hat axis owns two D-pad bits: -1 sets the first, 1 the second.
	hat := func(negative, positive uint16) {
		w.report.buttons &^= negative | positive
		if value < 0 {
			w.report.buttons |= negative
		} else if value > 0 {
			w.report.buttons |= positive
		}
	}
	r := &w.report
	// Linux sticks point down for +y, XInput sticks point up.
	switch code {
	case absX:
		r.thumbLX = stick(value)
	case absY:
		r.thumbLY = stick(-value)
	case absRX:
		r.thumbRX = stick(value)
	case absRY:
		r.thumbRY = stick(-value)
	case absZ:
		r.leftTrigger = trigger(value)
	case absRZ:
		r.rightTrigger = trigger(value)
	case absHat0X:
		hat(xusbDpadLeft, xusbDpadRight)
	case absHat0Y:
		hat(xusbDpadUp, xusbDpadDown)
	}
}

// ViGEmError turns a VIGEM_ERROR into an error, or nil for VIGEM_ERROR_NONE.
func ViGEmError(what string, code uint32) error {
	switch code {
	case vigemErrorNone:
		return nil
	case ViGEmErrorBusNotFound:
		return fmt.Errorf("%s: %w", what, ErrNoViGEmBus)
	case vigemErrorBusVersionMismatch:
		return fmt.Errorf("%s: the ViGEmBus driver is too old: update it from %s", what, ViGEmBusURL)
	case vigemErrorBusAccessFailed:
		return fmt.Errorf("%s: ViGEmBus refused access: restart the PC after installing it", what)
	}
	return fmt.Errorf("%s: ViGEm error %#x", what, code)
}

type vigemAPI struct {
	alloc, connect, x360Alloc, add, update, remove, free *windows.Proc
}

// loadViGEm loads ViGEmClient.dll from dir only. A bare name would let
// Windows look in the current folder and PATH too. The DLL's own imports are
// looked up only next to it and in System32.
func loadViGEm(dir string) (*vigemAPI, error) {
	path := filepath.Join(dir, vigemClientDLL)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%s is missing: reinstall PC Remote from the release zip", path)
	}
	handle, err := windows.LoadLibraryEx(path, 0, loadLibrarySearchDllLoadDir|loadLibrarySearchSystem32)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %v", path, err)
	}
	dll := &windows.DLL{Name: path, Handle: handle}
	api := &vigemAPI{}
	for _, p := range []struct {
		proc **windows.Proc
		name string
	}{
		{&api.alloc, "vigem_alloc"}, {&api.connect, "vigem_connect"},
		{&api.x360Alloc, "vigem_target_x360_alloc"}, {&api.add, "vigem_target_add"},
		{&api.update, "vigem_target_x360_update"}, {&api.remove, "vigem_target_remove"},
		{&api.free, "vigem_target_free"},
	} {
		if *p.proc, err = dll.FindProc(p.name); err != nil {
			dll.Release()
			return nil, fmt.Errorf("%s is broken: %v", path, err)
		}
	}
	return api, nil
}

// One ViGEm client serves every controller. A failure is kept only for
// vigemRetry, so the status the phone polls doesn't open the bus each time,
// and installing the driver while the server runs works without a restart.
var vigem struct {
	mu        sync.Mutex
	api       *vigemAPI
	client    uintptr
	connected bool
	err       error
	failedAt  time.Time
}

const vigemRetry = 5 * time.Second

func connectViGEm() (*vigemAPI, uintptr, error) {
	vigem.mu.Lock()
	defer vigem.mu.Unlock()
	if !vigem.connected && vigem.err != nil && time.Since(vigem.failedAt) < vigemRetry {
		return nil, 0, vigem.err
	}
	api, client, err := connectViGEmLocked()
	if err != nil {
		vigem.err, vigem.failedAt = err, time.Now()
	} else {
		vigem.err = nil
	}
	return api, client, err
}

// lostViGEmBus makes the next connectViGEm connect again, after the driver
// was removed under a connected client.
func lostViGEmBus() {
	vigem.mu.Lock()
	defer vigem.mu.Unlock()
	vigem.connected = false
}

func connectViGEmLocked() (*vigemAPI, uintptr, error) {
	if vigem.connected {
		return vigem.api, vigem.client, nil
	}
	// update passes XUSB_REPORT as a pointer, which only the x64 calling
	// convention does for a 12-byte struct.
	if runtime.GOARCH != "amd64" {
		return nil, 0, errors.New("the game controller needs 64-bit x86 Windows")
	}
	if vigem.api == nil {
		exe, err := os.Executable()
		if err != nil {
			return nil, 0, err
		}
		api, err := loadViGEm(filepath.Dir(exe))
		if err != nil {
			return nil, 0, err
		}
		vigem.api = api
	}
	// The client is allocated once and connected again on each try, so a
	// missing driver doesn't leak a client per check.
	if vigem.client == 0 {
		client, _, _ := vigem.api.alloc.Call()
		if client == 0 {
			return nil, 0, errors.New("ViGEmClient couldn't start")
		}
		vigem.client = client
	}
	code, _, _ := vigem.api.connect.Call(vigem.client)
	if err := ViGEmError("connecting to ViGEmBus", uint32(code)); err != nil {
		return nil, 0, err
	}
	vigem.connected = true
	return vigem.api, vigem.client, nil
}

// ViGEmReady says why there can be no game controller, or nil. Tests swap it.
var ViGEmReady = func() error {
	_, _, err := connectViGEm()
	return err
}

func GamepadAvailable() bool {
	return ViGEmReady() == nil
}

// vigemPad is one plugged-in controller. Close frees its target, and a
// guest's session can still send a state after that, so closed guards every
// call into ViGEmClient.
type vigemPad struct {
	mu             sync.Mutex
	closed         bool
	api            *vigemAPI
	client, target uintptr
}

var errPadUnplugged = errors.New("the controller is unplugged")

// update sends the report. C passes XUSB_REPORT by value, which the x64
// calling convention turns into a pointer to a copy, since it is 12 bytes.
func (p *vigemPad) update(r xusbReport) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errPadUnplugged
	}
	code, _, _ := p.api.update.Call(p.client, p.target, uintptr(unsafe.Pointer(&r)))
	err := ViGEmError("updating the controller", uint32(code))
	if errors.Is(err, ErrNoViGEmBus) {
		lostViGEmBus()
	}
	return err
}

// Close unplugs the controller. Games see it disconnect.
func (p *vigemPad) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	code, _, _ := p.api.remove.Call(p.client, p.target)
	p.api.free.Call(p.target)
	return ViGEmError("removing the controller", uint32(code))
}

func OpenGamepad() (*Gamepad, error) {
	api, client, err := connectViGEm()
	if err != nil {
		return nil, err
	}
	target, _, _ := api.x360Alloc.Call()
	if target == 0 {
		return nil, errors.New("ViGEmClient couldn't make a controller")
	}
	// vigem_target_add returns once Windows has the controller plugged in.
	code, _, _ := api.add.Call(client, target)
	if err := ViGEmError("adding the controller", uint32(code)); err != nil {
		api.free.Call(target)
		if errors.Is(err, ErrNoViGEmBus) {
			lostViGEmBus()
		}
		return nil, err
	}
	pad := &vigemPad{api: api, client: client, target: target}
	return &Gamepad{out: &xusbWriter{update: pad.update}, closer: pad}, nil
}
