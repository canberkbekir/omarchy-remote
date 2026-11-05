// Small web app for controlling an Omarchy PC from a phone.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math/big"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	passcodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	passcodeLength   = 10
	maxFailures      = 5
	lockoutDuration  = time.Minute
	maxBodyBytes     = 1024
	commandTimeout   = 10 * time.Second
)

//go:embed web
var webFiles embed.FS

// errUnknownAction means the phone asked for a power action this PC doesn't have.
var errUnknownAction = errors.New("unknown action")

// startDetached starts a command without waiting for it, because rebooting
// can end this process mid-request. Tests swap it for a recorder.
var startDetached = func(argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

var mediaActions = map[string]bool{"previous": true, "toggle": true, "next": true}

type status struct {
	Name       string `json:"name,omitempty"`
	Theme      string `json:"theme,omitempty"`
	Volume     int    `json:"volume"`
	Muted      bool   `json:"muted"`
	Media      bool   `json:"media"`
	Playing    bool   `json:"playing"`
	Nightlight bool   `json:"nightlight"`
	NowPlaying *track `json:"nowPlaying"`
	Output     string `json:"output"`
	Zooming    bool   `json:"zooming"` // the zoom lens is up
	// Features says which controls this PC supports, so the phone hides the rest.
	Features featureSet `json:"features"`
}

// featureSet lists the controls that work on this OS. Each OS fills it in platformFeatures().
type featureSet struct {
	Power      bool `json:"power"`
	Screens    bool `json:"screens"`
	Nightlight bool `json:"nightlight"`
	Brightness bool `json:"brightness"`
	Outputs    bool `json:"outputs"`
	NowPlaying bool `json:"nowPlaying"`
	Gamepad    bool `json:"gamepad"`
	Zoom       bool `json:"zoom"`
	Party      bool `json:"party"`
}

// errNoSpeakers means the PC has no sound output at all, such as a server or
// a CI machine. The status still works then; it shows volume 0.
var errNoSpeakers = errors.New("no sound output")

type monitor struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Brightness *int   `json:"brightness"`
	On         bool   `json:"on"`
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
	trustProxy bool            // behind `tailscale serve`, so loopback connections are the proxy
	ddc        sync.Mutex      // Monitors handle one DDC/CI request at a time.
	answered   map[string]bool // monitors that have reported a brightness; guarded by ddc

	keyboardTimeout    time.Duration
	openKeyboard       func() (*keyboard, error)
	keyboardMu         sync.Mutex
	keyboard           *keyboard
	endKeyboardSession context.CancelFunc

	gamepadTimeout    time.Duration
	openGamepad       func() (*gamepad, error)
	gamepadMu         sync.Mutex
	gamepad           *gamepad
	endGamepadSession context.CancelFunc

	party        *party
	guestPort    int // 0 turns parties off
	guestGuard   *loginGuard
	openGuestPad func() (*gamepad, error)
	guestMu      sync.Mutex
	guestServer  *http.Server // runs only while a party does

	phoneSeen atomic.Int64 // when a phone last polled the status, in Unix nanoseconds

	pointer *pointer // the zoom lens
}

func loadPasscode(dir string) (string, error) {
	path := filepath.Join(dir, "passcode")
	data, err := os.ReadFile(path)
	if err == nil {
		passcode := strings.TrimSpace(string(data))
		if passcode == "" {
			return "", errors.New(path + " is empty")
		}
		return passcode, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	chars := make([]byte, passcodeLength)
	for i := range chars {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passcodeAlphabet))))
		if err != nil {
			return "", err
		}
		chars[i] = passcodeAlphabet[n.Int64()]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(chars, '\n')); err != nil {
		return "", err
	}
	return string(chars), nil
}

func runCommand(argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	return string(output), err
}

func listMonitors() ([]monitor, error) {
	output, err := runCommand("pc-remote", "hyprctl", "monitors", "-j")
	if err != nil {
		return nil, err
	}
	var found []struct {
		Name       string `json:"name"`
		Model      string `json:"model"`
		DpmsStatus bool   `json:"dpmsStatus"`
	}
	if err := json.Unmarshal([]byte(output), &found); err != nil {
		return nil, err
	}
	monitors := make([]monitor, len(found))
	for i, m := range found {
		monitors[i] = monitor{Name: m.Name, Label: m.Model, On: m.DpmsStatus}
	}
	return monitors, nil
}

// Some monitors drop DDC/CI requests now and then, so brightness reads and
// writes are tried a few times, and a write counts only once it reads back.
// Only monitors that have answered before are retried: one that never has,
// like a TV without DDC/CI, gets a single try, so it stays quick to skip.
var (
	brightnessCommand  = runCommand
	brightnessAttempts = 3
	brightnessPause    = 200 * time.Millisecond
)

// retryBrightness waits before another attempt. One dropped request makes
// Omarchy's DDC helper mark the monitor unavailable for a minute, so its
// cache entry is cleared first; the helper finds the monitor again.
func retryBrightness(name string) {
	time.Sleep(brightnessPause)
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		cacheName := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r) {
				return r
			}
			return '_'
		}, name)
		os.Remove(filepath.Join(dir, "omarchy-brightness-display-ddc", cacheName+".bus"))
	}
}

// attempts says how many tries a monitor gets. Callers hold a.ddc.
func (a *app) attempts(name string) int {
	if a.answered[name] {
		return brightnessAttempts
	}
	return 1
}

// readBrightnessOnce asks the monitor once. Callers hold a.ddc.
func (a *app) readBrightnessOnce(name string) *int {
	output, err := brightnessCommand("pc-remote", "omarchy-brightness-display", "--monitor", name)
	if err != nil {
		return nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil || value < 0 || value > 100 {
		return nil
	}
	if a.answered == nil {
		a.answered = map[string]bool{}
	}
	a.answered[name] = true
	return &value
}

// readBrightness returns nil when the monitor doesn't answer. Callers hold a.ddc.
func (a *app) readBrightness(name string) *int {
	for attempt := range a.attempts(name) {
		if attempt > 0 {
			retryBrightness(name)
		}
		if value := a.readBrightnessOnce(name); value != nil {
			return value
		}
	}
	// Gone quiet, perhaps unplugged or swapped: one try until it answers again.
	delete(a.answered, name)
	return nil
}

// setBrightness reports whether the monitor took the brightness. Callers hold a.ddc.
func (a *app) setBrightness(name string, target int) bool {
	var previous *int
	for attempt := range a.attempts(name) {
		if attempt > 0 {
			retryBrightness(name)
		}
		if _, err := brightnessCommand("pc-remote", "omarchy-brightness-display", "--no-osd", "--monitor", name, strconv.Itoa(target)+"%"); err != nil {
			previous = nil
			continue
		}
		got := a.readBrightnessOnce(name)
		if got == nil {
			previous = nil
			continue
		}
		if max(*got-target, target-*got) <= 1 {
			return true
		}
		// A display with few brightness steps lands on the nearest one; two
		// writes in a row landing on the same value mean that's as close as it goes.
		if previous != nil && *previous == *got {
			return true
		}
		previous = got
	}
	return false
}

func nightlightEnabled() (bool, error) {
	output, err := runCommand("pc-remote", "omarchy-toggle-nightlight", "--status")
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
	volume, muted, err := readVolume()
	if err != nil && !errors.Is(err, errNoSpeakers) {
		return status{}, err
	}
	s := status{Volume: volume, Muted: muted, Features: platformFeatures()}
	if mediaAvailable() {
		s.Media = true
		s.NowPlaying = nowPlaying()
		s.Playing = s.NowPlaying != nil && s.NowPlaying.Playing
	}
	if outputs, err := listOutputs(); err == nil {
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
	if err := setVolume(*body.Volume); err != nil {
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
	if err := setMuted(*body.Muted); err != nil {
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
	if !mediaActions[body.Action] {
		writeError(w, http.StatusBadRequest, "Unknown action.")
		return
	}
	if !mediaAvailable() {
		writeError(w, http.StatusNotImplemented, mediaMissing)
		return
	}
	if err := mediaAction(body.Action); err != nil {
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
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
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
	monitors, err := listMonitors()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read monitors.")
		return
	}
	for i := range monitors {
		monitors[i].Brightness = a.readBrightness(monitors[i].Name)
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
	monitors, err := listMonitors()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read monitors.")
		return
	}
	known := slices.ContainsFunc(monitors, func(m monitor) bool { return m.Name == body.Monitor })
	if !known {
		writeError(w, http.StatusBadRequest, "Unknown monitor.")
		return
	}
	if !a.setBrightness(body.Monitor, *body.Brightness) {
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
	switch err := powerAction(body.Action); {
	case errors.Is(err, errUnknownAction):
		writeError(w, http.StatusBadRequest, "Unknown action.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func main() {
	if code, handled := runCommandLine(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}
	dir, err := configDir()
	if err != nil {
		log.Fatal(err)
	}
	settings, err := loadConfig(dir)
	if err != nil {
		log.Fatal(err)
	}
	look, err := newThemes(dir, settings.Theme)
	if err != nil {
		log.Fatal(err)
	}
	passcode, err := loadPasscode(dir)
	if err != nil {
		log.Fatal(err)
	}
	site, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	addr := settings.listenAddr()
	loopback, everywhere := listenScope(addr)
	a := &app{
		dir:             dir,
		name:            settings.Name,
		themes:          look,
		passcode:        []byte(passcode),
		guard:           &loginGuard{failures: map[string]attempt{}},
		trustProxy:      loopback || settings.Listen == "" && viaTailscale(settings.Access),
		keyboardTimeout: keyboardTimeout,
		openKeyboard:    openVirtualKeyboard,
		gamepadTimeout:  gamepadTimeout,
		openGamepad:     openVirtualGamepad,
		party:           newParty(),
		guestPort:       settings.GuestPort,
		guestGuard:      &loginGuard{failures: map[string]attempt{}},
		openGuestPad:    openVirtualGamepad,
		pointer:         newPointer(dir),
	}
	a.pointer.restore()

	syncBarPlugin()

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
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
		<-stop
		a.pointer.close()
		os.Exit(0)
	}()
	log.Printf("Listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
