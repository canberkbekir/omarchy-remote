package system

import (
	"reflect"
	"testing"
)

// The Linux commands must stay exactly as they were before the platform split.
func TestPowerActionLinuxArgv(t *testing.T) {
	want := map[string][]string{
		"lock":     {"pc-remote", "omarchy-system-lock"},
		"reboot":   {"pc-remote", "omarchy-system-reboot"},
		"shutdown": {"pc-remote", "omarchy-system-shutdown"},
	}
	for action, argv := range want {
		started := fakeStart(t)
		if err := Power(action); err != nil {
			t.Fatalf("powerAction(%q): %v", action, err)
		}
		if len(*started) != 1 || !reflect.DeepEqual((*started)[0], argv) {
			t.Errorf("powerAction(%q) started %v, want %v", action, *started, argv)
		}
	}
}

func TestScreensCommandLinux(t *testing.T) {
	for on, want := range map[bool][]string{
		true:  {"pc-remote", "omarchy-brightness-display", "on"},
		false: {"pc-remote", "omarchy-brightness-display", "off"},
	} {
		if got := screensCommand(on); !reflect.DeepEqual(got, want) {
			t.Errorf("screensCommand(%v) = %v, want %v", on, got, want)
		}
	}
}
