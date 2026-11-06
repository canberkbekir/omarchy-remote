package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"

	"github.com/canberkbekir/omarchy-remote/internal/config"
)

// answers are the choices made in setup.
type answers struct {
	Install      bool // install the missing packages
	Access       string
	OpenFirewall bool
	Passcode     string // one the owner chose; empty keeps the current one or makes one
	Name         string
	Theme        string
	Party        bool
	AutoUpdate   bool // update along with Omarchy
	Bar          bool // show the icon in the Omarchy bar
}

// defaultAnswers starts from the current settings, or the best guess for a new setup.
func defaultAnswers(s host) answers {
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
	// Without Omarchy there's no desktop theme to follow.
	if !omarchyDesktop && a.Theme == "omarchy" {
		a.Theme = "default"
	}
	// Turned on for a new setup; after that it stays as the owner left it.
	a.AutoUpdate = s.OmarchyHooks != "" && (s.UpdateHook || !s.HasConfig)
	a.Bar = s.BarPluginDir != "" && (s.BarPlugin || !s.HasConfig)
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
func plan(s host, a answers) []action {
	var actions []action
	if packages := s.missingPackages(); a.Install && len(packages) > 0 {
		actions = append(actions, action{
			Label: "Install " + strings.Join(packages, ", "),
			Sudo:  true,
			Run:   installPackages(packages),
		})
	}
	if blocked := s.firewallBlocks(a); a.OpenFirewall && len(blocked) > 0 {
		actions = append(actions, firewallActions(s, blocked)...)
	}
	if config.ViaTailscale(a.Access) && !s.TailscaleServing {
		actions = append(actions, action{
			Label: "Share the app on your tailnet with tailscale serve",
			Sudo:  tailscaleNeedsAdmin,
			Run:   serveTailscale(s.port()),
		})
	}
	path := filepath.Join(s.Dir, "config.toml")
	if s.ConfigProblem != "" {
		actions = append(actions, action{
			Label: "Keep a copy of your old settings, which have a mistake, as " + tildePath(path+".bak"),
			Run:   func() error { return os.Rename(path, path+".bak") },
		})
	}
	actions = append(actions, action{
		Label: "Save settings to " + tildePath(path),
		Run:   func() error { return saveConfig(path, renderConfig(s, a)) },
	})
	if a.Passcode != "" {
		actions = append(actions, action{
			Label: "Save your new passcode",
			Run:   func() error { return savePasscode(s.Dir, a.Passcode) },
		})
	}
	if hook := s.updateHookPath(); hook != "" && a.AutoUpdate != s.UpdateHook {
		if a.AutoUpdate {
			actions = append(actions, action{
				Label: "Update Omarchy Remote whenever you run omarchy update",
				Run:   func() error { return saveUpdateHook(hook, s.Executable) },
			})
		} else {
			actions = append(actions, action{
				Label: "Stop updating Omarchy Remote with omarchy update",
				Run:   func() error { return os.Remove(hook) },
			})
		}
	}
	if dir := s.BarPluginDir; dir != "" && a.Bar != s.BarPlugin {
		if a.Bar {
			actions = append(actions, action{
				Label: "Add the Omarchy Remote icon to the bar",
				Run:   func() error { return installBarPlugin(dir, s.Executable) },
			})
		} else {
			actions = append(actions, action{
				Label: "Take the Omarchy Remote icon off the bar",
				Run:   func() error { return removeBarPlugin(dir) },
			})
		}
	}
	return append(actions, autostartActions(s)...)
}

// saveUpdateHook adds a script that Omarchy runs after each omarchy update.
func saveUpdateHook(path, executable string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	script := "#!/bin/bash\n" +
		"# Added by pc-remote setup: updates Omarchy Remote after omarchy update.\n" +
		"# To stop, run pc-remote setup again or delete this file.\n" +
		"exec " + shellQuote(executable) + " update\n"
	return os.WriteFile(path, []byte(script), 0o755)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderConfig writes the settings with a short note on each one. Port and
// listen are kept only if they were set by hand.
func renderConfig(s host, a answers) string {
	guest := 0
	if a.Party {
		guest = s.guestPort()
	}
	var b strings.Builder
	b.WriteString("# Omarchy Remote settings, written by `" + cliName + " setup`.\n")
	b.WriteString("# Run it again to change them, or edit this file and run\n")
	b.WriteString(configRestartNote)
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

// cleanPCName trims the typed name, falling back to the default name.
func cleanPCName(name, fallback string) string {
	name = strings.Join(strings.FieldsFunc(name, unicode.IsControl), "")
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
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

const (
	minPasscodeLength = 6
	maxPasscodeLength = 64
)

// passcodeProblem says why a chosen passcode can't be used, or "" if it can.
// The login drops spaces, so a passcode can't have any.
func passcodeProblem(p string) string {
	switch {
	case strings.IndexFunc(p, unicode.IsSpace) >= 0:
		return "Spaces aren't allowed."
	case len([]rune(p)) < minPasscodeLength:
		return fmt.Sprintf("Use at least %d characters.", minPasscodeLength)
	case len(p) > maxPasscodeLength:
		return fmt.Sprintf("Use at most %d characters.", maxPasscodeLength)
	}
	return ""
}

// savePasscode replaces the passcode; the server reads it when it starts.
func savePasscode(dir, p string) error {
	if problem := passcodeProblem(p); problem != "" {
		return errors.New(problem)
	}
	return saveConfig(filepath.Join(dir, "passcode"), p+"\n")
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
		if serviceFailed() {
			break
		}
		if conn, err := net.DialTimeout("tcp", target, time.Second); err == nil {
			conn.Close()
			return nil
		}
	}
	return startFailure()
}

// Address is a way to open the app, and where it works.
type Address struct{ Label, URL string }

// appAddresses are the addresses to open on the phone. The first one goes in
// the QR code: the Tailscale one when there is one, since it works anywhere.
func appAddresses(s host, a answers) []Address {
	lan := "http://" + net.JoinHostPort(s.LANAddress, strconv.Itoa(s.port()))
	tailnet := "https://<this-pc>.<your-tailnet>.ts.net"
	if s.TailscaleName != "" {
		tailnet = "https://" + s.TailscaleName
	}
	switch a.Access {
	case "tailscale":
		return []Address{{"Address", tailnet}}
	case "both":
		return []Address{{"Anywhere", tailnet}, {"At home", lan}}
	}
	return []Address{{"Address", lan}}
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// runSetup is `pc-remote setup`.
func runSetup(args []string, stdout io.Writer) int {
	if code, handled := platformSetup(args, stdout); handled {
		return code
	}
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	yes := flags.Bool("yes", false, "use the current settings or the defaults, without asking")
	access := flags.String("access", "", `how the phone connects: "lan", "tailscale" or "both"`)
	name := flags.String("name", "", "the title at the top of the app")
	theme := flags.String("theme", "", `"default", "omarchy" or your own theme`)
	party := flags.Bool("party", true, "let friends join games as players 2 to 4")
	bar := flags.Bool("bar", true, "show the Omarchy Remote icon in the Omarchy bar")
	flags.SetOutput(stdout)
	flags.Usage = func() {
		fmt.Fprintln(stdout, "Usage: "+cliName+" setup [--yes] [--access lan|tailscale|both] [--name NAME] [--theme THEME] [--party=false] [--bar=false]")
		fmt.Fprintln(stdout, "\nSets up Omarchy Remote. Without --yes it asks, step by step.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}

	dir, err := config.Dir()
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
	if set["bar"] {
		a.Bar = *bar && s.BarPluginDir != ""
	}
	if !config.ValidAccess(a.Access) {
		fmt.Fprintf(stdout, "--access must be \"lan\", \"tailscale\" or \"both\", not %q\n", a.Access)
		return 2
	}
	if !config.ThemeNamePattern.MatchString(a.Theme) {
		fmt.Fprintf(stdout, "--theme %q may only use letters, digits, - and _\n", a.Theme)
		return 2
	}

	if *yes && s.ConfigProblem != "" {
		fmt.Fprintln(stdout, "Your settings in "+tildePath(filepath.Join(dir, "config.toml"))+" have a mistake, so setup didn't touch them:")
		fmt.Fprintln(stdout, "  "+s.ConfigProblem)
		fmt.Fprintln(stdout, "Fix it, or run `"+cliName+" setup` without --yes to start over; it keeps a copy of the old file.")
		return 1
	}

	interactive := term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
	switch {
	case *yes:
		// Without a person to ask, nothing that needs a password runs.
		a.Install, a.OpenFirewall = false, false
	case !interactive:
		fmt.Fprintln(stdout, "Setup asks a few questions, so it needs a terminal.")
		fmt.Fprintln(stdout, "To set up without questions, run: "+cliName+" setup --yes [--access lan|tailscale|both]")
		return 2
	default:
		// Runs after apply, so the ready card stays up when the console
		// would otherwise close with setup.
		defer holdConsole(stdout)
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
	a.Name = cleanPCName(a.Name, s.DefaultName)
	if *yes && config.ViaTailscale(a.Access) && !s.TailscaleServing {
		fmt.Fprintf(stdout, "Note: run `"+adminPrefix+"tailscale serve --bg %d` to reach the app through Tailscale.\n", s.port())
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
func apply(s host, a answers, actions []action, out io.Writer) int {
	st := newStyles(out)
	fmt.Fprintln(out)
	for _, act := range actions {
		label := act.Label
		if act.Sudo {
			label += st.dim.Render("  (" + elevationNote + ")")
		}
		fmt.Fprintln(out, "  "+st.accent.Render("›")+" "+label)
		if err := act.Run(); err != nil {
			fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+err.Error())
			fmt.Fprintln(out, "\n  Fix that and run "+st.code.Render(cliName+" setup")+" again.")
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
	if err := waitForServer(written.Existing.ListenAddr()); err != nil {
		fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	passcode, err := config.LoadPasscode(s.Dir)
	if err != nil {
		fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, readyCard(st, appAddresses(s, a), passcode, terminalWidth()))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+st.dim.Render("Change settings later   ")+st.code.Render(cliName+" setup"))
	fmt.Fprintln(out, "  "+st.dim.Render("Something not working?  ")+st.code.Render(cliName+" doctor"))
	fmt.Fprintln(out)
	return 0
}
