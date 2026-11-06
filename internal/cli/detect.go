package cli

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

	"github.com/canberkbekir/omarchy-remote/internal/config"
	"github.com/canberkbekir/omarchy-remote/internal/network"
	"github.com/canberkbekir/omarchy-remote/internal/run"
)

// check is one line of the system check.
type check struct {
	Label  string
	OK     bool
	Detail string   // why it failed, in a few words
	Fix    []string // packages that fix it, if any
}

// host is what setup knows about this PC before asking anything.
type host struct {
	Dir         string // ~/.config/pc-remote
	DefaultName string // what to call the PC when no name is given
	Checks      []check

	Existing      config.Config // the current settings, or the defaults
	ConfigProblem string        // why config.toml didn't load; setup keeps a copy before replacing it
	HasPasscode   bool
	HasConfig     bool
	KeepPort      bool // port and listen were set by hand; keep them
	KeepListen    bool
	CustomThemes  []string

	OmarchyTheme string // display name of the current Omarchy theme

	LANAddress string
	LANSubnet  string // e.g. 192.168.1.0/24

	Tailscale        bool   // installed and logged in
	TailscaleName    string // e.g. pc.tailnet.ts.net
	TailscaleServing bool   // tailscale serve already points at the port

	Firewall     bool         // ufw is on; Windows Firewall always is
	FirewallOpen map[int]bool // ports ufw already allows; nil if unknown

	Pacman      bool
	ServiceUnit bool // a pc-remote-web.service exists; on Windows, the logon task for this program
	Executable  string

	OmarchyHooks string // ~/.config/omarchy/hooks; empty without Omarchy
	UpdateHook   bool   // the post-update hook that runs pc-remote update is there

	BarPluginDir string // where the bar widget goes; empty without the Omarchy shell
	BarPlugin    bool   // the bar widget is installed
}

func (s host) missingPackages() []string {
	var packages []string
	for _, c := range s.Checks {
		packages = append(packages, c.Fix...)
	}
	return packages
}

// firewallBlocks lists the ports the answers need that ufw may block.
func (s host) firewallBlocks(a answers) []int {
	if !s.Firewall {
		return nil
	}
	var ports []int
	if config.ViaLAN(a.Access) && !s.KeepListen {
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

// updateHookPath is the script omarchy update runs afterwards, or "" without Omarchy.
func (s host) updateHookPath() string {
	if s.OmarchyHooks == "" {
		return ""
	}
	return filepath.Join(s.OmarchyHooks, "post-update.d", "omarchy-remote.hook")
}

func (s host) port() int { return s.Existing.Port }

func (s host) guestPort() int {
	if s.Existing.GuestPort == 0 {
		return config.DefaultGuestPort
	}
	return s.Existing.GuestPort
}

func detectSystem(dir string) host {
	s := host{Dir: dir}
	s.DefaultName = config.DefaultPCName()
	s.Executable, _ = os.Executable()

	var problem error
	s.Existing, s.HasConfig, s.KeepPort, s.KeepListen, problem = readExisting(dir)
	if problem != nil {
		// The messages start with the file's path, which the warning already names.
		s.ConfigProblem = strings.TrimPrefix(problem.Error(), filepath.Join(dir, "config.toml")+": ")
	}
	s.CustomThemes = customThemes(dir)
	if info, err := os.Stat(filepath.Join(dir, "passcode")); err == nil && info.Size() > 0 {
		s.HasPasscode = true
	}
	s.OmarchyTheme = omarchyThemeName()

	s.LANAddress = network.LANAddress()
	s.LANSubnet = subnetOf(s.LANAddress)

	if tool, ok := tailscaleTool(); ok {
		s.TailscaleName, s.Tailscale = tailscaleName(tool)
		if out, err := run.Command(tool, "serve", "status"); err == nil {
			s.TailscaleServing = servesPort(out, s.port())
		}
	}

	detectPlatform(&s)
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

// readExisting loads the current settings, falling back to the defaults if
// they don't load, and notes which advanced settings were set by hand.
// problem says why they didn't load.
func readExisting(dir string) (c config.Config, found, port, listen bool, problem error) {
	c, problem = config.Load(dir)
	if problem != nil {
		c, _ = config.Load(filepath.Join(dir, "missing"))
	}
	var raw map[string]any
	meta, err := toml.DecodeFile(filepath.Join(dir, "config.toml"), &raw)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, false, false, nil
	}
	if err != nil {
		return c, true, false, false, problem
	}
	return c, true, meta.IsDefined("port"), meta.IsDefined("listen"), problem
}

func customThemes(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "themes", "*.css"))
	var names []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".css")
		if config.ThemeNamePattern.MatchString(name) && name != "default" && name != "omarchy" {
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
	return config.TitleCase(strings.ReplaceAll(strings.TrimSpace(string(data)), "-", " "))
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

func tailscaleName(tool string) (string, bool) {
	out, err := run.Command(tool, "status", "--json")
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
