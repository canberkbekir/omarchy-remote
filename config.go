package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/BurntSushi/toml"
)

const (
	defaultPort      = 8377
	defaultGuestPort = 8378
)

// config is ~/.config/pc-remote/config.toml. Every setting is optional.
type config struct {
	Name   string `toml:"name"`
	Theme  string `toml:"theme"`
	Access string `toml:"access"`
	Port   int    `toml:"port"`
	Listen string `toml:"listen"`
	// GuestPort is where friends on the home Wi-Fi join a party; 0 turns
	// parties off. It's open only while a party runs.
	GuestPort int `toml:"guest_port"`
}

var themeNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pc-remote"), nil
}

func loadConfig(dir string) (config, error) {
	c := config{Theme: "default", Access: "lan", Port: defaultPort, GuestPort: defaultGuestPort}
	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if err == nil {
		meta, err := toml.Decode(string(data), &c)
		if err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
		// A typo would otherwise be ignored without a word.
		if keys := meta.Undecoded(); len(keys) > 0 {
			return c, fmt.Errorf("%s: unknown setting %q", path, keys[0].String())
		}
	}

	if c.Name == "" {
		c.Name = defaultPCName()
	}
	if !themeNamePattern.MatchString(c.Theme) {
		return c, fmt.Errorf("%s: theme %q may only use letters, digits, - and _", path, c.Theme)
	}
	if !validAccess(c.Access) {
		return c, fmt.Errorf("%s: access must be \"lan\", \"tailscale\" or \"both\", not %q", path, c.Access)
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("%s: port must be 1 to 65535", path)
	}
	if c.GuestPort < 0 || c.GuestPort > 65535 || c.GuestPort == c.Port {
		return c, fmt.Errorf("%s: guest_port must be 0 (off) or 1 to 65535, and not the same as port", path)
	}
	return c, nil
}

func validAccess(access string) bool {
	return access == "lan" || access == "tailscale" || access == "both"
}

// viaLAN and viaTailscale report the ways in that an access setting opens.
// "both" listens on the home network and also sits behind `tailscale serve`.
func viaLAN(access string) bool       { return access != "tailscale" }
func viaTailscale(access string) bool { return access != "lan" }

// listenAddr picks the address in order: PC_REMOTE_ADDR, listen, then the access preset.
// With "tailscale", only `tailscale serve` on this PC can reach the server.
func (c config) listenAddr() string {
	if addr := os.Getenv("PC_REMOTE_ADDR"); addr != "" {
		return addr
	}
	if c.Listen != "" {
		return c.Listen
	}
	host := "0.0.0.0"
	if c.Access == "tailscale" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.Port))
}

// listenScope reports whether addr is loopback only, or every interface.
func listenScope(addr string) (loopback, everywhere bool) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, false
	}
	if host == "" {
		return false, true
	}
	if host == "localhost" {
		return true, false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), ip != nil && ip.IsUnspecified()
}
