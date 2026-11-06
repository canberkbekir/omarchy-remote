package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxPointerBytes = 1 << 10

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
	path   string
	driver lensDriver // the desktop's own lens

	mu       sync.Mutex
	settings pointerSettings
	lens     *lensRun
	look     lensLook // colors of the running lens, kept for a restart
}

func newPointer(dir string) *pointer {
	p := &pointer{path: filepath.Join(dir, "pointer.json"), driver: newLensDriver(dir)}
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
	return pointerView{pointerSettings: p.settings, Zooming: p.lens != nil, CanZoom: p.driver.available()}
}

// restore undoes anything a crash could have left behind.
func (p *pointer) restore() {
	p.driver.restore()
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
			p.driver.hideCursor(false)
		}
	}
	return nil
}

var errNoLens = errors.New(noLensReason)

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
	if !p.driver.available() {
		return errNoLens
	}
	p.look = look.orDefault()
	if err := p.startLensLocked(); err != nil {
		return err
	}
	p.driver.hideCursor(true)
	return nil
}

// startLensLocked runs the lens and watches for it to end on its own.
func (p *pointer) startLensLocked() error {
	run, err := p.driver.start(p.settings, p.look)
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
			p.driver.hideCursor(false)
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
	p.driver.hideCursor(false)
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
			writeError(w, http.StatusConflict, noLensMessage)
			return
		}
		log.Printf("starting the zoom lens: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not start the zoom lens.")
		return
	}
	writeJSON(w, http.StatusOK, a.pointer.view())
}
