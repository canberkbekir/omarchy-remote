package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/canberkbekir/omarchy-remote/internal/api"
	"github.com/canberkbekir/omarchy-remote/internal/cli"
)

// trayTipMax is the longest tooltip the tray takes: NOTIFYICONDATA.szTip
// holds 128 UTF-16 units including the terminating zero.
const trayTipMax = 127

// trayTip is the tray icon's tooltip: the app's name and whether a phone is
// connected, plus the party when one runs, and why the icon warns.
func trayTip(name string, p api.Presence, warn bool) string {
	name = strings.ReplaceAll(name, "\x00", "") // Windows strings end at a NUL
	if name == "" {
		name = "Omarchy Remote"
	}
	var tip strings.Builder
	tip.WriteString(name)
	if p.Phone {
		tip.WriteString(": phone connected")
	} else {
		tip.WriteString(": no phone")
	}
	if p.Party.Active {
		joined := 0
		for _, player := range p.Party.Players {
			if player.Connected {
				joined++
			}
		}
		fmt.Fprintf(&tip, " · party %s %d/%d", p.Party.Code, joined, lastGuestSlot-firstGuestSlot+1)
	}
	if warn {
		tip.WriteString(" · the firewall blocks this network")
	}
	// Cut on a rune boundary; every rune counted here fits in one UTF-16
	// unit or two, so count units, not runes.
	units, out := 0, []rune{}
	for _, r := range tip.String() {
		n := 1
		if r > 0xFFFF {
			n = 2
		}
		if units+n > trayTipMax {
			break
		}
		units += n
		out = append(out, r)
	}
	return string(out)
}

// trayState picks the tray icon: a warning first, then the guests in a
// party, then whether a phone is connected.
func trayState(p api.Presence, warn bool) string {
	joined := 0
	if p.Party.Active {
		for _, player := range p.Party.Players {
			if player.Connected {
				joined++
			}
		}
	}
	switch {
	case warn:
		return "warning"
	case joined > 0:
		return fmt.Sprintf("party%d", min(joined, lastGuestSlot-firstGuestSlot+1))
	case p.Phone:
		return "connected"
	}
	return "idle"
}

// trayIconName is the icon for a state, drawn for a light or a dark taskbar
// by scripts/tray-icons.sh.
func trayIconName(light bool, state string) string {
	theme := "dark"
	if light {
		theme = "light"
	}
	return "trayicons/" + theme + "-" + state + ".ico"
}

func trayIconNames() []string {
	var names []string
	for _, light := range []bool{false, true} {
		for _, state := range []string{"idle", "connected", "party1", "party2", "party3", "warning"} {
			names = append(names, trayIconName(light, state))
		}
	}
	return names
}

// networkWarns reports whether the tray should warn about the network: the
// firewall rules only open Private networks, so on any other one the phone
// can't get in. A server only this PC uses, or a category Windows didn't
// give, doesn't warn.
func networkWarns(category string, err error, loopback bool) bool {
	return err == nil && !loopback && category != "" && category != "Private"
}

// The tray menu's commands. Left-clicking the icon runs trayShowPasscode.
const (
	trayShowPasscode = 1 + iota
	trayOpenApp
	trayParty
	trayUpdate
	trayQuit
)

// trayActions are what tray commands do outside the app; tests fake them.
type trayActions struct {
	open   func(url string) error // shows a page in the browser
	quit   func()
	update func() // runs the updater, which replaces this server
}

// trayUpdateLabel names the update item: the release to get when a newer
// one is out, or a check. A build from source can't tell what's newer.
func trayUpdateLabel(current, latest string) string {
	if updateAvailable(current, latest) {
		return "Update to " + latest
	}
	return "Check for updates"
}

// updateAvailable reports whether latest is a release newer than current.
func updateAvailable(current, latest string) bool {
	_, ok := cli.ParseVersion(current)
	return ok && latest != "" && cli.NewerVersion(latest, current)
}

// trayCommand runs a tray menu command. base is the server's local address;
// do carries what commands do outside the app.
func (a *app) trayCommand(id int, base string, do trayActions) {
	switch id {
	case trayShowPasscode:
		ticket, err := a.tickets.mint()
		if err != nil {
			log.Printf("tray: %v", err)
			return
		}
		if err := do.open(base + "/connect.html#" + ticket); err != nil {
			log.Printf("tray: opening the passcode page: %v", err)
		}
	case trayOpenApp:
		if err := do.open(base + "/"); err != nil {
			log.Printf("tray: opening the app: %v", err)
		}
	case trayParty:
		if a.party.active() {
			a.endParty()
		} else if err := a.startParty(); err != nil {
			log.Printf("tray: starting a party: %v", err)
		}
	case trayUpdate:
		do.update()
	case trayQuit:
		do.quit()
	}
}

// pickIcon finds the image in an .ico file that suits size pixels: that size,
// else the next larger, else the largest. The image is what
// CreateIconFromResourceEx takes.
func pickIcon(ico []byte, size int) ([]byte, error) {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[0:]) != 0 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil, errors.New("not an icon file")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	if count == 0 || len(ico) < 6+16*count {
		return nil, errors.New("icon file cut short")
	}
	best, bestWidth := -1, 0
	for i := range count {
		width := int(ico[6+16*i])
		if width == 0 {
			width = 256
		}
		better := best < 0 ||
			width >= size && (bestWidth < size || width < bestWidth) ||
			width < size && bestWidth < size && width > bestWidth
		if better {
			best, bestWidth = i, width
		}
	}
	entry := ico[6+16*best:]
	length := int(binary.LittleEndian.Uint32(entry[8:]))
	offset := int(binary.LittleEndian.Uint32(entry[12:]))
	if length == 0 || offset < 6+16*count || offset > len(ico) || length > len(ico)-offset {
		return nil, errors.New("icon image out of range")
	}
	return ico[offset : offset+length], nil
}
