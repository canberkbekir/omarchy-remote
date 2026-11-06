// Package cli is everything the pc-remote command does besides serving:
// setup, doctor, status, update and, on Windows, uninstall.
package cli

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/config"
)

// Version is the release, like v1.2.3. main sets it from its own version,
// which the build sets with -ldflags "-X main.version=v1.2.3".
var Version = "dev"

func VersionString() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return "dev (" + s.Value[:7] + ")"
			}
		}
	}
	return Version
}

// Run handles the subcommands. It reports false when there is
// none, so the server should start.
func Run(args []string, out io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	prepareConsole()
	switch args[0] {
	case "setup":
		return runSetup(args[1:], out), true
	case "doctor":
		return runDoctor(out), true
	case "passcode":
		return runPasscode(args[1:], out), true
	case "update":
		return runUpdate(args[1:], out), true
	case "status":
		return runStatus(args[1:], out), true
	case "version", "--version", "-v":
		fmt.Fprintln(out, "Omarchy Remote", VersionString())
		return 0, true
	case "help", "--help", "-h":
		fmt.Fprint(out, usage)
		return 0, true
	}
	if code, handled := platformCommand(args, out); handled {
		return code, true
	}
	fmt.Fprintf(out, "Unknown command %q.\n\n%s", args[0], usage)
	return 2, true
}

func runPasscode(args []string, out io.Writer) int {
	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--reset":
		if err := os.Remove(filepath.Join(dir, "passcode")); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(out, err)
			return 1
		}
		defer func() {
			if err := restartService(); err != nil {
				fmt.Fprintln(out, restartHint)
			}
		}()
	default:
		fmt.Fprintln(out, "Usage: "+cliName+" passcode [--reset]")
		return 2
	}
	passcode, err := config.LoadPasscode(dir)
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	fmt.Fprintln(out, passcode)
	return 0
}

// runDoctor checks what setup set up, and says how to fix what's wrong.
func runDoctor(out io.Writer) int {
	st := newStyles(out)
	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	s := detectSystem(dir)
	failed := 0
	line := func(ok bool, label, detail string) {
		mark := st.good.Render("✓")
		if !ok {
			mark = st.bad.Render("✗")
			failed++
		}
		fmt.Fprintf(out, "  %s  %-32s %s\n", mark, label, detail)
	}

	fmt.Fprintln(out, "\n  "+st.title.Render("This PC"))
	for _, c := range s.Checks {
		detail := st.dim.Render("ready")
		if !c.OK {
			detail = c.Detail
			if len(c.Fix) > 0 {
				detail += st.dim.Render(" · " + installHint(c.Fix))
			}
		}
		line(c.OK, c.Label, detail)
	}
	doctorHostLines(out, st, s, line)
	current := VersionString()
	if latest, err := LatestVersion(3 * time.Second); err == nil && NewerVersion(latest, current) {
		fmt.Fprintf(out, "  %s  %-32s %s\n", st.accent.Render("↑"), "Version", current+" · "+latest+" is out: "+st.code.Render(cliName+" update"))
	} else {
		fmt.Fprintf(out, "  %s  %-32s %s\n", st.good.Render("✓"), "Version", st.dim.Render(current))
	}

	fmt.Fprintln(out, "\n  "+st.title.Render("Omarchy Remote"))
	settings, err := config.Load(dir)
	if err != nil {
		line(false, "Settings", err.Error())
	} else {
		line(true, "Settings", st.dim.Render(tildePath(filepath.Join(dir, "config.toml"))))
	}
	if doctorServiceLine(st, s, settings, line) {
		addr := settings.ListenAddr()
		host, port, _ := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
		if err == nil {
			conn.Close()
		}
		line(err == nil, "Answering", ok(err == nil, addr, "nothing answers on "+addr))
	}

	a := answers{Access: settings.Access, Party: settings.GuestPort != 0}
	if config.ViaTailscale(settings.Access) {
		line(s.Tailscale, "Tailscale", ok(s.Tailscale, "logged in", "not logged in · "+adminPrefix+"tailscale up"))
		line(s.TailscaleServing, "Tailscale serve", ok(s.TailscaleServing, "https://"+s.TailscaleName,
			fmt.Sprintf("not set up · "+adminPrefix+"tailscale serve --bg %d", settings.Port)))
	}
	if s.Firewall {
		if s.FirewallOpen == nil {
			fmt.Fprintf(out, "  %s  %-32s %s\n", st.dim.Render("?"), "Firewall", st.dim.Render("can't read the ufw rules"))
		} else {
			blocked := s.firewallBlocks(a)
			from := s.LANSubnet
			if from == "" {
				from = "192.168.0.0/16"
			}
			line(len(blocked) == 0, "Firewall", ok(len(blocked) == 0, "lets the app through", firewallHint(from, blocked)))
		}
	}

	fmt.Fprintln(out)
	if failed > 0 {
		fmt.Fprintln(out, "  "+st.dim.Render("Fix the lines marked ✗, or run ")+st.code.Render(cliName+" setup")+st.dim.Render(" to go through it again."))
		fmt.Fprintln(out)
		return 1
	}
	fmt.Fprintln(out, "  Everything looks good. Open "+st.code.Render(appAddresses(s, a)[0].URL)+" on your phone.")
	fmt.Fprintln(out)
	return 0
}

func ok(good bool, yes, no string) string {
	if good {
		return yes
	}
	return no
}
