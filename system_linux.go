package main

import (
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// The Linux side of the remote: PipeWire for volume, playerctl for media,
// and wtype and wl-clipboard (run in the Hyprland session) for text.

const (
	sink         = "@DEFAULT_AUDIO_SINK@"
	mediaMissing = "playerctl is not installed."
)

// platformFeatures: everything the phone offers works on Linux.
func platformFeatures() featureSet {
	return featureSet{
		Power: true, Screens: true, Nightlight: true, Brightness: true, Outputs: true,
		NowPlaying: true, Gamepad: true, Zoom: true, Party: true,
	}
}

func listOutputs() ([]output, error) {
	text, err := runCommand("pactl", "-f", "json", "list", "sinks")
	if err != nil {
		return nil, err
	}
	var sinks []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(text), &sinks); err != nil {
		return nil, err
	}
	current, err := runCommand("pactl", "get-default-sink")
	if err != nil {
		return nil, err
	}
	current = strings.TrimSpace(current)
	outputs := make([]output, len(sinks))
	for i, sink := range sinks {
		outputs[i] = output{Name: sink.Name, Label: outputLabel(sink.Description), Active: sink.Name == current}
	}
	return outputs, nil
}

func setOutput(name string) error {
	_, err := runCommand("pactl", "set-default-sink", name)
	return err
}

// Power commands go through the helper so they reach the Hyprland session.
var powerCommands = map[string][]string{
	"lock":     {"pc-remote", "omarchy-system-lock"},
	"reboot":   {"pc-remote", "omarchy-system-reboot"},
	"shutdown": {"pc-remote", "omarchy-system-shutdown"},
}

func powerAction(action string) error {
	argv, ok := powerCommands[action]
	if !ok {
		return errUnknownAction
	}
	return startDetached(argv...)
}

func screensCommand(on bool) []string {
	state := "off"
	if on {
		state = "on"
	}
	return []string{"pc-remote", "omarchy-brightness-display", state}
}

func setScreens(on bool) error {
	_, err := runCommand(screensCommand(on)...)
	return err
}

var mediaCommands = map[string][]string{
	"previous": {"playerctl", "previous"},
	"toggle":   {"playerctl", "play-pause"},
	"next":     {"playerctl", "next"},
}

func readVolume() (volume int, muted bool, err error) {
	output, err := runCommand("wpctl", "get-volume", sink)
	if err != nil {
		return 0, false, err
	}
	fields := strings.Fields(output)
	if len(fields) < 2 {
		return 0, false, errors.New("unexpected wpctl output")
	}
	level, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, false, err
	}
	return int(math.Round(level * 100)), strings.Contains(output, "[MUTED]"), nil
}

// setVolume also unmutes, so the new level is heard.
func setVolume(volume int) error {
	if _, err := runCommand("wpctl", "set-volume", sink, strconv.Itoa(volume)+"%"); err != nil {
		return err
	}
	return setMuted(false)
}

func setMuted(muted bool) error {
	value := "0"
	if muted {
		value = "1"
	}
	_, err := runCommand("wpctl", "set-mute", sink, value)
	return err
}

func mediaAvailable() bool {
	_, err := exec.LookPath("playerctl")
	return err == nil
}

func nowPlaying() *track {
	t, _ := readTrack()
	return t
}

func mediaAction(action string) error {
	_, err := runCommand(mediaCommands[action]...)
	return err
}

func typeText(text string) error {
	return runDetached("", "pc-remote", "wtype", "--", text)
}

func pressEnter() error {
	return runDetached("", "pc-remote", "wtype", "-k", "Return")
}

// readClipboard returns "" when nothing is copied or the clipboard holds no
// text, which is when wl-paste fails.
func readClipboard() string {
	text, err := runCommand("pc-remote", "wl-paste", "--no-newline", "--type", "text/plain")
	if err != nil {
		return ""
	}
	return text
}

func writeClipboard(text string) error {
	return runDetached(text, "pc-remote", "wl-copy", "--type", "text/plain")
}
