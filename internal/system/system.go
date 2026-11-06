// Package system controls the PC itself: volume, media, power, screens,
// brightness, sound outputs and the clipboard, one file per OS.
package system

import (
	"errors"
)

// ErrUnknownAction means the phone asked for a power action this PC doesn't have.
var ErrUnknownAction = errors.New("unknown action")

// MediaActions are the media keys the phone can press.
var MediaActions = map[string]bool{"previous": true, "toggle": true, "next": true}

// FeatureSet lists the controls that work on this OS. Each OS fills it in Features().
type FeatureSet struct {
	Power      bool `json:"power"`
	Screens    bool `json:"screens"`
	Nightlight bool `json:"nightlight"`
	Brightness bool `json:"brightness"`
	Outputs    bool `json:"outputs"`
	NowPlaying bool `json:"nowPlaying"`
	Gamepad    bool `json:"gamepad"`
	Zoom       bool `json:"zoom"`
	Party      bool `json:"party"`
}

// ErrNoSpeakers means the PC has no sound output at all, such as a server or
// a CI machine. The status still works then; it shows volume 0.
var ErrNoSpeakers = errors.New("no sound output")

// Monitor is a screen and its brightness, nil when it doesn't answer.
type Monitor struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Brightness *int   `json:"brightness"`
	On         bool   `json:"on"`
}
