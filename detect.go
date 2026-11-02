package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
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
}

// check is one line of the system check.
type check struct {
	Label  string
	OK     bool
	Detail string   // why it failed, in a few words
	Fix    []string // packages that fix it, if any
}

// system is what setup knows about this PC before asking anything.
type system struct {
	Dir      string // ~/.config/pc-remote
	Hostname string
	Checks   []check

	Existing     config // the current settings, or the defaults
	HasConfig    bool
	KeepPort     bool // port and listen were set by hand; keep them
	KeepListen   bool
	CustomThemes []string

	OmarchyTheme string // display name of the current Omarchy theme

	LANAddress string
	LANSubnet  string // e.g. 192.168.1.0/24

	Tailscale        bool   // installed and logged in
	TailscaleName    string // e.g. pc.tailnet.ts.net
	TailscaleServing bool   // tailscale serve already points at the port

	Firewall     bool         // ufw is on
	FirewallOpen map[int]bool // ports ufw already allows; nil if unknown

	Pacman      bool
	ServiceUnit bool // a pc-remote-web.service exists
	Executable  string
}

func (s system) missingPackages() []string {
	var packages []string
	for _, c := range s.Checks {
		packages = append(packages, c.Fix...)
	}
	return packages
}

// firewallBlocks lists the ports the answers need that ufw may block.
func (s system) firewallBlocks(a answers) []int {
	if !s.Firewall {
		return nil
	}
	var ports []int
	if viaLAN(a.Access) && !s.KeepListen {
		ports = append(ports, s.port())
	}
	if a.Party {
		ports = append(ports, s.guestPort())
	}
	var blocked []int
	for _, p := range ports {
		if !s.FirewallOpen[p] {
			blocked = append(blocked, p)
		}
	}
	return blocked
}

func (s system) port() int { return s.Existing.Port }

func (s system) guestPort() int {
	if s.Existing.GuestPort == 0 {
		return defaultGuestPort
	}
	return s.Existing.GuestPort
}

func detectSystem(dir string) system {
	s := system{Dir: dir}
	s.Hostname, _ = os.Hostname()
	s.Executable, _ = os.Executable()

	s.Checks = append(s.Checks, checkUinput())
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

	s.Existing, s.HasConfig, s.KeepPort, s.KeepListen = readExisting(dir)
	s.CustomThemes = customThemes(dir)
	s.OmarchyTheme = omarchyThemeName()

	s.LANAddress = lanAddress()
	s.LANSubnet = subnetOf(s.LANAddress)

	if _, ok := lookPath("tailscale"); ok {
		s.TailscaleName, s.Tailscale = tailscaleName()
		if out, err := runCommand("tailscale", "serve", "status"); err == nil {
			s.TailscaleServing = servesPort(out, s.port())
		}
	}

	s.Firewall, s.FirewallOpen = ufwState("/etc/ufw/ufw.conf", "/etc/ufw/user.rules")
	_, err := runCommand("systemctl", "--user", "cat", "pc-remote-web.service")
	s.ServiceUnit = err == nil
	return s
}

// lookPath also finds tools in ~/.local/bin, which a fresh shell may not have on PATH.
func lookPath(tool string) (string, bool) {
	if path, err := exec.LookPath(tool); err == nil {
		return path, true
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".local", "bin", tool)
		if info, err := os.Stat(path); err == nil && info.Mode()&0o111 != 0 {
			return path, true
		}
	}
	return "", false
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

// readExisting loads the current settings, falling back to the defaults if
// they don't load, and notes which advanced settings were set by hand.
func readExisting(dir string) (c config, found, port, listen bool) {
	c, err := loadConfig(dir)
	if err != nil {
		c, _ = loadConfig(filepath.Join(dir, "missing"))
	}
	var raw map[string]any
	meta, err := toml.DecodeFile(filepath.Join(dir, "config.toml"), &raw)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, false, false
	}
	if err != nil {
		return c, true, false, false
	}
	return c, true, meta.IsDefined("port"), meta.IsDefined("listen")
}

func customThemes(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "themes", "*.css"))
	var names []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".css")
		if themeNamePattern.MatchString(name) && name != "default" && name != "omarchy" {
			names = append(names, name)
		}
	}
	return names
}

// omarchyThemeName turns "tokyo-night" into "Tokyo Night".
func omarchyThemeName() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	data, err := os.ReadFile(filepath.Join(state, "omarchy", "current", "theme.name"))
	if err != nil {
		return ""
	}
	return titleCase(strings.ReplaceAll(strings.TrimSpace(string(data)), "-", " "))
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func subnetOf(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return ""
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return (&net.IPNet{IP: ip.Mask(n.Mask), Mask: n.Mask}).String()
		}
	}
	return ""
}

func tailscaleName() (string, bool) {
	out, err := runCommand("tailscale", "status", "--json")
	if err != nil {
		return "", false
	}
	var status struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if json.Unmarshal([]byte(out), &status) != nil {
		return "", false
	}
	return strings.TrimSuffix(status.Self.DNSName, "."), status.BackendState == "Running"
}

// servesPort reports whether `tailscale serve status` proxies to this port.
func servesPort(status string, port int) bool {
	target := regexp.MustCompile(`proxy https?://(127\.0\.0\.1|localhost):` + strconv.Itoa(port) + `\b`)
	return target.MatchString(status)
}

var (
	ufwEnabled = regexp.MustCompile(`(?m)^ENABLED=yes`)
	ufwAllow   = regexp.MustCompile(`^### tuple ### allow (?:tcp|any) (\d+) `)
)

// ufwState reads whether ufw is on, and which TCP ports it already allows.
// The open ports are nil when the rules can't be read.
func ufwState(confPath, rulesPath string) (bool, map[int]bool) {
	conf, err := os.ReadFile(confPath)
	if err != nil || !ufwEnabled.Match(conf) {
		return false, nil
	}
	rules, err := os.ReadFile(rulesPath)
	if err != nil {
		return true, nil
	}
	open := map[int]bool{}
	for _, line := range strings.Split(string(rules), "\n") {
		if m := ufwAllow.FindStringSubmatch(line); m != nil {
			port, _ := strconv.Atoi(m[1])
			open[port] = true
		}
	}
	return true, open
}
