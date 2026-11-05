package main

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestXUSBReportSize(t *testing.T) {
	if size := unsafe.Sizeof(xusbReport{}); size != 12 {
		t.Fatalf("XUSB_REPORT is %d bytes, want 12", size)
	}
}

// The phone's state goes through gamepad.apply, the same Linux events Linux
// gets, and comes out as the report ViGEmBus sends to games.
func TestGamepadStateToXUSBReport(t *testing.T) {
	const (
		a, b, x, y, lb, rb, back, start, home, l3, r3 = 1 << 0, 1 << 1, 1 << 2, 1 << 3, 1 << 4, 1 << 5, 1 << 6, 1 << 7, 1 << 8, 1 << 9, 1 << 10
	)
	tests := []struct {
		name  string
		state padState
		want  xusbReport
	}{
		{"a", padState{buttons: a}, xusbReport{buttons: 0x1000}},
		{"b", padState{buttons: b}, xusbReport{buttons: 0x2000}},
		{"x", padState{buttons: x}, xusbReport{buttons: 0x4000}},
		{"y", padState{buttons: y}, xusbReport{buttons: 0x8000}},
		{"lb", padState{buttons: lb}, xusbReport{buttons: 0x0100}},
		{"rb", padState{buttons: rb}, xusbReport{buttons: 0x0200}},
		{"back", padState{buttons: back}, xusbReport{buttons: 0x0020}},
		{"start", padState{buttons: start}, xusbReport{buttons: 0x0010}},
		{"home", padState{buttons: home}, xusbReport{buttons: 0x0400}},
		{"l3", padState{buttons: l3}, xusbReport{buttons: 0x0040}},
		{"r3", padState{buttons: r3}, xusbReport{buttons: 0x0080}},
		{"dpad up", padState{buttons: dpadUp}, xusbReport{buttons: 0x0001}},
		{"dpad down", padState{buttons: dpadDown}, xusbReport{buttons: 0x0002}},
		{"dpad left", padState{buttons: dpadLeft}, xusbReport{buttons: 0x0004}},
		{"dpad right", padState{buttons: dpadRight}, xusbReport{buttons: 0x0008}},
		{"dpad up right", padState{buttons: dpadUp | dpadRight}, xusbReport{buttons: 0x0009}},
		{"dpad down left", padState{buttons: dpadDown | dpadLeft}, xusbReport{buttons: 0x0006}},
		{"dpad up and down cancel", padState{buttons: a | dpadUp | dpadDown}, xusbReport{buttons: 0x1000}},
		{"everything", padState{buttons: 1<<15 - 1}, xusbReport{buttons: 0xf7f0}},
		// Linux sticks point down for +y, XInput sticks point up.
		{"left stick", padState{axes: [6]int32{12345, -32767}}, xusbReport{thumbLX: 12345, thumbLY: 32767}},
		{"right stick", padState{axes: [6]int32{0, 0, -32767, 32767}}, xusbReport{thumbRX: -32767, thumbRY: -32767}},
		{"triggers", padState{axes: [6]int32{0, 0, 0, 0, 255, 7}}, xusbReport{leftTrigger: 255, rightTrigger: 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent []xusbReport
			w := &xusbWriter{update: func(r xusbReport) error { sent = append(sent, r); return nil }}
			g := &gamepad{out: w}
			if err := g.apply(tt.state); err != nil {
				t.Fatal(err)
			}
			if len(sent) != 1 || sent[0] != tt.want {
				t.Fatalf("sent %+v, want [%+v]", sent, tt.want)
			}
			// Letting go sends one centered report.
			if err := g.apply(padState{}); err != nil {
				t.Fatal(err)
			}
			if tt.want != (xusbReport{}) && (len(sent) != 2 || sent[1] != xusbReport{}) {
				t.Fatalf("after reset sent %+v", sent)
			}
		})
	}
}

// Changes carry over: a report holds every control, not just what changed.
func TestXUSBWriterKeepsState(t *testing.T) {
	var sent []xusbReport
	g := &gamepad{out: &xusbWriter{update: func(r xusbReport) error { sent = append(sent, r); return nil }}}
	g.apply(padState{buttons: 1, axes: [6]int32{100}})
	g.apply(padState{buttons: 1 | 2, axes: [6]int32{100, 0, 0, 0, 9}})
	want := xusbReport{buttons: 0x3000, thumbLX: 100, leftTrigger: 9}
	if len(sent) != 2 || sent[1] != want {
		t.Fatalf("sent %+v, want last %+v", sent, want)
	}
}

func TestXUSBWriterError(t *testing.T) {
	g := &gamepad{out: &xusbWriter{update: func(xusbReport) error { return errors.New("bus gone") }}}
	if err := g.apply(padState{buttons: 1}); err == nil || !strings.Contains(err.Error(), "bus gone") {
		t.Fatalf("got %v", err)
	}
}

func TestVigemErrors(t *testing.T) {
	if err := vigemError("connecting", vigemErrorNone); err != nil {
		t.Fatalf("no error got %v", err)
	}
	err := vigemError("connecting", vigemErrorBusNotFound)
	if err == nil || !errors.Is(err, errNoViGEmBus) || !strings.Contains(err.Error(), vigemBusURL) {
		t.Fatalf("bus not found: %v", err)
	}
	err = vigemError("adding the controller", 0xE0000002)
	if err == nil || errors.Is(err, errNoViGEmBus) || !strings.Contains(err.Error(), "0xe0000002") {
		t.Fatalf("other error: %v", err)
	}
}

func TestMissingViGEmClientDLL(t *testing.T) {
	_, err := loadViGEm(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ViGEmClient.dll") {
		t.Fatalf("got %v", err)
	}
}

func TestGamepadCheck(t *testing.T) {
	defer func(old func() error) { vigemReady = old }(vigemReady)
	vigemReady = func() error { return nil }
	if c := checkGamepad(); !c.OK {
		t.Fatalf("got %+v", c)
	}
	vigemReady = func() error { return vigemError("connecting", vigemErrorBusNotFound) }
	if c := checkGamepad(); c.OK || !strings.Contains(c.Detail, vigemBusURL) {
		t.Fatalf("got %+v", c)
	}
}

// A removed guest's session may still send a state before it ends; the freed
// controller must never reach ViGEmClient again.
func TestClosedVigemPadRefusesUpdates(t *testing.T) {
	p := &vigemPad{closed: true} // nil api: any DLL call would crash
	if err := p.update(xusbReport{buttons: 1}); err == nil {
		t.Fatal("update after close succeeded")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestViGEmFailureIsCachedBriefly(t *testing.T) {
	defer func() { vigem.err, vigem.failedAt = nil, time.Time{} }()
	vigem.err, vigem.failedAt = errNoViGEmBus, time.Now()
	if _, _, err := connectViGEm(); !errors.Is(err, errNoViGEmBus) {
		t.Fatalf("got %v", err)
	}
}
