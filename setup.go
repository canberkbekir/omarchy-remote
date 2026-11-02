package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

// answers are the choices made in setup.
type answers struct {
	Install      bool // install the missing packages
	Access       string
	OpenFirewall bool
	Name         string
	Theme        string
	Party        bool
}

// defaultAnswers starts from the current settings, or the best guess for a new setup.
func defaultAnswers(s system) answers {
	a := answers{
		Install: true,
		Access:  s.Existing.Access,
		Name:    s.Existing.Name,
		Theme:   s.Existing.Theme,
		Party:   s.Existing.GuestPort != 0,
	}
	if !s.HasConfig {
		a.Access = "lan"
		a.Theme = "default"
		if s.OmarchyTheme != "" {
			a.Theme = "omarchy"
		}
	}
	a.OpenFirewall = true
	return a
}

// action is one change setup makes. Sudo actions ask for a password.
type action struct {
	Label string
	Sudo  bool
	Run   func() error
}

// plan turns the answers into the changes to make, in order. Nothing is
// changed before the review screen.
func plan(s system, a answers) []action {
	var actions []action
	if packages := s.missingPackages(); a.Install && len(packages) > 0 {
		actions = append(actions, action{
			Label: "Install " + strings.Join(packages, ", "),
			Sudo:  true,
			Run:   sudo(append([]string{"pacman", "-S", "--needed", "--noconfirm"}, packages...)...),
		})
	}
	if blocked := s.firewallBlocks(a); a.OpenFirewall && len(blocked) > 0 {
		from := s.LANSubnet
		if from == "" {
			from = "192.168.0.0/16"
		}
		for _, port := range blocked {
			actions = append(actions, action{
				Label: fmt.Sprintf("Let devices on %s reach port %d", from, port),
				Sudo:  true,
				Run:   sudo("ufw", "allow", "from", from, "to", "any", "port", strconv.Itoa(port), "proto", "tcp"),
			})
		}
	}
	if viaTailscale(a.Access) && !s.TailscaleServing {
		actions = append(actions, action{
			Label: "Share the app on your tailnet with tailscale serve",
			Sudo:  true,
			Run:   sudo("tailscale", "serve", "--bg", strconv.Itoa(s.port())),
		})
	}
	path := filepath.Join(s.Dir, "config.toml")
	actions = append(actions, action{
		Label: "Save settings to " + tildePath(path),
		Run:   func() error { return saveConfig(path, renderConfig(s, a)) },
	})
	if !s.ServiceUnit {
		actions = append(actions, action{
			Label: "Add the background service",
			Run:   func() error { return installUnit(s.Executable) },
		})
	}
	actions = append(actions, action{
		Label: "Start Omarchy Remote now and on every login",
		Run:   startService,
	})
	return actions
}

func sudo(argv ...string) func() error {
	return func() error {
		cmd := exec.Command("sudo", argv...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
}

// renderConfig writes the settings with a short note on each one. Port and
// listen are kept only if they were set by hand.
func renderConfig(s system, a answers) string {
	guest := 0
	if a.Party {
		guest = s.guestPort()
	}
	var b strings.Builder
	b.WriteString("# Omarchy Remote settings, written by `pc-remote setup`.\n")
	b.WriteString("# Run it again to change them, or edit this file and run\n")
	b.WriteString("# `systemctl --user restart pc-remote-web`. See the README.\n\n")
	fmt.Fprintf(&b, "name   = %s  # the title at the top of the app\n", tomlString(a.Name))
	fmt.Fprintf(&b, "theme  = %s  # \"default\", \"omarchy\" or a file in themes/\n", tomlString(a.Theme))
	fmt.Fprintf(&b, "access = %s  # \"lan\": home Wi-Fi; \"tailscale\": through tailscale serve; \"both\"\n", tomlString(a.Access))
	fmt.Fprintf(&b, "guest_port = %d  # where friends join a party; 0 turns parties off\n", guest)
	if s.KeepPort {
		fmt.Fprintf(&b, "port   = %d\n", s.Existing.Port)
	}
	if s.KeepListen {
		fmt.Fprintf(&b, "listen = %s\n", tomlString(s.Existing.Listen))
	}
	return b.String()
}

// tomlString quotes s for TOML; Go's escapes for the characters left in are
// the same as TOML's.
func tomlString(s string) string {
	return strconv.Quote(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// cleanPCName trims the typed name, falling back to the computer's name.
func cleanPCName(name, hostname string) string {
	name = strings.Join(strings.FieldsFunc(name, unicode.IsControl), "")
	name = strings.TrimSpace(name)
	if name == "" {
		return hostname
	}
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40])
	}
	return name
}

func saveConfig(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// installUnit adds a user service for this binary, for installs that didn't ship one.
func installUnit(executable string) error {
	if executable == "" {
		return errors.New("can't find this program's path")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=Omarchy Remote
After=pipewire.service wireplumber.service

[Service]
ExecStart=%s
Environment=PATH=%s:%%h/.local/bin:/usr/local/bin:/usr/bin:/bin
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, executable, filepath.Dir(executable))
	path := filepath.Join(dir, "systemd", "user", "pc-remote-web.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	_, err = runCommand("systemctl", "--user", "daemon-reload")
	return err
}

func startService() error {
	if out, err := exec.Command("systemctl", "--user", "enable", "pc-remote-web.service").CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "restart", "pc-remote-web.service").CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitForServer waits until the server answers, or reports why it stopped.
func waitForServer(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	target := net.JoinHostPort(host, port)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if _, err := runCommand("systemctl", "--user", "is-failed", "--quiet", "pc-remote-web.service"); err == nil {
			break
		}
		if conn, err := net.DialTimeout("tcp", target, time.Second); err == nil {
			conn.Close()
			return nil
		}
	}
	log, _ := runCommand("journalctl", "--user", "-u", "pc-remote-web.service", "-n", "5", "--no-pager", "-o", "cat")
	return fmt.Errorf("it didn't start. The last log lines:\n%s", strings.TrimSpace(log))
}

// address is a way to open the app, and where it works.
type address struct{ label, url string }

// appAddresses are the addresses to open on the phone. The first one goes in
// the QR code: the Tailscale one when there is one, since it works anywhere.
func appAddresses(s system, a answers) []address {
	lan := "http://" + net.JoinHostPort(s.LANAddress, strconv.Itoa(s.port()))
	tailnet := "https://<this-pc>.<your-tailnet>.ts.net"
	if s.TailscaleName != "" {
		tailnet = "https://" + s.TailscaleName
	}
	switch a.Access {
	case "tailscale":
		return []address{{"Address", tailnet}}
	case "both":
		return []address{{"Anywhere", tailnet}, {"At home", lan}}
	}
	return []address{{"Address", lan}}
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// runSetup is `pc-remote setup`.
func runSetup(args []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	yes := flags.Bool("yes", false, "use the current settings or the defaults, without asking")
	access := flags.String("access", "", `how the phone connects: "lan", "tailscale" or "both"`)
	name := flags.String("name", "", "the title at the top of the app")
	theme := flags.String("theme", "", `"default", "omarchy" or your own theme`)
	party := flags.Bool("party", true, "let friends join games as players 2 to 4")
	flags.SetOutput(stdout)
	flags.Usage = func() {
		fmt.Fprintln(stdout, "Usage: pc-remote setup [--yes] [--access lan|tailscale|both] [--name NAME] [--theme THEME] [--party=false]")
		fmt.Fprintln(stdout, "\nSets up Omarchy Remote. Without --yes it asks, step by step.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}

	dir, err := configDir()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	s := detectSystem(dir)
	a := defaultAnswers(s)
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["access"] {
		a.Access = *access
	}
	if set["name"] {
		a.Name = *name
	}
	if set["theme"] {
		a.Theme = *theme
	}
	if set["party"] {
		a.Party = *party
	}
	if !validAccess(a.Access) {
		fmt.Fprintf(stdout, "--access must be \"lan\", \"tailscale\" or \"both\", not %q\n", a.Access)
		return 2
	}
	if !themeNamePattern.MatchString(a.Theme) {
		fmt.Fprintf(stdout, "--theme %q may only use letters, digits, - and _\n", a.Theme)
		return 2
	}

	interactive := term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
	switch {
	case *yes:
		// Without a person to ask, nothing that needs a password runs.
		a.Install, a.OpenFirewall = false, false
	case !interactive:
		fmt.Fprintln(stdout, "Setup asks a few questions, so it needs a terminal.")
		fmt.Fprintln(stdout, "To set up without questions, run: pc-remote setup --yes [--access lan|tailscale|both]")
		return 2
	default:
		final, err := tea.NewProgram(newWizard(s, a), tea.WithAltScreen()).Run()
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		w := final.(wizard)
		if !w.confirmed {
			fmt.Fprintln(stdout, "Setup stopped. Nothing was changed.")
			return 1
		}
		a = w.answers
	}
	a.Name = cleanPCName(a.Name, s.Hostname)
	if *yes && viaTailscale(a.Access) && !s.TailscaleServing {
		fmt.Fprintf(stdout, "Note: run `sudo tailscale serve --bg %d` to reach the app through Tailscale.\n", s.port())
	}
	if *yes {
		var kept []action
		for _, act := range plan(s, a) {
			if !act.Sudo {
				kept = append(kept, act)
			}
		}
		return apply(s, a, kept, stdout)
	}
	return apply(s, a, plan(s, a), stdout)
}

// apply runs the planned changes, then shows how to connect.
func apply(s system, a answers, actions []action, out io.Writer) int {
	st := newStyles(out)
	fmt.Fprintln(out)
	for _, act := range actions {
		label := act.Label
		if act.Sudo {
			label += st.dim.Render("  (asks for your password)")
		}
		fmt.Fprintln(out, "  "+st.accent.Render("›")+" "+label)
		if err := act.Run(); err != nil {
			fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+err.Error())
			fmt.Fprintln(out, "\n  Fix that and run "+st.code.Render("pc-remote setup")+" again.")
			return 1
		}
	}

	written := s
	written.Existing.Access = a.Access
	written.Existing.Listen = ""
	if s.KeepListen {
		written.Existing.Listen = s.Existing.Listen
	}
	fmt.Fprintln(out, "  "+st.accent.Render("›")+" Waiting for it to answer")
	if err := waitForServer(written.Existing.listenAddr()); err != nil {
		fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	passcode, err := loadPasscode(s.Dir)
	if err != nil {
		fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, readyCard(st, appAddresses(s, a), passcode, terminalWidth()))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+st.dim.Render("Change settings later   ")+st.code.Render("pc-remote setup"))
	fmt.Fprintln(out, "  "+st.dim.Render("Something not working?  ")+st.code.Render("pc-remote doctor"))
	fmt.Fprintln(out)
	return 0
}

// groupPasscode spaces the passcode in fours so it's easier to read off the
// screen. The login ignores spaces.
func groupPasscode(p string) string {
	var groups []string
	for len(p) > 4 {
		groups = append(groups, p[:4])
		p = p[4:]
	}
	return strings.Join(append(groups, p), " ")
}
