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

// hyprLog stands in for hyprctl and keeps every call.
type hyprLog struct {
	mu    sync.Mutex
	calls []string
	fail  func(args []string) bool
}

func (h *hyprLog) run(args ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, strings.Join(args, " "))
	if h.fail != nil && h.fail(args) {
		return errors.New("hyprctl failed")
	}
	return nil
}

func (h *hyprLog) list() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

func (h *hyprLog) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(h.list(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hyprctl calls = %q, want %q among them", h.list(), want)
}

const (
	hideCall = "eval hl.config({ cursor = { invisible = true } })"
	showCall = "eval hl.config({ cursor = { invisible = false } })"
)

// fakeLens counts lens starts; each run ends when stopped.
type fakeLens struct {
	mu    sync.Mutex
	runs  []*lensRun
	looks []lensLook
}

func (f *fakeLens) start(_ pointerSettings, look lensLook) (*lensRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.looks = append(f.looks, look)
	run := &lensRun{done: make(chan struct{})}
	var once sync.Once
	run.stop = func() { once.Do(func() { close(run.done) }) }
	f.runs = append(f.runs, run)
	return run, nil
}

func (f *fakeLens) last() *lensRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[len(f.runs)-1]
}

func testPointer(t *testing.T, hypr *hyprLog) *pointer {
	t.Helper()
	if hypr == nil {
		hypr = &hyprLog{}
	}
	dir := t.TempDir()
	p := &pointer{
		path:       filepath.Join(dir, "pointer.json"),
		hiddenMark: filepath.Join(dir, "cursor-hidden"),
		canZoom:    true,
		hyprctl:    hypr.run,
		startLens:  (&fakeLens{}).start,
		settings:   defaultPointerSettings(),
	}
	return p
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
	hypr := &hyprLog{}
	p := testPointer(t, hypr)
	lens := &fakeLens{}
	p.startLens = lens.start

	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	if len(lens.runs) != 1 {
		t.Fatalf("lens started %d times, want once", len(lens.runs))
	}
	hypr.waitFor(t, hideCall)
	if !p.view().Zooming {
		t.Fatal("view says not zooming")
	}

	if err := p.setZoom(false, lensLook{}); err != nil {
		t.Fatal(err)
	}
	hypr.waitFor(t, showCall)
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
	hypr := &hyprLog{}
	p := testPointer(t, hypr)
	lens := &fakeLens{}
	p.startLens = lens.start
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	lens.last().stop() // Quickshell quit or crashed
	hypr.waitFor(t, showCall)
	deadline := time.Now().Add(2 * time.Second)
	for p.view().Zooming && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.view().Zooming {
		t.Fatal("view still says zooming")
	}
}

func TestNewLensSettingsRestartTheLens(t *testing.T) {
	hypr := &hyprLog{}
	p := testPointer(t, hypr)
	lens := &fakeLens{}
	p.startLens = lens.start
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
	if slices.Contains(hypr.list(), showCall) {
		t.Fatal("the cursor came back while the lens restarted")
	}
}

func TestLensTakesThePhoneThemeColors(t *testing.T) {
	p := testPointer(t, nil)
	lens := &fakeLens{}
	p.startLens = lens.start
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
	p := testPointer(t, nil)
	lens := &fakeLens{}
	p.startLens = lens.start
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

func TestRestoreOnlyShowsACursorItHid(t *testing.T) {
	hypr := &hyprLog{}
	p := testPointer(t, hypr)
	p.restore()
	if got := hypr.list(); len(got) != 0 {
		t.Fatalf("restore with nothing hidden called hyprctl: %q", got)
	}
	p.hideCursor(true) // then pc-remote crashed
	p.restore()
	hypr.waitFor(t, showCall)
	if _, err := os.Stat(p.hiddenMark); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the hidden mark is still there: %v", err)
	}
}

func TestHideCursorFallsBackToKeyword(t *testing.T) {
	hypr := &hyprLog{fail: func(args []string) bool { return args[0] == "eval" }}
	p := testPointer(t, hypr)
	p.hideCursor(true)
	hypr.waitFor(t, "keyword cursor:invisible true")
}

func TestZoomNeedsQuickshell(t *testing.T) {
	p := testPointer(t, nil)
	p.canZoom = false
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

// hyprBatchReply is what Hyprland answers to "[[BATCH]]j/cursorpos;j/activewindow".
const hyprBatchReply = "{\n    \"x\": 5100,\n    \"y\": 600\n}\n\n\n\n{\n    \"address\": \"0x55b9933fd940\",\n    \"at\": [3840, 0],\n    \"size\": [2560, 1440],\n    \"class\": \"chromium\"\n}"

func TestParseSpot(t *testing.T) {
	spot, err := parseSpot(strings.NewReader(hyprBatchReply))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spot.line(), "5100 600 3840 0 2560 1440 0x55b9933fd940\n"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	for _, reply := range []string{"{\"x\": 10, \"y\": 20}\n\n{}", "{\"x\": 10, \"y\": 20}"} {
		spot, err := parseSpot(strings.NewReader(reply))
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		if got, want := spot.line(), "10 20 0 0 0 0 -\n"; got != want {
			t.Fatalf("%q: line = %q, want %q", reply, got, want)
		}
	}
	if _, err := parseSpot(strings.NewReader("unknown request")); err == nil {
		t.Fatal("parseSpot accepted garbage")
	}
}

func TestLensFilesAreEmbedded(t *testing.T) {
	dir := t.TempDir()
	if err := writeLensFiles(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shell.qml", "sharpen.frag.qsb"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() == 0 {
			t.Errorf("%s missing or empty: %v", name, err)
		}
	}
}
