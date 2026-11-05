package main

import (
	"reflect"
	"testing"
)

// Shutdown and reboot start shutdown.exe without waiting. Lock and screens
// call user32 directly and are checked by hand in the VM.
func TestPowerActionWindowsArgv(t *testing.T) {
	want := map[string][]string{
		"reboot":   {"shutdown", "/r", "/t", "0"},
		"shutdown": {"shutdown", "/s", "/t", "0"},
	}
	for action, argv := range want {
		started := fakeStart(t)
		if err := powerAction(action); err != nil {
			t.Fatalf("powerAction(%q): %v", action, err)
		}
		if len(*started) != 1 || !reflect.DeepEqual((*started)[0], argv) {
			t.Errorf("powerAction(%q) started %v, want %v", action, *started, argv)
		}
	}
}
