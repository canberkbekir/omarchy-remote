package cli

import (
	"io"

	"github.com/canberkbekir/omarchy-remote/internal/config"
)

const usage = `Omarchy Remote: your phone as a remote for this PC.

Usage:
  pc-remote setup              set up step by step, or change settings
  pc-remote doctor             check that everything works
  pc-remote passcode           show the passcode
  pc-remote passcode --reset   make a new passcode; phones sign in again
  pc-remote update             install the latest release
  pc-remote status             say whether it runs and a phone is connected
  pc-remote version            show the version
  pc-remote <command>          run a command in the Hyprland session

Run without a command, pc-remote-web starts the server.
`

// platformCommand runs the OS-only commands; Linux has none.
func platformCommand([]string, io.Writer) (int, bool) { return 0, false }

// doctorHostLines adds the Linux checks to the "This PC" list.
func doctorHostLines(_ io.Writer, _ styles, _ host, line func(bool, string, string)) {
	_, helper := lookPath("pc-remote")
	line(helper, "Session helper", ok(helper, "pc-remote is installed", "pc-remote is missing: run ./install.sh again"))
}

// doctorServiceLine checks the systemd service, and reports whether it runs,
// so doctor then checks that it answers.
func doctorServiceLine(_ styles, _ host, settings config.Config, line func(bool, string, string)) bool {
	running := serviceRunning(settings)
	line(running, "Background service", ok(running, "running", "not running · journalctl --user -u pc-remote-web"))
	return running
}
