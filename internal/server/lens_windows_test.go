package server

import (
	"testing"
	"time"
)

// The lens opens, follows, and closes on stop, and a restart waits for the
// old one to go.
func TestLensWindowStartsAndStops(t *testing.T) {
	l := newLensDriver("")
	if !l.available() {
		t.Skip("no Magnification API here")
	}
	for range 2 {
		run, err := l.start(pointerSettings{Lens: "small", Zoom: 2}, lensLook{})
		if err != nil {
			t.Skipf("no lens window here: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		run.stop()
		run.stop() // safe twice
		select {
		case <-run.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the lens didn't close")
		}
	}
}
