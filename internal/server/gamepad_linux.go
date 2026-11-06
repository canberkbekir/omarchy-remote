package server

import (
	"github.com/canberkbekir/omarchy-remote/internal/input"
)

// guestPadOpener: each guest gets a new controller.
func guestPadOpener(*app) func() (*input.Gamepad, error) {
	return input.OpenGamepad
}
