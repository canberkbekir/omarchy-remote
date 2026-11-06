// Package server is the web server the phone talks to, and the desktop side
// of it: the tray icon or bar widget and the zoom lens.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/cli"
	"github.com/canberkbekir/omarchy-remote/internal/config"
	"github.com/canberkbekir/omarchy-remote/internal/input"
	"github.com/canberkbekir/omarchy-remote/internal/run"
	"github.com/canberkbekir/omarchy-remote/internal/system"
	"github.com/canberkbekir/omarchy-remote/web"
)

const (
	maxFailures     = 5
	lockoutDuration = time.Minute
	maxBodyBytes    = 1024
)

type status struct {
	Name       string        `json:"name,omitempty"`
	Theme      string        `json:"theme,omitempty"`
	Volume     int           `json:"volume"`
	Muted      bool          `json:"muted"`
	Media      bool          `json:"media"`
	Playing    bool          `json:"playing"`
	Nightlight bool          `json:"nightlight"`
	NowPlaying *system.Track `json:"nowPlaying"`
	Output     string        `json:"output"`
	Zooming    bool          `json:"zooming"` // the zoom lens is up
	// Features says which controls this PC supports, so the phone hides the rest.
	Features system.FeatureSet `json:"features"`
}

type attempt struct {
	count int
	last  time.Time
}

// loginGuard blocks an address for a minute after repeated wrong passcodes.
type loginGuard struct {
	mu       sync.Mutex
	failures map[string]attempt
}

// pruneLocked forgets addresses quiet for the lockout time.
func (g *loginGuard) pruneLocked() {
	for address, a := range g.failures {
		if time.Since(a.last) >= lockoutDuration {
			delete(g.failures, address)
		}
	}
}

func (g *loginGuard) blocked(address string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	return g.failures[address].count >= maxFailures
}

func (g *loginGuard) recordFailure(address string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	a := g.failures[address]
	g.failures[address] = attempt{a.count + 1, time.Now()}
}

// reserve counts a try before it is checked, so tries sent together can't all
// slip under the limit. It reports false when the address is locked out.
func (g *loginGuard) reserve(address string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	a := g.failures[address]
	if a.count >= maxFailures {
		return false
	}
	g.failures[address] = attempt{a.count + 1, time.Now()}
	return true
}

// refund gives back a reserved try that never got to guess.
func (g *loginGuard) refund(address string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	a, ok := g.failures[address]
	if !ok {
		return
	}
	if a.count <= 1 {
		delete(g.failures, address)
		return
	}
	g.failures[address] = attempt{a.count - 1, a.last}
}

func (g *loginGuard) clear(address string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, address)
}

type app struct {
	dir        string // ~/.config/pc-remote
	name       string
	themes     *themes
	passcode   []byte
	guard      *loginGuard
	trustProxy bool              // behind `tailscale serve`, so loopback connections are the proxy
	ddc        sync.Mutex        // Monitors handle one DDC/CI request at a time.
	brightness system.Brightness // retries for monitors; guarded by ddc

	keyboardTimeout    time.Duration
	openKeyboard       func() (*input.Keyboard, error)
	keyboardMu         sync.Mutex
	keyboard           *input.Keyboard
	endKeyboardSession context.CancelFunc

	gamepadTimeout    time.Duration
	openGamepad       func() (*input.Gamepad, error)
	gamepadMu         sync.Mutex
	gamepad           *input.Gamepad
	endGamepadSession context.CancelFunc

	party        *party
	guestPort    int // 0 turns parties off
	guestGuard   *loginGuard
	openGuestPad func() (*input.Gamepad, error)
	guestMu      sync.Mutex
	guestServer  *http.Server // runs only while a party does

	phoneSeen atomic.Int64 // when a phone last polled the status, in Unix nanoseconds

	pointer *pointer // the zoom lens

	tickets        *connectTickets // the tray's one-time links to the passcode page
	connectAddress func() cli.Address
}

func nightlightEnabled() (bool, error) {
	output, err := run.Command("pc-remote", "omarchy-toggle-nightlight", "--status")
	if err != nil {
		return false, err
	}
	var state struct {
		Enabled bool `json:"enabled"`
	}
	err = json.Unmarshal([]byte(output), &state)
	return state.Enabled, err
}

func readStatus() (status, error) {
	volume, muted, err := system.Volume()
	if err != nil && !errors.Is(err, system.ErrNoSpeakers) {
		return status{}, err
	}
	s := status{Volume: volume, Muted: muted, Features: system.Features()}
	if system.MediaAvailable() {
		s.Media = true
		s.NowPlaying = system.NowPlaying()
		s.Playing = s.NowPlaying != nil && s.NowPlaying.Playing
	}
	if outputs, err := system.Outputs(); err == nil {
		for _, o := range outputs {
			if o.Active {
				s.Output = o.Label
			}
		}
	}
	s.Nightlight, _ = nightlightEnabled()
	return s, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// Scripts only from this server; https styles, fonts and images are for user.css.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; connect-src 'self' ws: wss:; " +
	"style-src 'self' https:; font-src 'self' https: data:; img-src 'self' blob: data: https:; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeStatus(w http.ResponseWriter) {
	s, err := readStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read status.")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeBodyLimit(w, r, target, maxBodyBytes)
}

func decodeBodyLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "Bad request.")
		return false
	}
	return true
}

func (a *app) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := clientAddress(r, a.trustProxy)
		if a.guard.blocked(address) {
			writeError(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute.")
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(supplied), a.passcode) != 1 {
			a.guard.recordFailure(address)
			writeError(w, http.StatusUnauthorized, "Wrong passcode.")
			return
		}
		a.guard.clear(address)
		next(w, r)
	}
}

// The status poll also carries the title and the theme version, so both update live.
func (a *app) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.phoneSeen.Store(time.Now().UnixNano())
	s, err := readStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read status.")
		return
	}
	s.Name = a.name
	s.Theme = a.themes.version()
	s.Zooming = a.pointer.view().Zooming
	writeJSON(w, http.StatusOK, s)
}

func handleVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Volume *int `json:"volume"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Volume == nil || *body.Volume < 0 || *body.Volume > 100 {
		writeError(w, http.StatusBadRequest, "Volume must be 0 to 100.")
		return
	}
	if err := system.SetVolume(*body.Volume); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	writeStatus(w)
}

func handleMute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Muted *bool `json:"muted"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Muted == nil {
		writeError(w, http.StatusBadRequest, "Muted must be true or false.")
		return
	}
	if err := system.SetMuted(*body.Muted); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	writeStatus(w)
}

func handleMedia(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !system.MediaActions[body.Action] {
		writeError(w, http.StatusBadRequest, "Unknown action.")
		return
	}
	if !system.MediaAvailable() {
		writeError(w, http.StatusNotImplemented, system.MediaMissing)
		return
	}
	if err := system.Media(body.Action); err != nil {
		writeError(w, http.StatusInternalServerError, "No media player found.")
		return
	}
	writeStatus(w)
}

func handleNightlight(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "Enabled must be true or false.")
		return
	}
	current, err := nightlightEnabled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read night light.")
		return
	}
	if current != *body.Enabled {
		// Run without output pipes: the script can leave hyprsunset running in
		// the background, which would hold them open until the timeout.
		ctx, cancel := context.WithTimeout(context.Background(), run.Timeout)
		defer cancel()
		if err := exec.CommandContext(ctx, "pc-remote", "omarchy-toggle-nightlight").Run(); err != nil {
			writeError(w, http.StatusInternalServerError, "Command failed.")
			return
		}
	}
	writeStatus(w)
}

func (a *app) handleBrightnessRead(w http.ResponseWriter, r *http.Request) {
	a.ddc.Lock()
	defer a.ddc.Unlock()
	monitors, err := system.Monitors()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read monitors.")
		return
	}
	for i := range monitors {
		monitors[i].Brightness = a.brightness.Read(monitors[i].Name)
	}
	writeJSON(w, http.StatusOK, monitors)
}

func (a *app) handleBrightnessSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Monitor    string `json:"monitor"`
		Brightness *int   `json:"brightness"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Brightness == nil || *body.Brightness < 1 || *body.Brightness > 100 {
		writeError(w, http.StatusBadRequest, "Brightness must be 1 to 100.")
		return
	}
	a.ddc.Lock()
	defer a.ddc.Unlock()
	monitors, err := system.Monitors()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read monitors.")
		return
	}
	known := slices.ContainsFunc(monitors, func(m system.Monitor) bool { return m.Name == body.Monitor })
	if !known {
		writeError(w, http.StatusBadRequest, "Unknown monitor.")
		return
	}
	if !a.brightness.Set(body.Monitor, *body.Brightness) {
		writeError(w, http.StatusInternalServerError, "The monitor didn’t respond.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handlePower(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch err := system.Power(body.Action); {
	case errors.Is(err, system.ErrUnknownAction):
		writeError(w, http.StatusBadRequest, "Unknown action.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Run runs the server until the process is stopped.
func Run() {
	if exe, err := cli.Executable(); err == nil {
		cli.RemoveOldFiles(exe)
	}
	dir, err := config.Dir()
	if err != nil {
		log.Fatal(err)
	}
	settings, err := config.Load(dir)
	if err != nil {
		log.Fatal(err)
	}
	look, err := newThemes(dir, settings.Theme)
	if err != nil {
		log.Fatal(err)
	}
	passcode, err := config.LoadPasscode(dir)
	if err != nil {
		log.Fatal(err)
	}
	var site fs.FS = web.Files
	addr := settings.ListenAddr()
	loopback, everywhere := config.ListenScope(addr)
	a := &app{
		dir:             dir,
		name:            settings.Name,
		themes:          look,
		passcode:        []byte(passcode),
		guard:           &loginGuard{failures: map[string]attempt{}},
		trustProxy:      loopback || settings.Listen == "" && config.ViaTailscale(settings.Access),
		keyboardTimeout: keyboardTimeout,
		openKeyboard:    input.OpenKeyboard,
		gamepadTimeout:  gamepadTimeout,
		openGamepad:     input.OpenGamepad,
		party:           newParty(),
		guestPort:       settings.GuestPort,
		guestGuard:      &loginGuard{failures: map[string]attempt{}},
		openGuestPad:    input.OpenGamepad,
		pointer:         newPointer(dir),
		tickets:         newConnectTickets(),
		connectAddress:  func() cli.Address { return cli.ConnectAddress(dir, settings) },
	}
	a.openGuestPad = guestPadOpener(a)
	a.pointer.restore()

	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		log.Fatal(err)
	}
	static := http.FileServerFS(site)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.auth(a.handleStatus))
	mux.HandleFunc("POST /api/volume", a.auth(handleVolume))
	mux.HandleFunc("POST /api/mute", a.auth(handleMute))
	mux.HandleFunc("POST /api/media", a.auth(handleMedia))
	mux.HandleFunc("POST /api/nightlight", a.auth(handleNightlight))
	mux.HandleFunc("GET /api/brightness", a.auth(a.handleBrightnessRead))
	mux.HandleFunc("POST /api/brightness", a.auth(a.handleBrightnessSet))
	mux.HandleFunc("POST /api/power", a.auth(handlePower))
	mux.HandleFunc("GET /api/keyboard", a.handleKeyboard)
	mux.HandleFunc("GET /api/gamepad", a.handleGamepad)
	mux.HandleFunc("GET /api/gamepad/layout", a.auth(a.handleLayoutRead))
	mux.HandleFunc("POST /api/gamepad/layout", a.auth(a.handleLayoutSave))
	mux.HandleFunc("GET /api/party", a.auth(a.handlePartyRead))
	mux.HandleFunc("GET /api/presence", a.auth(a.handlePresence))
	mux.HandleFunc("POST /api/connect", a.handleConnect)
	mux.HandleFunc("POST /api/party", a.auth(a.handlePartyChange))
	mux.HandleFunc("GET /api/art", a.auth(handleArt))
	mux.HandleFunc("GET /api/outputs", a.auth(handleOutputs))
	mux.HandleFunc("POST /api/output", a.auth(handleOutput))
	mux.HandleFunc("POST /api/screens", a.auth(handleScreens))
	mux.HandleFunc("POST /api/type", a.auth(handleType))
	mux.HandleFunc("GET /api/clipboard", a.auth(handleClipboardRead))
	mux.HandleFunc("POST /api/clipboard", a.auth(handleClipboardWrite))
	mux.HandleFunc("GET /api/pointer", a.auth(a.handlePointerRead))
	mux.HandleFunc("POST /api/pointer", a.auth(a.handlePointerSave))
	mux.HandleFunc("POST /api/pointer/zoom", a.auth(a.handleZoom))
	mux.HandleFunc("GET /theme.css", look.handleTheme)
	mux.HandleFunc("GET /user.css", look.handleUser)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})

	var handler http.Handler = mux
	if everywhere {
		handler = homeNetworkOnly(handler, true)
	}
	if !loopback {
		log.Print("Serving plain HTTP: devices on the network can see the passcode. See Security in the README for Tailscale.")
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	// A stop or restart closes the zoom lens and brings the cursor back.
	watchConsoleClose(a.pointer.close)
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
		<-stop
		closeDesktopIntegration()
		a.pointer.close()
		os.Exit(0)
	}()
	// Listen before showing the tray icon, so a second copy that can't get
	// the port exits without leaving an icon behind.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	desktopIntegration(a, addr)
	log.Printf("Listening on %s", addr)
	log.Fatal(server.Serve(listener))
}
