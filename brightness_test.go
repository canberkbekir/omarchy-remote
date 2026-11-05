//go:build linux

package main

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// flakyMonitor fails every request listed in drops (counted from 1), the way
// some monitors drop DDC/CI requests, and otherwise remembers its brightness.
// With steps set, it lands on the nearest of that many brightness steps.
type flakyMonitor struct {
	brightness int
	steps      int
	requests   int
	drops      map[int]bool
}

func (m *flakyMonitor) run(argv ...string) (string, error) {
	m.requests++
	if m.drops[m.requests] {
		return "", errors.New("DDC communication failed")
	}
	if value, ok := strings.CutSuffix(argv[len(argv)-1], "%"); ok {
		m.brightness, _ = strconv.Atoi(value)
		if m.steps > 0 {
			step := math.Round(float64(m.brightness*m.steps) / 100)
			m.brightness = int(math.Round(step * 100 / float64(m.steps)))
		}
		return value + "\n", nil
	}
	return strconv.Itoa(m.brightness) + "\n", nil
}

// dropFrom fails every request from first on.
func dropFrom(first int) map[int]bool {
	drops := map[int]bool{}
	for i := first; i < first+20; i++ {
		drops[i] = true
	}
	return drops
}

func useMonitor(t *testing.T, m *flakyMonitor) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	command, pause := brightnessCommand, brightnessPause
	brightnessCommand, brightnessPause = m.run, 0
	t.Cleanup(func() { brightnessCommand, brightnessPause = command, pause })
}

func TestReadBrightnessTriesSilentMonitorOnce(t *testing.T) {
	m := &flakyMonitor{drops: dropFrom(1)}
	useMonitor(t, m)
	a := &app{}
	if got := a.readBrightness("HDMI-A-1"); got != nil {
		t.Fatalf("readBrightness = %d, want nil", *got)
	}
	if m.requests != 1 {
		t.Fatalf("asked a monitor that never answered %d times, want 1", m.requests)
	}
}

func TestReadBrightnessRetriesMonitorThatAnswered(t *testing.T) {
	m := &flakyMonitor{brightness: 40, drops: map[int]bool{2: true, 3: true}}
	useMonitor(t, m)
	a := &app{}
	a.readBrightness("DP-2")
	if got := a.readBrightness("DP-2"); got == nil || *got != 40 {
		t.Fatalf("readBrightness = %v, want 40", got)
	}
}

func TestReadBrightnessForgetsMonitorThatGoesQuiet(t *testing.T) {
	m := &flakyMonitor{brightness: 40, drops: dropFrom(2)}
	useMonitor(t, m)
	a := &app{}
	a.readBrightness("DP-2")
	if got := a.readBrightness("DP-2"); got != nil {
		t.Fatalf("readBrightness = %d, want nil", *got)
	}
	before := m.requests
	a.readBrightness("DP-2")
	if m.requests-before != 1 {
		t.Fatalf("asked a monitor that went quiet %d times, want 1", m.requests-before)
	}
}

func TestRetryClearsUnavailableMarker(t *testing.T) {
	m := &flakyMonitor{brightness: 40, drops: map[int]bool{2: true}}
	useMonitor(t, m)
	a := &app{}
	a.readBrightness("DP-2")
	cache := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "omarchy-brightness-display-ddc", "DP-2.bus")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("unavailable 1793698189\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.readBrightness("DP-2"); got == nil || *got != 40 {
		t.Fatalf("readBrightness = %v, want 40", got)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("unavailable marker still there: %v", err)
	}
}

func TestSetBrightnessRetriesDroppedWrite(t *testing.T) {
	m := &flakyMonitor{brightness: 1, drops: map[int]bool{2: true}}
	useMonitor(t, m)
	a := &app{}
	a.readBrightness("DP-2")
	if !a.setBrightness("DP-2", 60) || m.brightness != 60 {
		t.Fatalf("setBrightness failed, monitor at %d", m.brightness)
	}
}

func TestSetBrightnessAcceptsNearestStep(t *testing.T) {
	m := &flakyMonitor{brightness: 100, steps: 15}
	useMonitor(t, m)
	a := &app{}
	a.readBrightness("eDP-1")
	if !a.setBrightness("eDP-1", 50) {
		t.Fatalf("setBrightness failed on a %d-step display at %d", m.steps, m.brightness)
	}
}

func TestSetBrightnessFailsWhenMonitorStopsAnswering(t *testing.T) {
	m := &flakyMonitor{brightness: 1, drops: dropFrom(2)}
	useMonitor(t, m)
	a := &app{}
	a.readBrightness("DP-2")
	if a.setBrightness("DP-2", 60) {
		t.Fatal("setBrightness reported success for a monitor that never answered")
	}
	if writes := m.requests - 1; writes != brightnessAttempts {
		t.Fatalf("sent %d writes, want %d", writes, brightnessAttempts)
	}
}
