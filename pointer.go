package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// The zoom lens, run by Quickshell while Zoom is on. sharpen.frag.qsb is built
// from sharpen.frag with: /usr/lib/qt6/bin/qsb --qt6 -o sharpen.frag.qsb sharpen.frag
//
//go:embed lens
var lensFiles embed.FS

const (
	// How often the lens hears where the pointer is: about once a frame.
	lensFeedEvery = 16 * time.Millisecond
	// How long Quickshell gets to start and connect before the lens gives up.
	lensConnectTimeout = 10 * time.Second
	maxPointerBytes    = 1 << 10
)

// lensSizes is the lens diameter in pixels for each size the phone offers.
var lensSizes = map[string]int{"small": 200, "medium": 280, "large": 380}

// pointerSettings is ~/.config/pc-remote/pointer.json.
type pointerSettings struct {
	Lens string `json:"lens"` // small, medium or large
	Zoom int    `json:"zoom"` // how much the lens magnifies: 2 to 4
}

func defaultPointerSettings() pointerSettings {
	return pointerSettings{Lens: "medium", Zoom: 2}
}

func (s pointerSettings) valid() bool {
	_, knownLens := lensSizes[s.Lens]
	return s.Zoom >= 2 && s.Zoom <= 4 && knownLens
}

// pointerView is what the phone gets back: the settings, whether the lens is
// up, and whether this PC can run it.
type pointerView struct {
	pointerSettings
	Zooming bool `json:"zooming"`
	CanZoom bool `json:"canZoom"`
}

// lensLook is the ring and fill color of the lens, taken from the phone's theme.
type lensLook struct {
	Ring   string `json:"ring"`
	Ground string `json:"ground"`
}

func defaultLensLook() lensLook { return lensLook{Ring: "#C8F24A", Ground: "#0E0F0C"} }

// orDefault keeps each color only if it's a plain hex color, so nothing else
// reaches the lens.
func (l lensLook) orDefault() lensLook {
	look := defaultLensLook()
	if colorValuePattern.MatchString(l.Ring) {
		look.Ring = l.Ring
	}
	if colorValuePattern.MatchString(l.Ground) {
		look.Ground = l.Ground
	}
	return look
}

// lensRun is one running lens: the Quickshell process and the socket that feeds it.
type lensRun struct {
	stop func()        // ends the lens; safe to call more than once
	done chan struct{} // closed once the lens has ended
}

// pointer runs the zoom lens.
type pointer struct {
	path    string
	canZoom bool
	// hiddenMark exists while pc-remote has the cursor hidden, so a restart
	// after a crash knows to bring it back, and otherwise leaves it alone.
	hiddenMark string
	hyprctl    func(args ...string) error
	// startLens runs the lens; a field so tests can stand in for Quickshell.
	startLens func(s pointerSettings, look lensLook) (*lensRun, error)

	mu       sync.Mutex
	settings pointerSettings
	lens     *lensRun
	look     lensLook // colors of the running lens, kept for a restart
}

func onHyprland() bool {
	_, err := exec.LookPath("hyprctl")
	return err == nil && os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != ""
}

func newPointer(dir string) *pointer {
	p := &pointer{
		path:       filepath.Join(dir, "pointer.json"),
		hiddenMark: filepath.Join(runtimeDir(), "pc-remote-cursor-hidden"),
		hyprctl:    func(args ...string) error { _, err := runCommand(append([]string{"hyprctl"}, args...)...); return err },
		startLens:  startQuickshellLens,
	}
	// The lens files and its socket need a private runtime folder.
	if _, err := exec.LookPath("qs"); err == nil && runtimeDir() != "" {
		p.canZoom = onHyprland()
	}
	p.settings = p.load()
	return p
}

func (p *pointer) load() pointerSettings {
	data, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultPointerSettings()
	}
	settings := defaultPointerSettings()
	if err == nil {
		err = json.Unmarshal(data, &settings)
	}
	if err != nil || !settings.valid() {
		log.Printf("reading %s: not valid pointer settings", p.path)
		return defaultPointerSettings()
	}
	return settings
}

// save writes the settings; the rename keeps a half-written file from ever
// replacing a good one.
func (p *pointer) save(settings pointerSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	temp := p.path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, p.path)
}

func (p *pointer) view() pointerView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pointerView{pointerSettings: p.settings, Zooming: p.lens != nil, CanZoom: p.canZoom}
}

// restore undoes anything a crash could have left behind: a hidden cursor.
func (p *pointer) restore() {
	if _, err := os.Stat(p.hiddenMark); err == nil {
		p.hideCursor(false)
	}
}

// hideCursor uses the Lua config of newer Hyprland, then the older keyword.
func (p *pointer) hideCursor(hidden bool) {
	err := p.hyprctl("eval", fmt.Sprintf("hl.config({ cursor = { invisible = %t } })", hidden))
	if err != nil {
		err = p.hyprctl("keyword", "cursor:invisible", strconv.FormatBool(hidden))
	}
	if err != nil {
		log.Printf("hiding the cursor: %v", err)
		return
	}
	if hidden {
		err = os.WriteFile(p.hiddenMark, nil, 0o600)
	} else if err = os.Remove(p.hiddenMark); errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		log.Printf("marking the cursor hidden: %v", err)
	}
}

func (p *pointer) setSettings(settings pointerSettings) error {
	if err := p.save(settings); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.settings
	p.settings = settings
	// A running lens restarts to pick up a new size or zoom.
	if p.lens != nil && settings != old {
		running := p.lens
		p.lens = nil
		running.stop()
		if err := p.startLensLocked(); err != nil {
			log.Printf("restarting the zoom lens: %v", err)
			p.hideCursor(false)
		}
	}
	return nil
}

var errNoLens = errors.New("zoom needs Hyprland and Quickshell")

// setZoom starts or stops the lens.
func (p *pointer) setZoom(on bool, look lensLook) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !on {
		p.stopLensLocked()
		return nil
	}
	if p.lens != nil {
		return nil
	}
	if !p.canZoom {
		return errNoLens
	}
	p.look = look.orDefault()
	if err := p.startLensLocked(); err != nil {
		return err
	}
	p.hideCursor(true)
	return nil
}

// startLensLocked runs the lens and watches for it to end on its own.
func (p *pointer) startLensLocked() error {
	run, err := p.startLens(p.settings, p.look)
	if err != nil {
		return err
	}
	p.lens = run
	go func() {
		<-run.done
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.lens == run {
			p.lens = nil
			p.hideCursor(false)
		}
	}()
	return nil
}

func (p *pointer) stopLensLocked() {
	if p.lens == nil {
		return
	}
	p.lens.stop()
	p.lens = nil
	p.hideCursor(false)
}

// endSession runs when the phone's input connection ends: the lens closes.
func (p *pointer) endSession() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLensLocked()
}

// close puts the cursor back before pc-remote exits.
func (p *pointer) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lens == nil {
		return
	}
	run := p.lens
	p.stopLensLocked()
	select {
	case <-run.done:
	case <-time.After(time.Second):
	}
}

// runtimeDir is the user's private runtime folder. There's no fallback to /tmp,
// where another user could plant files for Quickshell to load.
func runtimeDir() string {
	return os.Getenv("XDG_RUNTIME_DIR")
}

// writeLensFiles copies the embedded lens to dir, for Quickshell to load.
func writeLensFiles(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fs.WalkDir(lensFiles, "lens", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := lensFiles.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(path)), data, 0o600)
	})
}

func startQuickshellLens(settings pointerSettings, look lensLook) (*lensRun, error) {
	dir := filepath.Join(runtimeDir(), "pc-remote-lens")
	if err := writeLensFiles(dir); err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "feed.sock")
	os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("qs", "-p", dir)
	cmd.Env = append(os.Environ(),
		"PC_REMOTE_LENS_SOCKET="+socket,
		"PC_REMOTE_LENS_SIZE="+strconv.Itoa(lensSizes[settings.Lens]),
		"PC_REMOTE_LENS_ZOOM="+strconv.Itoa(settings.Zoom),
		"PC_REMOTE_LENS_RING="+look.Ring,
		"PC_REMOTE_LENS_GROUND="+look.Ground,
	)
	if err := cmd.Start(); err != nil {
		listener.Close()
		return nil, err
	}

	run := &lensRun{done: make(chan struct{})}
	quit := make(chan struct{})
	var once sync.Once
	run.stop = func() {
		once.Do(func() {
			close(quit)
			listener.Close()
			cmd.Process.Signal(syscall.SIGTERM)
		})
	}
	go func() {
		feedLens(listener, quit, hyprSocket())
		run.stop() // the lens went away or never connected
	}()
	go func() {
		cmd.Wait()
		run.stop()
		os.Remove(socket)
		close(run.done)
	}()
	return run, nil
}

func hyprSocket() string {
	return filepath.Join(runtimeDir(), "hypr", os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"), ".socket.sock")
}

// lensSpot is where the pointer is and the window under it, as the lens wants it.
type lensSpot struct {
	X, Y          float64
	WinX, WinY    float64
	WinW, WinH    float64
	WindowAddress string
}

func (s lensSpot) line() string {
	address := s.WindowAddress
	if address == "" {
		address = "-"
	}
	return fmt.Sprintf("%g %g %g %g %g %g %s\n", s.X, s.Y, s.WinX, s.WinY, s.WinW, s.WinH, address)
}

// readSpot asks Hyprland for the pointer and the focused window in one round
// trip. Focus follows the mouse, so that's the window under the pointer.
func readSpot(socket string) (lensSpot, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return lensSpot{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(conn, "[[BATCH]]j/cursorpos;j/activewindow"); err != nil {
		return lensSpot{}, err
	}
	return parseSpot(io.LimitReader(conn, 1<<20))
}

func parseSpot(r io.Reader) (lensSpot, error) {
	var cursor struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	var window struct {
		At      [2]float64 `json:"at"`
		Size    [2]float64 `json:"size"`
		Address string     `json:"address"`
	}
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&cursor); err != nil {
		return lensSpot{}, err
	}
	// With no window focused, Hyprland answers "{}" or nothing at all.
	if err := decoder.Decode(&window); err != nil && !errors.Is(err, io.EOF) {
		return lensSpot{}, err
	}
	return lensSpot{
		X: cursor.X, Y: cursor.Y,
		WinX: window.At[0], WinY: window.At[1],
		WinW: window.Size[0], WinH: window.Size[1],
		WindowAddress: window.Address,
	}, nil
}

// feedLens waits for the lens to connect, then sends it every change of the
// pointer or the window under it until quit closes or the lens goes away.
func feedLens(listener net.Listener, quit <-chan struct{}, socket string) {
	if unix, ok := listener.(*net.UnixListener); ok {
		unix.SetDeadline(time.Now().Add(lensConnectTimeout))
	}
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	ticker := time.NewTicker(lensFeedEvery)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
		}
		spot, err := readSpot(socket)
		if err != nil {
			continue
		}
		line := spot.line()
		if line == last {
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(conn, line); err != nil {
			return
		}
		last = line
	}
}

func (a *app) handlePointerRead(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.pointer.view())
}

func (a *app) handlePointerSave(w http.ResponseWriter, r *http.Request) {
	settings := defaultPointerSettings()
	if !decodeBodyLimit(w, r, &settings, maxPointerBytes) {
		return
	}
	if !settings.valid() {
		writeError(w, http.StatusBadRequest, "Not valid zoom settings.")
		return
	}
	if err := a.pointer.setSettings(settings); err != nil {
		log.Printf("saving zoom settings: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not save the settings.")
		return
	}
	writeJSON(w, http.StatusOK, a.pointer.view())
}

func (a *app) handleZoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
		lensLook
	}
	if !decodeBodyLimit(w, r, &body, maxPointerBytes) {
		return
	}
	if body.On == nil {
		writeError(w, http.StatusBadRequest, "Say whether zoom is on.")
		return
	}
	if err := a.pointer.setZoom(*body.On, body.lensLook); err != nil {
		if errors.Is(err, errNoLens) {
			writeError(w, http.StatusConflict, "Zoom needs Hyprland and Quickshell on the PC.")
			return
		}
		log.Printf("starting the zoom lens: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not start the zoom lens.")
		return
	}
	writeJSON(w, http.StatusOK, a.pointer.view())
}
