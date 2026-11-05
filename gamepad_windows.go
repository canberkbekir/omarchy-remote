package main

import "errors"

// A virtual controller on Windows needs the ViGEmBus driver, which isn't
// wired up yet.
func openVirtualGamepad() (*gamepad, error) {
	return nil, errors.New("the game controller works only on Linux for now")
}
