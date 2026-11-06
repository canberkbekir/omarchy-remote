package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/canberkbekir/omarchy-remote/internal/input"
)

func TestGamepadCheck(t *testing.T) {
	defer func(old func() error) { input.ViGEmReady = old }(input.ViGEmReady)
	input.ViGEmReady = func() error { return nil }
	if c := checkGamepad(); !c.OK {
		t.Fatalf("got %+v", c)
	}
	input.ViGEmReady = func() error { return input.ViGEmError("connecting", input.ViGEmErrorBusNotFound) }
	if c := checkGamepad(); c.OK || !strings.Contains(c.Detail, input.ViGEmBusURL) {
		t.Fatalf("got %+v", c)
	}
	// Setup offers to install the driver, and only the driver: a missing
	// ViGEmClient.dll means reinstalling pc-remote.
	if c := checkGamepad(); !slices.Equal(c.Fix, []string{input.ViGEmBusPackage}) {
		t.Fatalf("driver missing: Fix = %q, want %q", c.Fix, input.ViGEmBusPackage)
	}
	input.ViGEmReady = func() error { return errors.New("can't load ViGEmClient.dll") }
	if c := checkGamepad(); len(c.Fix) != 0 {
		t.Fatalf("DLL missing: Fix = %q, want none", c.Fix)
	}
	input.ViGEmReady = func() error { return nil }
	if c := checkGamepad(); len(c.Fix) != 0 {
		t.Fatalf("ready: Fix = %q, want none", c.Fix)
	}
}
