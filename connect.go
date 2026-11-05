package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"mime"
	"net/http"
	"sync"
	"time"
)

// The tray's "Show passcode" page. The tray can't hand the passcode to a
// browser directly, and the server can't tell the tray's browser from a
// `tailscale serve` visitor (both come from loopback), so the tray mints a
// one-time ticket and opens connect.html with it after the #. The page trades
// the ticket for the passcode once.

// connectTicketLife is how long the browser has to open the page.
const connectTicketLife = time.Minute

type connectTickets struct {
	mu      sync.Mutex
	ticket  string // "" when none is waiting
	expires time.Time
	now     func() time.Time
}

func newConnectTickets() *connectTickets {
	return &connectTickets{now: time.Now}
}

// mint makes a ticket that replaces any earlier one.
func (c *connectTickets) mint() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ticket = hex.EncodeToString(b)
	c.expires = c.now().Add(connectTicketLife)
	return c.ticket, nil
}

// use reports whether ticket is the waiting one, and spends it.
func (c *connectTickets) use(ticket string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ticket == "" || !c.now().Before(c.expires) {
		c.ticket = ""
		return false
	}
	if subtle.ConstantTimeCompare([]byte(ticket), []byte(c.ticket)) != 1 {
		return false
	}
	c.ticket = ""
	return true
}

type connectInfo struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	URL      string   `json:"url"`
	QR       []string `json:"qr"`
	Passcode string   `json:"passcode"`
}

// handleConnect trades a tray ticket for what the phone needs to connect.
//
// It sits outside a.auth, since the ticket is the credential, but wrong
// tickets count against the same login guard as wrong passcodes.
func (a *app) handleConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	address := clientAddress(r, a.trustProxy)
	if a.guard != nil && a.guard.blocked(address) {
		writeError(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute.")
		return
	}
	// JSON only: a cross-site form can't send it without a CORS preflight.
	if kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); kind != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Bad request.")
		return
	}
	var body struct {
		Ticket string `json:"ticket"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if a.tickets == nil || !a.tickets.use(body.Ticket) {
		if a.guard != nil {
			a.guard.recordFailure(address)
		}
		writeError(w, http.StatusForbidden, "This link has expired. Open it again from the tray icon.")
		return
	}
	first := a.connectAddress()
	writeJSON(w, http.StatusOK, connectInfo{
		Name:     a.name,
		Label:    first.label,
		URL:      first.url,
		QR:       qrRows(first.url),
		Passcode: string(a.passcode),
	})
}

// connectAddress is the first address setup gives for the app: the one the
// bar card and the tray page show as a QR code.
func connectAddress(dir string, c config) address {
	s := system{Dir: dir, Existing: c, LANAddress: lanAddress()}
	if viaTailscale(c.Access) {
		s.TailscaleName, _ = tailscaleName()
	}
	return appAddresses(s, answers{Access: c.Access})[0]
}
