package system

import (
	"errors"
	"testing"

	"github.com/canberkbekir/omarchy-remote/internal/run"
)

func TestPowerActionUnknown(t *testing.T) {
	started := fakeStart(t)
	for _, action := range []string{"", "hibernate", "LOCK"} {
		if err := Power(action); !errors.Is(err, ErrUnknownAction) {
			t.Errorf("powerAction(%q) = %v, want errUnknownAction", action, err)
		}
	}
	if len(*started) != 0 {
		t.Errorf("unknown actions started %v", *started)
	}
}

// fakeStart swaps run.Start for a recorder, so no test reboots the PC.
func fakeStart(t *testing.T) *[][]string {
	return fakeStartErr(t, nil)
}

func fakeStartErr(t *testing.T, err error) *[][]string {
	t.Helper()
	var started [][]string
	old := run.Start
	run.Start = func(argv ...string) error {
		started = append(started, argv)
		return err
	}
	t.Cleanup(func() { run.Start = old })
	return &started
}
