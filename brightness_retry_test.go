package main

import (
	"errors"
	"fmt"
	"testing"
)

// fakePanel stands in for the platform's brightness primitives, so the
// retry and readback logic is tested the same way on every OS.
type fakePanel struct {
	brightness int
	requests   int
	drops      map[int]bool
	final      bool // a dropped request is not worth another try
	forgotten  []string
}

func (p *fakePanel) answer() error {
	p.requests++
	if p.drops[p.requests] {
		if p.final {
			return fmt.Errorf("%w: no WMI answer", errNoRetry)
		}
		return errors.New("no answer")
	}
	return nil
}

// failFrom drops every request from first on. (dropFrom is Linux-only.)
func failFrom(first int) map[int]bool {
	drops := map[int]bool{}
	for i := first; i < first+20; i++ {
		drops[i] = true
	}
	return drops
}

func usePanel(t *testing.T, p *fakePanel) {
	read, write, forget, pause := readMonitorBrightness, writeMonitorBrightness, forgetMonitor, brightnessPause
	readMonitorBrightness = func(string) (int, error) {
		if err := p.answer(); err != nil {
			return 0, err
		}
		return p.brightness, nil
	}
	writeMonitorBrightness = func(_ string, target int) error {
		if err := p.answer(); err != nil {
			return err
		}
		p.brightness = target
		return nil
	}
	forgetMonitor = func(name string) { p.forgotten = append(p.forgotten, name) }
	brightnessPause = 0
	t.Cleanup(func() {
		readMonitorBrightness, writeMonitorBrightness, forgetMonitor, brightnessPause = read, write, forget, pause
	})
}

func TestPrimitivesReadThroughRetry(t *testing.T) {
	p := &fakePanel{brightness: 70, drops: map[int]bool{2: true}}
	usePanel(t, p)
	a := &app{}
	a.readBrightness(`\\.\DISPLAY1`)
	if got := a.readBrightness(`\\.\DISPLAY1`); got == nil || *got != 70 {
		t.Fatalf("readBrightness = %v, want 70", got)
	}
	if len(p.forgotten) != 1 || p.forgotten[0] != `\\.\DISPLAY1` {
		t.Fatalf("forgotten = %q, want the monitor once", p.forgotten)
	}
}

func TestPrimitivesRejectOutOfRange(t *testing.T) {
	p := &fakePanel{brightness: 140}
	usePanel(t, p)
	if got := (&app{}).readBrightness("panel"); got != nil {
		t.Fatalf("readBrightness = %d, want nil", *got)
	}
}

func TestPrimitivesWriteReadsBack(t *testing.T) {
	p := &fakePanel{brightness: 10, drops: map[int]bool{2: true}}
	usePanel(t, p)
	a := &app{}
	a.readBrightness("panel")
	if !a.setBrightness("panel", 55) || p.brightness != 55 {
		t.Fatalf("setBrightness failed, panel at %d", p.brightness)
	}
}

func TestNoRetryErrorStopsReadRetries(t *testing.T) {
	p := &fakePanel{brightness: 70, drops: failFrom(2), final: true}
	usePanel(t, p)
	a := &app{}
	a.readBrightness("panel")
	before := p.requests
	if got := a.readBrightness("panel"); got != nil {
		t.Fatalf("readBrightness = %d, want nil", *got)
	}
	if asked := p.requests - before; asked != 1 {
		t.Fatalf("asked %d times after a final failure, want 1", asked)
	}
}

func TestNoRetryErrorStopsWriteRetries(t *testing.T) {
	p := &fakePanel{brightness: 70, drops: failFrom(2), final: true}
	usePanel(t, p)
	a := &app{}
	a.readBrightness("panel")
	if a.setBrightness("panel", 30) {
		t.Fatal("setBrightness reported success")
	}
	if writes := p.requests - 1; writes != 1 {
		t.Fatalf("tried %d writes after a final failure, want 1", writes)
	}
}

func TestNoRetryErrorStopsAfterReadback(t *testing.T) {
	p := &fakePanel{brightness: 70, drops: failFrom(3), final: true}
	usePanel(t, p)
	a := &app{}
	a.readBrightness("panel")
	if a.setBrightness("panel", 30) {
		t.Fatal("setBrightness reported success without a readback")
	}
	if asked := p.requests - 1; asked != 2 {
		t.Fatalf("made %d requests, want one write and one readback", asked)
	}
}
