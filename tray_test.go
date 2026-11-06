package main

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTrayTip(t *testing.T) {
	party := partyView{Active: true, Code: "ABCD", Players: []playerView{
		{Slot: 2, Connected: true}, {Slot: 3, Connected: true}, {Slot: 4},
	}}
	cases := []struct {
		name string
		p    presence
		want string
	}{
		{"PC Remote", presence{}, "PC Remote: no phone"},
		{"PC Remote", presence{Phone: true}, "PC Remote: phone connected"},
		{"", presence{Phone: true}, "Omarchy Remote: phone connected"},
		{"Den\x00Evil", presence{}, "DenEvil: no phone"},
		{"Den", presence{Phone: true, Party: party}, "Den: phone connected · party ABCD 2/3"},
	}
	for _, c := range cases {
		if got := trayTip(c.name, c.p, false); got != c.want {
			t.Errorf("trayTip(%q, %+v) = %q, want %q", c.name, c.p, got, c.want)
		}
	}
	long := trayTip(strings.Repeat("ğ", 300), presence{}, false)
	if n := len([]rune(long)); n > trayTipMax || !utf8.ValidString(long) {
		t.Errorf("long tip has %d runes (max %d), valid=%v", n, trayTipMax, utf8.ValidString(long))
	}
}

func TestStartPartyNeedsGuestPort(t *testing.T) {
	a := &app{party: newParty()}
	if err := a.startParty(); err == nil {
		t.Fatal("started a party with parties turned off")
	}
	if a.party.active() {
		t.Error("party active after a refused start")
	}
}

func TestLocalURL(t *testing.T) {
	cases := map[string]string{
		":8377":            "http://127.0.0.1:8377",
		"0.0.0.0:8377":     "http://127.0.0.1:8377",
		"[::]:8377":        "http://127.0.0.1:8377",
		"127.0.0.1:9000":   "http://127.0.0.1:9000",
		"192.168.1.5:8377": "http://192.168.1.5:8377",
		"[fd00::5]:8377":   "http://[fd00::5]:8377",
		"localhost:8377":   "http://localhost:8377",
	}
	for addr, want := range cases {
		if got, err := localURL(addr); err != nil || got != want {
			t.Errorf("localURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := localURL("nonsense"); err == nil {
		t.Error("localURL accepted an address without a port")
	}
}

func TestTrayCommand(t *testing.T) {
	a := &app{party: newParty(), tickets: newConnectTickets()}
	var opened []string
	open := func(url string) error { opened = append(opened, url); return nil }
	quit, update := 0, 0
	do := func(id int) {
		a.trayCommand(id, "http://127.0.0.1:8377", trayActions{
			open: open, quit: func() { quit++ }, update: func() { update++ },
		})
	}

	do(trayShowPasscode)
	if len(opened) != 1 || !strings.HasPrefix(opened[0], "http://127.0.0.1:8377/connect.html#") {
		t.Fatalf("show passcode opened %q", opened)
	}
	if ticket := strings.TrimPrefix(opened[0], "http://127.0.0.1:8377/connect.html#"); !a.tickets.use(ticket) {
		t.Error("the opened page's ticket doesn't work")
	}

	do(trayOpenApp)
	if opened[len(opened)-1] != "http://127.0.0.1:8377/" {
		t.Errorf("open app opened %q", opened[len(opened)-1])
	}

	do(trayParty) // parties are off: nothing starts
	if a.party.active() {
		t.Error("tray started a party with parties turned off")
	}
	a.party.start()
	do(trayParty)
	if a.party.active() {
		t.Error("tray didn't end the party")
	}

	do(trayUpdate)
	if update != 1 {
		t.Errorf("update ran %d times", update)
	}

	do(trayQuit)
	do(99)
	if quit != 1 {
		t.Errorf("quit ran %d times", quit)
	}
}

// The menu says when a release is out; otherwise it offers to look.
func TestTrayUpdateLabel(t *testing.T) {
	tests := []struct{ current, latest, want string }{
		{"v0.2.0", "v0.3.0", "Update to v0.3.0"},
		{"v0.2.0", "v0.2.0", "Check for updates"},
		{"v0.3.0", "v0.2.0", "Check for updates"},
		{"v0.2.0", "", "Check for updates"}, // not checked yet, or offline
		{"dev (836cf4d)", "v0.3.0", "Check for updates"},
	}
	for _, tt := range tests {
		if got := trayUpdateLabel(tt.current, tt.latest); got != tt.want {
			t.Errorf("trayUpdateLabel(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
		}
	}
}

// The tray picks the icon size Windows asks for, so every icon must carry
// the small sizes, from 100% to 300% scaling.
func TestTrayIconSizes(t *testing.T) {
	for _, name := range trayIconNames() {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range []int{16, 20, 24, 32, 40, 48} {
			image, err := pickIcon(data, size)
			if err != nil {
				t.Fatalf("%s: pickIcon(%d): %v", name, size, err)
			}
			if got := iconWidth(t, image); got != size {
				t.Errorf("%s: pickIcon(%d) gave a %d-pixel image", name, size, got)
			}
		}
		if image, _ := pickIcon(data, 18); iconWidth(t, image) != 20 {
			t.Errorf("%s: pickIcon(18) didn't take the next size up", name)
		}
		if image, _ := pickIcon(data, 512); iconWidth(t, image) != 48 {
			t.Errorf("%s: pickIcon(512) didn't take the largest", name)
		}
	}
	data, _ := os.ReadFile(trayIconName(false, "idle"))
	for _, bad := range [][]byte{nil, data[:5], data[:20], {0, 0, 2, 0, 1, 0}} {
		if _, err := pickIcon(bad, 16); err == nil {
			t.Errorf("pickIcon accepted %d bad bytes", len(bad))
		}
	}
}

func TestTrayIconNames(t *testing.T) {
	names := trayIconNames()
	if len(names) != 12 {
		t.Fatalf("%d icons, want 12: %q", len(names), names)
	}
	if got := trayIconName(true, "party2"); got != "trayicons/light-party2.ico" {
		t.Errorf("trayIconName = %q", got)
	}
}

func TestTrayState(t *testing.T) {
	party := func(joined int) partyView {
		v := partyView{Active: true, Code: "ABCD"}
		for i := range 3 {
			v.Players = append(v.Players, playerView{Connected: i < joined})
		}
		return v
	}
	tests := []struct {
		name string
		p    presence
		warn bool
		want string
	}{
		{"nobody", presence{}, false, "idle"},
		{"phone", presence{Phone: true}, false, "connected"},
		{"party, no guest yet", presence{Phone: true, Party: party(0)}, false, "connected"},
		{"party, no guest, no phone", presence{Party: party(0)}, false, "idle"},
		{"one guest", presence{Party: party(1)}, false, "party1"},
		{"three guests", presence{Phone: true, Party: party(3)}, false, "party3"},
		{"blocked network wins", presence{Phone: true, Party: party(2)}, true, "warning"},
	}
	for _, tt := range tests {
		if got := trayState(tt.p, tt.warn); got != tt.want {
			t.Errorf("%s: trayState = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// The warning shows only when the phone is meant to reach the PC over the
// network and Windows Firewall keeps it out there.
func TestNetworkWarns(t *testing.T) {
	tests := []struct {
		category string
		err      error
		loopback bool
		want     bool
	}{
		{"Private", nil, false, false},
		{"Public", nil, false, true},
		{"DomainAuthenticated", nil, false, true},
		{"Public", nil, true, false}, // only this PC uses it
		{"", nil, false, false},      // can't tell
		{"Public", errors.New("powershell failed"), false, false},
	}
	for _, tt := range tests {
		if got := networkWarns(tt.category, tt.err, tt.loopback); got != tt.want {
			t.Errorf("networkWarns(%q, %v, %v) = %v, want %v", tt.category, tt.err, tt.loopback, got, tt.want)
		}
	}
}

func TestTrayTipSaysWhyItWarns(t *testing.T) {
	if got, want := trayTip("pc", presence{}, true), "pc: no phone · the firewall blocks this network"; got != want {
		t.Errorf("trayTip = %q, want %q", got, want)
	}
}

func iconWidth(t *testing.T, image []byte) int {
	t.Helper()
	if len(image) >= 24 && string(image[1:4]) == "PNG" {
		return int(binary.BigEndian.Uint32(image[16:]))
	}
	if len(image) < 8 {
		t.Fatal("image too short")
	}
	return int(int32(binary.LittleEndian.Uint32(image[4:])))
}
