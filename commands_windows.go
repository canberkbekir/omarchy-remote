package main

import (
	"fmt"
	"io"
)

const usage = `Omarchy Remote: your phone as a remote for this PC.

Usage:
  pc-remote-web setup              set up step by step, or change settings
  pc-remote-web doctor             check that everything works
  pc-remote-web passcode           show the passcode
  pc-remote-web passcode --reset   make a new passcode; phones sign in again
  pc-remote-web update             install the latest release
  pc-remote-web status             say whether it runs and a phone is connected
  pc-remote-web version            show the version
  pc-remote-web uninstall          stop it and remove the logon task and firewall rules
  pc-remote-web uninstall --purge  the same, and delete the settings and passcode

Run without a command, pc-remote-web starts the server.
`

// doctorHostLines checks the network: the firewall rules only open Private
// ones, so on any other Windows Firewall keeps the phone out.
func doctorHostLines(out io.Writer, st styles, s system, line func(bool, string, string)) {
	category, err := networkCategory(s.LANAddress)
	switch {
	case err != nil || category == "":
		fmt.Fprintf(out, "  %s  %-32s %s\n", st.dim.Render("?"), "Network", st.dim.Render("can't tell whether it's private"))
	case category == "Private":
		line(true, "Network", st.dim.Render(category))
	case category == "DomainAuthenticated":
		line(false, "Network", "a work domain network: the firewall rules only open Private networks")
	default:
		line(false, "Network", category+", so Windows keeps the phone out · make it Private in Settings › Network & internet")
	}
}

// doctorServiceLine checks the logon task. Doctor then always checks that
// the server answers, since a task doesn't say whether it is running.
func doctorServiceLine(st styles, s system, _ config, line func(bool, string, string)) bool {
	line(s.ServiceUnit, "Logon task", ok(s.ServiceUnit, st.dim.Render("starts it when you sign in"), "missing · run "+cliName+" setup"))
	return true
}
