// Package api is what the server answers on /api/presence, and a client
// for it: the CLI status command and the Windows tray read it back.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/config"
)

type PlayerView struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Delay     int    `json:"delay"`
}

type PartyView struct {
	Active  bool         `json:"active"`
	Code    string       `json:"code,omitempty"`
	Link    string       `json:"link,omitempty"`
	QR      []string     `json:"qr,omitempty"`
	Players []PlayerView `json:"players"`
}

type Presence struct {
	Phone bool      `json:"phone"`
	Party PartyView `json:"party"`
}

// AskPresence asks the running server, the way the phone does.
func AskPresence(c config.Config, passcode string) (Presence, error) {
	base, err := LocalURL(c.ListenAddr())
	if err != nil {
		return Presence{}, err
	}
	req, err := http.NewRequest("GET", base+"/api/presence", nil)
	if err != nil {
		return Presence{}, err
	}
	req.Header.Set("Authorization", "Bearer "+passcode)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return Presence{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Presence{}, errors.New(resp.Status)
	}
	var p Presence
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&p)
	return p, err
}

// LocalURL is how this PC reaches the server listening on addr.
func LocalURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
