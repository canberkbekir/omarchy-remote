package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"
	"rsc.io/qr"
)

// A party lets friends on the home Wi-Fi join as players 2 to 4. They get a
// controller and nothing else: the guest port serves only the join page and
// the guest controller, accepts only home-network addresses, and is open only
// while a party runs.
const (
	partyCodeLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ" // no I or O, easy to mishear
	partyCodeLength  = 4
	firstGuestSlot   = 2
	lastGuestSlot    = 4
	maxNameRunes     = 20

	// Wrong codes from all devices together; past this, joining pauses for a
	// minute, so several devices can't share the per-device allowance.
	maxJoinFailures = 20

	// Close code sent to a guest's controller when the host removes them or
	// ends the party; the reason says which.
	statusPartyOver = websocket.StatusCode(4000)
)

var (
	errNoParty   = errors.New("no party")
	errWrongCode = errors.New("wrong code")
	errFull      = errors.New("party full")
	errNoGuest   = errors.New("unknown guest")
)

// leftError says why a guest's token no longer works.
type leftError struct{ reason string }

func (e *leftError) Error() string { return "guest " + e.reason }

type guest struct {
	slot      int
	name      string
	token     string
	delay     int // ms, as the phone measured it; 0 until known
	pad       *gamepad
	connected bool
	conn      *websocket.Conn    // the phone's current connection
	session   context.Context    // its session
	end       context.CancelFunc // ends it without a word, when the same phone reconnects
	reason    string             // set when removed: "removed" or "ended"
}

type party struct {
	mu       sync.Mutex
	code     string // empty when no party runs
	guests   map[int]*guest
	gone     map[string]string // token → why that guest left, until the next party
	failures []time.Time
	server   *http.Server
}

func newParty() *party {
	return &party{guests: map[int]*guest{}, gone: map[string]string{}}
}

func randomCode() (string, error) {
	code := make([]byte, partyCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(partyCodeLetters))))
		if err != nil {
			return "", err
		}
		code[i] = partyCodeLetters[n.Int64()]
	}
	return string(code), nil
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// cleanName keeps a guest's name short and printable.
func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if runes := []rune(name); len(runes) > maxNameRunes {
		name = string(runes[:maxNameRunes])
	}
	return name
}

func (p *party) active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code != ""
}

func (p *party) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code != "" {
		return nil
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	p.code = code
	p.failures = nil
	p.gone = map[string]string{}
	return nil
}

// newCode changes the code; players already in stay.
func (p *party) newCode() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" {
		return errNoParty
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	p.code = code
	return nil
}

// end removes every guest and closes the party.
func (p *party) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for slot := range p.guests {
		p.removeLocked(slot, "ended")
	}
	p.code = ""
}

func (p *party) remove(slot int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removeLocked(slot, "removed")
}

// removeLocked tells the phone why it's leaving; the close also ends its
// session. (Cancelling the session first would drop the connection before the
// reason got out.) The controller is unplugged here if the phone is away, or
// by the connection handler as it leaves.
func (p *party) removeLocked(slot int, reason string) {
	g := p.guests[slot]
	if g == nil {
		return
	}
	delete(p.guests, slot)
	g.reason = reason
	p.gone[g.token] = reason
	if g.connected {
		go g.conn.Close(statusPartyOver, reason)
	} else if g.pad != nil {
		g.pad.close()
	}
}

// tooManyFailures reports whether wrong codes from all devices together have
// hit the limit within the last minute.
func (p *party) tooManyFailures() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-lockoutDuration)
	recent := p.failures[:0]
	for _, at := range p.failures {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	p.failures = recent
	return len(p.failures) >= maxJoinFailures
}

func (p *party) join(code, name string) (*guest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" {
		return nil, errNoParty
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(code))), []byte(p.code)) != 1 {
		p.failures = append(p.failures, time.Now())
		return nil, errWrongCode
	}
	for slot := firstGuestSlot; slot <= lastGuestSlot; slot++ {
		if p.guests[slot] != nil {
			continue
		}
		token, err := randomToken()
		if err != nil {
			return nil, err
		}
		g := &guest{slot: slot, name: name, token: token}
		p.guests[slot] = g
		return g, nil
	}
	return nil, errFull
}

// connect starts a phone's connection for a guest, ending any older one from
// the same guest, and makes their controller on first use.
func (p *party) connect(token string, conn *websocket.Conn, openPad func() (*gamepad, error)) (*guest, context.Context, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var g *guest
	for _, candidate := range p.guests {
		if subtle.ConstantTimeCompare([]byte(token), []byte(candidate.token)) == 1 {
			g = candidate
		}
	}
	if g == nil {
		// A phone that was away when it was removed still learns why.
		if reason, ok := p.gone[token]; ok {
			return nil, nil, &leftError{reason}
		}
		return nil, nil, errNoGuest
	}
	if g.pad == nil {
		pad, err := openPad()
		if err != nil {
			return nil, nil, err
		}
		g.pad = pad
	}
	if g.connected {
		g.end()
	}
	session, end := context.WithCancel(context.Background())
	g.connected = true
	g.conn = conn
	g.session = session
	g.end = end
	return g, session, nil
}

// disconnect records that a connection left, and unplugs the controller if the
// guest was removed meanwhile.
func (p *party) disconnect(g *guest, session context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if g.reason != "" {
		g.pad.close()
		return
	}
	// A newer connection from the same phone may have taken over already.
	if g.session == session {
		g.connected = false
	}
}

// leave removes the guest with this token, when they tap Leave game.
func (p *party) leave(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for slot, g := range p.guests {
		if subtle.ConstantTimeCompare([]byte(token), []byte(g.token)) == 1 {
			p.removeLocked(slot, "left")
		}
	}
}

func (p *party) setDelay(g *guest, ms int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	g.delay = ms
}

type playerView struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Delay     int    `json:"delay"`
}

type partyView struct {
	Active  bool         `json:"active"`
	Code    string       `json:"code,omitempty"`
	Link    string       `json:"link,omitempty"`
	QR      []string     `json:"qr,omitempty"`
	Players []playerView `json:"players"`
}

func (p *party) view() partyView {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := partyView{Active: p.code != "", Code: p.code, Players: []playerView{}}
	for slot := firstGuestSlot; slot <= lastGuestSlot; slot++ {
		if g := p.guests[slot]; g != nil {
			v.Players = append(v.Players, playerView{Slot: slot, Name: g.name, Connected: g.connected, Delay: g.delay})
		}
	}
	return v
}

// lanAddress is this PC's address on the home network: the source address the
// system would use for a public address. Dialing UDP sends nothing.
func lanAddress() string {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// qrRows draws the link as rows of "1" (dark) and "0" (light) for the page.
func qrRows(text string) []string {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	rows := make([]string, code.Size)
	for y := range code.Size {
		var row strings.Builder
		for x := range code.Size {
			if code.Black(x, y) {
				row.WriteByte('1')
			} else {
				row.WriteByte('0')
			}
		}
		rows[y] = row.String()
	}
	return rows
}

func (a *app) partyLink(code string) string {
	host := net.JoinHostPort(lanAddress(), strconv.Itoa(a.guestPort))
	return "http://" + host + "/#" + code
}

func (a *app) handlePartyRead(w http.ResponseWriter, r *http.Request) {
	v := a.party.view()
	if v.Active {
		v.Link = a.partyLink(v.Code)
		v.QR = qrRows(v.Link)
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *app) handlePartyChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Slot   int    `json:"slot"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch body.Action {
	case "start":
		if a.guestPort == 0 {
			writeError(w, http.StatusConflict, "Parties are turned off in config.toml.")
			return
		}
		if err := a.startGuestServer(); err != nil {
			log.Printf("opening guest port: %v", err)
			writeError(w, http.StatusInternalServerError, "Couldn’t open the guest port.")
			return
		}
		if err := a.party.start(); err != nil {
			writeError(w, http.StatusInternalServerError, "Couldn’t start the party.")
			return
		}
	case "code":
		if err := a.party.newCode(); err != nil {
			writeError(w, http.StatusConflict, "No party is running.")
			return
		}
	case "remove":
		a.party.remove(body.Slot)
	case "end":
		a.party.end()
		a.stopGuestServer()
	default:
		writeError(w, http.StatusBadRequest, "Bad request.")
		return
	}
	a.handlePartyRead(w, r)
}

// homeNetworkOnly refuses addresses outside the home network, so the guest
// port stays private even if the router forwards it by mistake.
func homeNetworkOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			http.Error(w, "Only devices on the home network can join.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guestHandler serves the guest port: the join page with the few files it
// needs, joining, and the guest controller. Nothing from the admin app.
func (a *app) guestHandler() http.Handler {
	site, _ := fs.Sub(webFiles, "web")
	files := http.FileServerFS(site)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, site, "join.html")
	})
	for _, name := range []string{"/base.css", "/gamepad.js", "/icon-180.png"} {
		mux.HandleFunc("GET "+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /theme.css", a.themes.handleTheme)
	mux.HandleFunc("GET /user.css", a.themes.handleUser)
	mux.HandleFunc("GET /api/info", a.handleInfo)
	mux.HandleFunc("POST /api/join", a.handleJoin)
	mux.HandleFunc("POST /api/leave", a.handleLeave)
	mux.HandleFunc("GET /api/play", a.handlePlay)
	return homeNetworkOnly(mux)
}

func (a *app) startGuestServer() error {
	a.guestMu.Lock()
	defer a.guestMu.Unlock()
	if a.guestServer != nil {
		return nil
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(a.guestPort)))
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           a.guestHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	a.guestServer = server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("guest port: %v", err)
		}
	}()
	log.Printf("Party open on port %d", a.guestPort)
	return nil
}

func (a *app) stopGuestServer() {
	a.guestMu.Lock()
	defer a.guestMu.Unlock()
	if a.guestServer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.guestServer.Shutdown(ctx)
	a.guestServer = nil
	log.Printf("Party closed")
}

// handleInfo tells the join page whose PC this is and whether a party runs.
func (a *app) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pc": a.name, "party": a.party.active()})
}

func (a *app) handleLeave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	a.party.leave(body.Token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleJoin(w http.ResponseWriter, r *http.Request) {
	address := clientAddress(r)
	if a.guestGuard.blocked(address) || a.party.tooManyFailures() {
		writeError(w, http.StatusTooManyRequests, "Too many tries. Wait a minute.")
		return
	}
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name := cleanName(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Enter your name.")
		return
	}
	g, err := a.party.join(body.Code, name)
	switch {
	case errors.Is(err, errNoParty):
		writeError(w, http.StatusNotFound, "There’s no game right now. Ask the host to start one.")
	case errors.Is(err, errWrongCode):
		a.guestGuard.recordFailure(address)
		writeError(w, http.StatusUnauthorized, "That code didn’t work. Check it with the host.")
	case errors.Is(err, errFull):
		writeError(w, http.StatusConflict, "The game is full.")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "Couldn’t join.")
	default:
		a.guestGuard.clear(address)
		writeJSON(w, http.StatusOK, map[string]any{"token": g.token, "slot": g.slot, "name": g.name, "pc": a.name})
	}
}

// handlePlay is a guest's controller. The first message is the token from
// joining; an unknown token means the party ended or the guest was removed.
func (a *app) handlePlay(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxBodyBytes)

	authContext, cancelAuth := context.WithTimeout(context.Background(), keyboardAuthTimeout)
	_, token, err := conn.Read(authContext)
	cancelAuth()
	if err != nil {
		return
	}
	g, session, err := a.party.connect(string(token), conn, a.openGuestPad)
	var left *leftError
	if errors.As(err, &left) {
		conn.Close(statusPartyOver, left.reason)
		return
	}
	if errors.Is(err, errNoGuest) {
		conn.Close(statusPartyOver, "ended")
		return
	}
	if err != nil {
		log.Printf("opening guest gamepad: %v", err)
		conn.Close(websocket.StatusInternalError, "Could not create gamepad.")
		return
	}
	defer a.party.disconnect(g, session)
	playGamepad(session, conn, g.pad, a.gamepadTimeout, func(ms int) { a.party.setDelay(g, ms) })
}
