package input

import (
	"testing"
)

func TestParsePadState(t *testing.T) {
	s, ok := ParsePadState("g 3 100 -200 0 99999 255 -5")
	if !ok {
		t.Fatal("expected a valid state")
	}
	want := padState{buttons: 3, axes: [6]int32{100, -200, 0, stickMax, triggerMax, 0}}
	if s != want {
		t.Errorf("state = %+v, want %+v", s, want)
	}
	for _, bad := range []string{"g", "g 1 2 3", "x 0 0 0 0 0 0 0", "g a 0 0 0 0 0 0", "g 0 0 0 0 0 0 0 0", "g -1 0 0 0 0 0 0"} {
		if _, ok := ParsePadState(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if s, _ := ParsePadState("g 4294967295 0 0 0 0 0 0"); s.buttons != 1<<15-1 {
		t.Errorf("unknown button bits should be dropped, got %b", s.buttons)
	}
}
