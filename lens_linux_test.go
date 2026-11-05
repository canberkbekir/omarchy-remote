//go:build linux

package main

import (
	"bufio"
	"errors"
	"io"
	"net"
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

func testHyprLens(t *testing.T, hypr *hyprLog) *hyprLens {
	t.Helper()
	if hypr == nil {
		hypr = &hyprLog{}
	}
	return &hyprLens{
		can:        true,
		hiddenMark: filepath.Join(t.TempDir(), "cursor-hidden"),
		hyprctl:    hypr.run,
		run:        (&fakeDriver{}).start,
	}
}

func TestZoomHidesTheHyprlandCursor(t *testing.T) {
	hypr := &hyprLog{}
	p := testPointer(t, nil)
	p.driver = testHyprLens(t, hypr)
	if err := p.setZoom(true, lensLook{}); err != nil {
		t.Fatal(err)
	}
	hypr.waitFor(t, hideCall)
	if err := p.setZoom(false, lensLook{}); err != nil {
		t.Fatal(err)
	}
	hypr.waitFor(t, showCall)
}

func TestRestoreOnlyShowsACursorItHid(t *testing.T) {
	hypr := &hyprLog{}
	p := testHyprLens(t, hypr)
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
	p := testHyprLens(t, hypr)
	p.hideCursor(true)
	hypr.waitFor(t, "keyword cursor:invisible true")
}

func TestFeedLensSendsThePointer(t *testing.T) {
	dir := t.TempDir()
	hypr, err := net.Listen("unix", filepath.Join(dir, "hypr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer hypr.Close()
	go func() {
		for {
			conn, err := hypr.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 128)
			conn.Read(buf)
			io.WriteString(conn, hyprBatchReply)
			conn.Close()
		}
	}()

	feed, err := net.Listen("unix", filepath.Join(dir, "feed.sock"))
	if err != nil {
		t.Fatal(err)
	}
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		feedLens(feed, quit, filepath.Join(dir, "hypr.sock"))
		close(finished)
	}()

	lens, err := net.Dial("unix", filepath.Join(dir, "feed.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lens.Close()
	lens.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(lens).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if want := "5100 600 3840 0 2560 1440 0x55b9933fd940\n"; line != want {
		t.Fatalf("lens got %q, want %q", line, want)
	}
	close(quit)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("feedLens kept running after quit")
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
