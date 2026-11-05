package main

import (
	"encoding/binary"
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
		if got := trayTip(c.name, c.p); got != c.want {
			t.Errorf("trayTip(%q, %+v) = %q, want %q", c.name, c.p, got, c.want)
		}
	}
	long := trayTip(strings.Repeat("ğ", 300), presence{})
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
	quit := 0
	do := func(id int) { a.trayCommand(id, "http://127.0.0.1:8377", open, func() { quit++ }) }

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

	do(trayQuit)
	do(99)
	if quit != 1 {
		t.Errorf("quit ran %d times", quit)
	}
}

// The tray picks the icon size Windows asks for, so the embedded .ico must
// carry the small sizes, from 100% to 200% scaling.
func TestTrayIconSizes(t *testing.T) {
	data, err := os.ReadFile("tray.ico")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{16, 20, 24, 32} {
		image, err := pickIcon(data, size)
		if err != nil {
			t.Fatalf("pickIcon(%d): %v", size, err)
		}
		if got := iconWidth(t, image); got != size {
			t.Errorf("pickIcon(%d) gave a %d-pixel image", size, got)
		}
	}
	if image, _ := pickIcon(data, 18); iconWidth(t, image) != 20 {
		t.Error("pickIcon(18) didn't take the next size up")
	}
	if image, _ := pickIcon(data, 512); iconWidth(t, image) != 64 {
		t.Error("pickIcon(512) didn't take the largest")
	}
	for _, bad := range [][]byte{nil, data[:5], data[:20], {0, 0, 2, 0, 1, 0}} {
		if _, err := pickIcon(bad, 16); err == nil {
			t.Errorf("pickIcon accepted %d bad bytes", len(bad))
		}
	}
}

// iconWidth reads an icon image's width: a PNG or a BITMAPINFOHEADER.
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
