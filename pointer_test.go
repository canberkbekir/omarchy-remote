package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDriver stands in for the desktop's lens and keeps every call.
type fakeDriver struct {
	mu       sync.Mutex
	cannot   bool
	runs     []*lensRun
	looks    []lensLook
	settings []pointerSettings
	calls    []string // "hide", "show" and "restore", in order
}

func (f *fakeDriver) available() bool { return !f.cannot }

func (f *fakeDriver) start(s pointerSettings, look lensLook) (*lensRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.looks = append(f.looks, look)
	f.settings = append(f.settings, s)
	run := &lensRun{done: make(chan struct{})}
	var once sync.Once
	run.stop = func() { once.Do(func() { close(run.done) }) }
	f.runs = append(f.runs, run)
	return run, nil
}

func (f *fakeDriver) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeDriver) hideCursor(hidden bool) {
	if hidden {
		f.record("hide")
	} else {
		f.record("show")
	}
}

func (f *fakeDriver) restore() { f.record("restore") }

func (f *fakeDriver) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeDriver) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(f.list(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("lens calls = %q, want %q among them", f.list(), want)
}

func (f *fakeDriver) last() *lensRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[len(f.runs)-1]
}

func testPointer(t *testing.T, driver *fakeDriver) *pointer {
	t.Helper()
	if driver == nil {
		driver = &fakeDriver{}
	}
	return &pointer{
		path:     filepath.Join(t.TempDir(), "pointer.json"),
		driver:   driver,
		settings: defaultPointerSettings(),
	}
}

func TestPointerSettingsValid(t *testing.T) {
	for _, test := range []struct {
		settings pointerSettings
		valid    bool
	}{
		{defaultPointerSettings(), true},
		{pointerSettings{Lens: "large", Zoom: 4}, true},
		{pointerSettings{Lens: "huge", Zoom: 2}, false},
		{pointerSettings{Lens: "small", Zoom: 1}, false},
		{pointerSettings{Lens: "small", Zoom: 5}, false},
	} {
		if got := test.settings.valid(); got != test.valid {
			t.Errorf("%+v valid = %v, want %v", test.settings, got, test.valid)
		}
	}
}

func TestPointerSettingsSaveAndLoad(t *testing.T) {
	p := testPointer(t, nil)
	if got := p.load(); got != defaultPointerSettings() {
		t.Fatalf("with no file, load = %+v, want the defaults", got)
	}
	want := pointerSettings{Lens: "large", Zoom: 4}
	if err := p.setSettings(want); err != nil {
		t.Fatal(err)
	}
	if got := p.load(); got != want {
		t.Fatalf("load = %+v, want %+v", got, want)
	}
	if err := os.WriteFile(p.path, []byte(`{"zoom": 9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.load(); got != defaultPointerSettings() {
		t.Fatalf("with a bad file, load = %+v, want the defaults", got)
	}
}

func TestZoomHidesTheCursorUntilItEnds(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)

	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	if len(lens.runs) != 1 {
		t.Fatalf("lens started %d times, want once", len(lens.runs))
	}
	lens.waitFor(t, "hide")
	if !p.view().Zooming {
		t.Fatal("view says not zooming")
	}

	if err := p.setZoom(false, lensLook{}); err != nil {
		t.Fatal(err)
	}
	lens.waitFor(t, "show")
	select {
	case <-lens.last().done:
	default:
		t.Fatal("the lens is still running")
	}
	if p.view().Zooming {
		t.Fatal("view says zooming")
	}
}

func TestLensEndingOnItsOwnBringsTheCursorBack(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	lens.last().stop() // Quickshell quit or crashed
	lens.waitFor(t, "show")
	deadline := time.Now().Add(2 * time.Second)
	for p.view().Zooming && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.view().Zooming {
		t.Fatal("view still says zooming")
	}
}

func TestNewLensSettingsRestartTheLens(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	first := lens.last()
	if err := p.setSettings(pointerSettings{Lens: "large", Zoom: 3}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	default:
		t.Fatal("the old lens is still running")
	}
	if len(lens.runs) != 2 || !p.view().Zooming {
		t.Fatalf("lens started %d times, zooming %v; want a second lens running", len(lens.runs), p.view().Zooming)
	}
	time.Sleep(50 * time.Millisecond)
	if slices.Contains(lens.list(), "show") {
		t.Fatal("the cursor came back while the lens restarted")
	}
}

func TestLensTakesThePhoneThemeColors(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)
	if err := p.setZoom(true, lensLook{Ring: "#7aa2f7", Ground: "red; }"}); err != nil {
		t.Fatal(err)
	}
	want := lensLook{Ring: "#7aa2f7", Ground: defaultLensLook().Ground}
	if lens.looks[0] != want {
		t.Fatalf("look = %+v, want %+v", lens.looks[0], want)
	}
	if err := p.setSettings(pointerSettings{Lens: "small", Zoom: 2}); err != nil {
		t.Fatal(err)
	}
	if lens.looks[1] != want {
		t.Fatalf("after a restart, look = %+v, want %+v", lens.looks[1], want)
	}
}

func TestEndSessionStopsZoom(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	p.endSession()
	select {
	case <-lens.last().done:
	case <-time.After(time.Second):
		t.Fatal("the lens is still running")
	}
}

func TestZoomNeedsALens(t *testing.T) {
	p := testPointer(t, &fakeDriver{cannot: true})
	if err := p.setZoom(true, lensLook{}); !errors.Is(err, errNoLens) {
		t.Fatalf("setZoom = %v, want errNoLens", err)
	}
	a := &app{pointer: p}
	w := httptest.NewRecorder()
	a.handleZoom(w, httptest.NewRequest("POST", "/api/pointer/zoom", strings.NewReader(`{"on": true}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestPointerSaveRejectsBadSettings(t *testing.T) {
	a := &app{pointer: testPointer(t, nil)}
	w := httptest.NewRecorder()
	a.handlePointerSave(w, httptest.NewRequest("POST", "/api/pointer", strings.NewReader(`{"lens": "giant", "zoom": 2}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestLensGetsTheSavedSettings(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)
	if err := p.setSettings(pointerSettings{Lens: "small", Zoom: 4}); err != nil {
		t.Fatal(err)
	}
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	if want := (pointerSettings{Lens: "small", Zoom: 4}); lens.settings[0] != want {
		t.Fatalf("lens settings = %+v, want %+v", lens.settings[0], want)
	}
}

func TestRestoreAsksTheLens(t *testing.T) {
	lens := &fakeDriver{}
	testPointer(t, lens).restore()
	if got := lens.list(); !slices.Equal(got, []string{"restore"}) {
		t.Fatalf("lens calls = %q, want [restore]", got)
	}
}

// close runs on every way out: a signal, the console closing, tray Quit.
func TestCloseEndsZoomBeforeExit(t *testing.T) {
	lens := &fakeDriver{}
	p := testPointer(t, lens)
	p.close() // nothing running: nothing to do
	if got := lens.list(); len(got) != 0 {
		t.Fatalf("close with no lens called %q", got)
	}
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	p.close()
	select {
	case <-lens.last().done:
	default:
		t.Fatal("close returned with the lens still running")
	}
	lens.waitFor(t, "show")
	p.close() // a second way out finds nothing left
	if p.view().Zooming {
		t.Fatal("view says zooming after close")
	}
}
