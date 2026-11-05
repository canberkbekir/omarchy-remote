package main

import (
	"net"
	"strings"
)

// detectPlatform fills in the Windows checks: SendInput, the logon task and
// the firewall rules.
func detectPlatform(s *system) {
	c := check{Label: "Keyboard and trackpad", OK: sendInputWorks()}
	if !c.OK {
		c.Detail = "SendInput doesn't work: run it on your own unlocked desktop, not as a service"
	}
	s.Checks = append([]check{c}, s.Checks...)
	s.Firewall = true
	s.FirewallOpen = firewallOpenPorts([]int{s.port(), s.guestPort()})
	s.ServiceUnit = taskInstalled(s.Executable)
}

// networkCategory says whether Windows calls the home network Private,
// Public or DomainAuthenticated. The firewall rules only open Private ones.
func networkCategory(lan string) (string, error) {
	script := "(Get-NetConnectionProfile).NetworkCategory"
	if ip := net.ParseIP(lan); ip != nil {
		script = "(Get-NetConnectionProfile -InterfaceIndex (Get-NetIPAddress -IPAddress '" + ip.String() + "').InterfaceIndex).NetworkCategory"
	}
	out, err := winRun("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return "", err
	}
	var found string
	for _, line := range strings.Fields(out) {
		if found == "" || line != "Private" {
			found = line
		}
	}
	return found, nil
}
