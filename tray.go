package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
)

// trayTipMax is the longest tooltip the tray takes: NOTIFYICONDATA.szTip
// holds 128 UTF-16 units including the terminating zero.
const trayTipMax = 127

// trayTip is the tray icon's tooltip: the app's name and whether a phone is
// connected, plus the party when one runs.
func trayTip(name string, p presence) string {
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

// The tray menu's commands. Left-clicking the icon runs trayShowPasscode.
const (
	trayShowPasscode = 1 + iota
	trayOpenApp
	trayParty
	trayQuit
)

// trayCommand runs a tray menu command. base is the server's local address;
// open shows a page in the browser.
func (a *app) trayCommand(id int, base string, open func(url string) error, quit func()) {
	switch id {
	case trayShowPasscode:
		ticket, err := a.tickets.mint()
		if err != nil {
			log.Printf("tray: %v", err)
			return
		}
		if err := open(base + "/connect.html#" + ticket); err != nil {
			log.Printf("tray: opening the passcode page: %v", err)
		}
	case trayOpenApp:
		if err := open(base + "/"); err != nil {
			log.Printf("tray: opening the app: %v", err)
		}
	case trayParty:
		if a.party.active() {
			a.endParty()
		} else if err := a.startParty(); err != nil {
			log.Printf("tray: starting a party: %v", err)
		}
	case trayQuit:
		quit()
	}
}

// localURL is how this PC reaches the server listening on addr.
func localURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
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
