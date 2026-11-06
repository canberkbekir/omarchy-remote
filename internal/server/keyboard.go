package server

import (
	"context"
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/canberkbekir/omarchy-remote/internal/input"
)

const (

	// Keys are released if the phone goes quiet for this long, so a dropped
	// connection can't leave one held down. The phone pings every second.
	keyboardTimeout     = 3 * time.Second
	keyboardAuthTimeout = 5 * time.Second
)

func (a *app) keyboardDevice() (*input.Keyboard, error) {
	a.keyboardMu.Lock()
	defer a.keyboardMu.Unlock()
	if a.keyboard == nil {
		device, err := a.openKeyboard()
		if err != nil {
			return nil, err
		}
		a.keyboard = device
	}
	return a.keyboard, nil
}

// startKeyboardSession ends any older session, so one connection owns the keys.
func (a *app) startKeyboardSession(cancel context.CancelFunc) {
	a.keyboardMu.Lock()
	defer a.keyboardMu.Unlock()
	if a.endKeyboardSession != nil {
		a.endKeyboardSession()
	}
	a.endKeyboardSession = cancel
}

// acceptSocket opens a WebSocket and checks the passcode. Browsers can't set
// headers on a WebSocket, so the passcode is the first message. On failure it
// returns nil and has already answered.
func (a *app) acceptSocket(w http.ResponseWriter, r *http.Request) *websocket.Conn {
	// Counted now: the passcode only arrives after the socket opens.
	address := clientAddress(r, a.trustProxy)
	if !a.guard.reserve(address) {
		writeError(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute.")
		return nil
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		a.guard.refund(address)
		return nil
	}
	conn.SetReadLimit(maxBodyBytes)

	authContext, cancelAuth := context.WithTimeout(context.Background(), keyboardAuthTimeout)
	defer cancelAuth()
	_, supplied, err := conn.Read(authContext)
	if err != nil {
		a.guard.refund(address)
		conn.CloseNow()
		return nil
	}
	if subtle.ConstantTimeCompare(supplied, a.passcode) != 1 {
		conn.Close(websocket.StatusPolicyViolation, "Wrong passcode.")
		return nil
	}
	a.guard.clear(address)
	return conn
}

func (a *app) handleKeyboard(w http.ResponseWriter, r *http.Request) {
	conn := a.acceptSocket(w, r)
	if conn == nil {
		return
	}
	defer conn.CloseNow()

	device, err := a.keyboardDevice()
	if err != nil {
		log.Printf("opening keyboard: %v", err)
		conn.Close(websocket.StatusInternalError, "Could not create keyboard.")
		return
	}
	session, end := context.WithCancel(context.Background())
	defer end()
	a.startKeyboardSession(end)
	device.ReleaseAll()
	defer device.ReleaseAll()
	defer a.pointer.endSession()

	if err := conn.Write(session, websocket.MessageText, []byte("ok")); err != nil {
		return
	}
	for {
		readContext, cancelRead := context.WithTimeout(session, a.keyboardTimeout)
		_, message, err := conn.Read(readContext)
		cancelRead()
		if err != nil {
			return
		}
		if err := device.HandleMessage(string(message)); err != nil {
			log.Printf("sending key: %v", err)
			return
		}
	}
}

// clientAddress names the device for the passcode lockout. Behind a local proxy
// such as `tailscale serve`, every request comes from loopback, so the device is
// read from the X-Forwarded-For entry the proxy appended. That needs trustProxy
// and a loopback connection: without a proxy, loopback is a local program.
func clientAddress(r *http.Request, trustProxy bool) string {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if ip := net.ParseIP(address); !trustProxy || ip == nil || !ip.IsLoopback() {
		return address
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return address
	}
	entries := strings.Split(forwarded[len(forwarded)-1], ",")
	if ip := net.ParseIP(strings.TrimSpace(entries[len(entries)-1])); ip != nil {
		return ip.String()
	}
	return address
}
