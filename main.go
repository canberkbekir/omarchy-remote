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
	"math"
	"math/big"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sink             = "@DEFAULT_AUDIO_SINK@"
	passcodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	passcodeLength   = 10
	maxFailures      = 5
	lockoutDuration  = time.Minute
	maxBodyBytes     = 1024
	commandTimeout   = 10 * time.Second
)

//go:embed web
var webFiles embed.FS

// Power commands go through the helper so they reach the Hyprland session.
var powerCommands = map[string][]string{
	"lock":     {"pc-remote", "omarchy-system-lock"},
	"reboot":   {"pc-remote", "omarchy-system-reboot"},
	"shutdown": {"pc-remote", "omarchy-system-shutdown"},
}

var mediaCommands = map[string][]string{
	"previous": {"playerctl", "previous"},
	"toggle":   {"playerctl", "play-pause"},
	"next":     {"playerctl", "next"},
}

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
}

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
	trustProxy bool       // listening on loopback, behind `tailscale serve`
	ddc        sync.Mutex // Monitors handle one DDC/CI request at a time.

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

func readBrightness(name string) *int {
	output, err := runCommand("pc-remote", "omarchy-brightness-display", "--monitor", name)
	if err != nil {
		return nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return nil
	}
	return &value
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
	output, err := runCommand("wpctl", "get-volume", sink)
	if err != nil {
		return status{}, err
	}
	fields := strings.Fields(output)
	if len(fields) < 2 {
		return status{}, errors.New("unexpected wpctl output")
	}
	level, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return status{}, err
	}
	s := status{
		Volume: int(math.Round(level * 100)),
		Muted:  strings.Contains(output, "[MUTED]"),
	}
	if _, err := exec.LookPath("playerctl"); err == nil {
		s.Media = true
		s.NowPlaying, _ = readTrack()
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
	s, err := readStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read status.")
		return
	}
	s.Name = a.name
	s.Theme = a.themes.version()
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
	if _, err := runCommand("wpctl", "set-volume", sink, strconv.Itoa(*body.Volume)+"%"); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	if _, err := runCommand("wpctl", "set-mute", sink, "0"); err != nil {
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
	value := "0"
	if *body.Muted {
		value = "1"
	}
	if _, err := runCommand("wpctl", "set-mute", sink, value); err != nil {
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
	argv, ok := mediaCommands[body.Action]
	if !ok {
		writeError(w, http.StatusBadRequest, "Unknown action.")
		return
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		writeError(w, http.StatusNotImplemented, "playerctl is not installed.")
		return
	}
	if _, err := runCommand(argv...); err != nil {
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
		monitors[i].Brightness = readBrightness(monitors[i].Name)
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
	target := strconv.Itoa(*body.Brightness) + "%"
	if _, err := runCommand("pc-remote", "omarchy-brightness-display", "--no-osd", "--monitor", body.Monitor, target); err != nil {
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
	argv, ok := powerCommands[body.Action]
	if !ok {
		writeError(w, http.StatusBadRequest, "Unknown action.")
		return
	}
	// Don't wait for it: rebooting can end this process mid-request.
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	go cmd.Wait()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func main() {
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
		trustProxy:      loopback,
		keyboardTimeout: keyboardTimeout,
		openKeyboard:    openVirtualKeyboard,
		gamepadTimeout:  gamepadTimeout,
		openGamepad:     openVirtualGamepad,
		party:           newParty(),
		guestPort:       settings.GuestPort,
		guestGuard:      &loginGuard{failures: map[string]attempt{}},
		openGuestPad:    openVirtualGamepad,
	}

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
	mux.HandleFunc("POST /api/party", a.auth(a.handlePartyChange))
	mux.HandleFunc("GET /api/art", a.auth(handleArt))
	mux.HandleFunc("GET /api/outputs", a.auth(handleOutputs))
	mux.HandleFunc("POST /api/output", a.auth(handleOutput))
	mux.HandleFunc("POST /api/screens", a.auth(handleScreens))
	mux.HandleFunc("POST /api/type", a.auth(handleType))
	mux.HandleFunc("GET /api/clipboard", a.auth(handleClipboardRead))
	mux.HandleFunc("POST /api/clipboard", a.auth(handleClipboardWrite))
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
	log.Printf("Listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
