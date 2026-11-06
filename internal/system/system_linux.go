package system

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/canberkbekir/omarchy-remote/internal/run"
)

const (
	sink         = "@DEFAULT_AUDIO_SINK@"
	MediaMissing = "playerctl is not installed."
)

// Features: everything the phone offers works on Linux.
func Features() FeatureSet {
	return FeatureSet{
		Power: true, Screens: true, Nightlight: true, Brightness: true, Outputs: true,
		NowPlaying: true, Gamepad: true, Zoom: true, Party: true,
	}
}

func Monitors() ([]Monitor, error) {
	output, err := run.Command("pc-remote", "hyprctl", "monitors", "-j")
	if err != nil {
		return nil, err
	}
	var found []struct {
		Name       string `json:"name"`
		Model      string `json:"model"`
		DpmsStatus bool   `json:"dpmsStatus"`
	}
	if err := json.Unmarshal([]byte(output), &found); err != nil {
		return nil, err
	}
	monitors := make([]Monitor, len(found))
	for i, m := range found {
		monitors[i] = Monitor{Name: m.Name, Label: m.Model, On: m.DpmsStatus}
	}
	return monitors, nil
}

var brightnessCommand = run.Command

func platformReadBrightness(name string) (int, error) {
	output, err := brightnessCommand("pc-remote", "omarchy-brightness-display", "--monitor", name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(output))
}

func platformWriteBrightness(name string, target int) error {
	_, err := brightnessCommand("pc-remote", "omarchy-brightness-display", "--no-osd", "--monitor", name, strconv.Itoa(target)+"%")
	return err
}

// platformForgetMonitor: one dropped request makes Omarchy's DDC helper mark
// the monitor unavailable for a minute, so its cache entry is cleared before
// a retry; the helper finds the monitor again.
func platformForgetMonitor(name string) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		cacheName := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r) {
				return r
			}
			return '_'
		}, name)
		os.Remove(filepath.Join(dir, "omarchy-brightness-display-ddc", cacheName+".bus"))
	}
}

func Outputs() ([]Output, error) {
	text, err := run.Command("pactl", "-f", "json", "list", "sinks")
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
	current, err := run.Command("pactl", "get-default-sink")
	if err != nil {
		return nil, err
	}
	current = strings.TrimSpace(current)
	outputs := make([]Output, len(sinks))
	for i, sink := range sinks {
		outputs[i] = Output{Name: sink.Name, Label: outputLabel(sink.Description), Active: sink.Name == current}
	}
	return outputs, nil
}

func SetOutput(name string) error {
	_, err := run.Command("pactl", "set-default-sink", name)
	return err
}

// Power commands go through the helper so they reach the Hyprland session.
var powerCommands = map[string][]string{
	"lock":     {"pc-remote", "omarchy-system-lock"},
	"reboot":   {"pc-remote", "omarchy-system-reboot"},
	"shutdown": {"pc-remote", "omarchy-system-shutdown"},
}

func Power(action string) error {
	argv, ok := powerCommands[action]
	if !ok {
		return ErrUnknownAction
	}
	return run.Start(argv...)
}

func screensCommand(on bool) []string {
	state := "off"
	if on {
		state = "on"
	}
	return []string{"pc-remote", "omarchy-brightness-display", state}
}

func SetScreens(on bool) error {
	_, err := run.Command(screensCommand(on)...)
	return err
}

var mediaCommands = map[string][]string{
	"previous": {"playerctl", "previous"},
	"toggle":   {"playerctl", "play-pause"},
	"next":     {"playerctl", "next"},
}

func Volume() (volume int, muted bool, err error) {
	output, err := run.Command("wpctl", "get-volume", sink)
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

// SetVolume also unmutes, so the new level is heard.
func SetVolume(volume int) error {
	if _, err := run.Command("wpctl", "set-volume", sink, strconv.Itoa(volume)+"%"); err != nil {
		return err
	}
	return SetMuted(false)
}

func SetMuted(muted bool) error {
	value := "0"
	if muted {
		value = "1"
	}
	_, err := run.Command("wpctl", "set-mute", sink, value)
	return err
}

func MediaAvailable() bool {
	_, err := exec.LookPath("playerctl")
	return err == nil
}

func NowPlaying() *Track {
	t, _ := readTrack()
	return t
}

// Cover reads the cover from the URL the player reported.
func Cover() ([]byte, error) {
	_, art := readTrack()
	return readArt(art)
}

func Media(action string) error {
	_, err := run.Command(mediaCommands[action]...)
	return err
}

func TypeText(text string) error {
	return run.WithInput("", "pc-remote", "wtype", "--", text)
}

func PressEnter() error {
	return run.WithInput("", "pc-remote", "wtype", "-k", "Return")
}

// ReadClipboard returns "" when nothing is copied or the clipboard holds no
// text, which is when wl-paste fails.
func ReadClipboard() string {
	text, err := run.Command("pc-remote", "wl-paste", "--no-newline", "--type", "text/plain")
	if err != nil {
		return ""
	}
	return text
}

func WriteClipboard(text string) error {
	return run.WithInput(text, "pc-remote", "wl-copy", "--type", "text/plain")
}
