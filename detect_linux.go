package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// feature is one thing the app can do, and the commands it needs.
type feature struct {
	Label   string
	Tools   []string
	Package string // Arch packages that provide the tools
}

var features = []feature{
	{"Volume and audio output", []string{"wpctl"}, "wireplumber"},
	{"Screens and night light", []string{"hyprctl"}, "hyprland"},
	{"Typing and clipboard", []string{"wtype", "wl-copy"}, "wtype wl-clipboard"},
	{"Now Playing and media keys", []string{"playerctl"}, "playerctl"},
	{"Zoom lens", []string{"qs"}, "quickshell"},
}

// detectPlatform fills in what only Linux has: uinput, the tools each
// feature runs, Omarchy's bar and hooks, ufw and the systemd unit.
func detectPlatform(s *system) {
	s.Checks = append([]check{checkUinput()}, s.Checks...)
	_, s.Pacman = lookPath("pacman")
	for _, f := range features {
		c := check{Label: f.Label, OK: true}
		var missing []string
		for _, tool := range f.Tools {
			if _, ok := lookPath(tool); !ok {
				missing = append(missing, tool)
			}
		}
		if len(missing) > 0 {
			c.OK = false
			c.Detail = "needs " + strings.Join(missing, " and ")
			if s.Pacman {
				c.Fix = strings.Fields(f.Package)
			}
		}
		s.Checks = append(s.Checks, c)
	}

	if _, ok := lookPath("omarchy-shell"); ok {
		if dir, err := barPluginDir(); err == nil {
			s.BarPluginDir = dir
			_, err := os.Stat(dir)
			s.BarPlugin = err == nil
		}
	}
	if config, err := os.UserConfigDir(); err == nil {
		if info, err := os.Stat(filepath.Join(config, "omarchy", "hooks")); err == nil && info.IsDir() {
			s.OmarchyHooks = filepath.Join(config, "omarchy", "hooks")
			_, err := os.Stat(s.updateHookPath())
			s.UpdateHook = err == nil
		}
	}

	s.Firewall, s.FirewallOpen = ufwState("/etc/ufw/ufw.conf", "/etc/ufw/user.rules")
	_, err := runCommand("systemctl", "--user", "cat", "pc-remote-web.service")
	s.ServiceUnit = err == nil
}

func checkUinput() check {
	c := check{Label: "Keyboard, trackpad and gamepad"}
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	switch {
	case err == nil:
		f.Close()
		c.OK = true
	case errors.Is(err, os.ErrNotExist):
		c.Detail = "/dev/uinput is missing"
	default:
		c.Detail = "no access to /dev/uinput: log in at the PC itself"
	}
	return c
}
