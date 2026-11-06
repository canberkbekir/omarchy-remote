package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
)

// platformCommand runs the Windows-only commands: uninstall.
func platformCommand(args []string, out io.Writer) (int, bool) {
	if args[0] != "uninstall" {
		return 0, false
	}
	return runUninstall(args[1:], out), true
}

// runUninstall stops the server and takes away what setup added: the logon
// task and the firewall rules, in one permission prompt. The installer's
// uninstaller runs it; the program files are the installer's to remove.
// --purge also deletes the settings and passcode. The ViGEmBus driver stays,
// since other programs may use it.
func runUninstall(args []string, out io.Writer) int {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(out)
	purge := flags.Bool("purge", false, "also delete the settings and passcode")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		fmt.Fprintln(out, "Usage: "+cliName+" uninstall [--purge]")
		return 2
	}
	st := newStyles(out)
	done := func(text string) { fmt.Fprintln(out, "  "+st.good.Render("✓")+" "+text) }
	failed := 0
	fail := func(text string) {
		fmt.Fprintln(out, "  "+st.bad.Render("✗")+" "+text)
		failed++
	}

	dir, err := configDir()
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	c, _ := loadConfig(dir) // the defaults when there are no settings

	winRun("schtasks", "/End", "/TN", taskName) // fails when it isn't running
	if err := stopServers(); err != nil {
		fail("couldn't stop the server: " + err.Error())
	} else {
		done("Stopped the server")
	}

	if _, err := winRun("schtasks", "/Query", "/TN", taskName); err == nil {
		if out, err := winRun("schtasks", "/Delete", "/TN", taskName, "/F"); err != nil {
			fail("couldn't remove the logon task: " + commandError(err, out).Error())
		} else {
			done("Removed the logon task")
		}
	}

	var ports []int
	for _, p := range []int{c.Port, c.GuestPort, defaultPort, defaultGuestPort} {
		if p != 0 && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	var rules []int
	for _, p := range ports {
		if firewallOpenPorts([]int{p})[p] {
			rules = append(rules, p)
		}
	}
	if len(rules) > 0 {
		exe, params, err := firewallDeleteCommand(rules)
		if err == nil {
			err = elevate(exe, params)
		}
		var names []string
		for _, p := range rules {
			names = append(names, firewallRuleName(p))
		}
		if err != nil {
			fail(fmt.Sprintf("the firewall rules %v stay: %v", names, err))
		} else {
			done("Removed the firewall rules")
		}
	}

	if *purge {
		if err := os.RemoveAll(dir); err != nil {
			fail("couldn't delete the settings: " + err.Error())
		} else {
			done("Deleted the settings and passcode in " + tildePath(dir))
		}
	} else {
		fmt.Fprintln(out, "  Kept the settings and passcode in "+tildePath(dir)+"; --purge deletes them.")
	}
	fmt.Fprintln(out, "  The ViGEmBus driver stays: remove \"ViGEm Bus Driver\" in Settings › Apps if nothing else uses it.")
	if failed > 0 {
		return 1
	}
	return 0
}
