package server

import (
	"github.com/canberkbekir/omarchy-remote/internal/input"
)

// guestPadOpener plugs in the admin's controller before the first guest's.
// XInput numbers controllers in the order they appear, so the admin stays
// player 1 and guests get 2 to 4.
func guestPadOpener(a *app) func() (*input.Gamepad, error) {
	return func() (*input.Gamepad, error) {
		if _, err := a.gamepadDevice(); err != nil {
			return nil, err
		}
		return input.OpenGamepad()
	}
}
