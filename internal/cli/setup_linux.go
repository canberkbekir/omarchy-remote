package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/canberkbekir/omarchy-remote/internal/config"
	"github.com/canberkbekir/omarchy-remote/internal/run"
)

const (
	cliName             = "pc-remote"
	adminPrefix         = "sudo "
	elevationNote       = "asks for your password"
	elevateOnceNote     = "Asks for your password once, after you review."
	tailscaleNeedsAdmin = true
	tailscaleLogin      = "Log in with sudo tailscale up"
	omarchyDesktop      = true // the phone can follow the Omarchy theme
	configRestartNote   = "# `systemctl --user restart pc-remote-web`. See the README.\n\n"
	restartHint         = "Restart the service to use it: systemctl --user restart pc-remote-web"
	notRunningProblem   = "Omarchy Remote isn't running. Start it with: systemctl --user start pc-remote-web"
)

func sudo(argv ...string) func() error {
	return func() error {
		cmd := exec.Command("sudo", argv...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
}

// installHint is the command that installs packages, for setup and doctor.
func installHint(packages []string) string {
	return "sudo pacman -S " + strings.Join(packages, " ")
}

func installPackages(packages []string) func() error {
	return sudo(append([]string{"pacman", "-S", "--needed", "--noconfirm"}, packages...)...)
}

// firewallActions adds a ufw rule for each blocked port, from the home network only.
func firewallActions(s host, blocked []int) []action {
	from := s.LANSubnet
	if from == "" {
		from = "192.168.0.0/16"
	}
	var actions []action
	for _, port := range blocked {
		actions = append(actions, action{
			Label: fmt.Sprintf("Let devices on %s reach port %d", from, port),
			Sudo:  true,
			Run:   sudo("ufw", "allow", "from", from, "to", "any", "port", strconv.Itoa(port), "proto", "tcp"),
		})
	}
	return actions
}

// firewallHint is what to run to open the ports by hand.
func firewallHint(from string, ports []int) string { return ufwRules(from, ports) }

func firewallExplain(from string, ports []int) string {
	return "Omarchy turns on the ufw firewall, which blocks every incoming connection unless a rule allows it. This adds: " + ufwRules(from, ports) + "."
}

// tailscaleTool is the tailscale command, if it is installed.
func tailscaleTool() (string, bool) {
	_, ok := lookPath("tailscale")
	return "tailscale", ok
}

func serveTailscale(port int) func() error {
	return sudo("tailscale", "serve", "--bg", strconv.Itoa(port))
}

// platformSetup handles setup's OS-only subcommands; Linux has none.
func platformSetup([]string, io.Writer) (int, bool) { return 0, false }

func autostartActions(s host) []action {
	var actions []action
	if !s.ServiceUnit {
		actions = append(actions, action{
			Label: "Add the background service",
			Run:   func() error { return installUnit(s.Executable) },
		})
	}
	return append(actions, action{
		Label: "Start Omarchy Remote now and on every login",
		Run:   startService,
	})
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
	_, err = run.Command("systemctl", "--user", "daemon-reload")
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

func serviceRunning(config.Config) bool {
	_, err := run.Command("systemctl", "--user", "is-active", "--quiet", "pc-remote-web.service")
	return err == nil
}

func restartService() error {
	_, err := run.Command("systemctl", "--user", "try-restart", "pc-remote-web.service")
	return err
}

func serviceFailed() bool {
	_, err := run.Command("systemctl", "--user", "is-failed", "--quiet", "pc-remote-web.service")
	return err == nil
}

// startFailure says why the server didn't answer, from its last log lines.
func startFailure() error {
	log, _ := run.Command("journalctl", "--user", "-u", "pc-remote-web.service", "-n", "5", "--no-pager", "-o", "cat")
	return fmt.Errorf("it didn't start. The last log lines:\n%s", strings.TrimSpace(log))
}

// prepareConsole: Linux terminals already understand colors.
func prepareConsole() {}

// holdConsole: a Linux terminal stays open after setup.
func holdConsole(io.Writer) {}
