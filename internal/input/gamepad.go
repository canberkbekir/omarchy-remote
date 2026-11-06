package input

import (
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
)

const EvAbs = 3

const absX = 0x00

const absY = 0x01

const absZ = 0x02

const absRX = 0x03

const absRY = 0x04

const absRZ = 0x05

const absHat0X = 0x10

const absHat0Y = 0x11

const // ioctl numbers from linux/uinput.h.
uiSetAbsBit = 0x40045567

const uiAbsSetup = 0x401c5504

const busUSB = 0x03

const stickMax = 32767

const triggerMax = 255

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

// CodeName is the phone's name for a key or button code, or "unknown".
func CodeName(code uint16) string {
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

// ParsePadState reads "g <buttons> <lx> <ly> <rx> <ry> <lt> <rt>". Values out
// of range are clamped, so a bad phone can't send anything a real pad couldn't.
func ParsePadState(message string) (padState, bool) {
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

type Gamepad struct {
	mu     sync.Mutex
	out    io.Writer
	closer io.Closer // removes the controller from the PC; nil in tests
	state  padState
}

// NewGamepad is a controller that writes its input events to out. closer,
// when not nil, removes the controller from the PC.
func NewGamepad(out io.Writer, closer io.Closer) *Gamepad {
	return &Gamepad{out: out, closer: closer}
}

// Close unplugs the controller. Games see it disconnect.
func (g *Gamepad) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closer != nil {
		g.closer.Close()
		g.closer = nil
	}
}

// Apply sends only what changed since the last state, then one sync.
func (g *Gamepad) Apply(next padState) error {
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
			add(EvKey, button.code, boolInt(next.buttons&bit != 0))
		}
	}
	for i, axis := range gamepadAxes {
		if prev.axes[i] != next.axes[i] {
			add(EvAbs, axis.code, next.axes[i])
		}
	}
	prevX, prevY := prev.hat()
	nextX, nextY := next.hat()
	if prevX != nextX {
		add(EvAbs, absHat0X, nextX)
	}
	if prevY != nextY {
		add(EvAbs, absHat0Y, nextY)
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

func (g *Gamepad) Reset() {
	if err := g.Apply(padState{}); err != nil {
		log.Printf("resetting gamepad: %v", err)
	}
}
