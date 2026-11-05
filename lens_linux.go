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

// How long Quickshell gets to start and connect before the lens gives up.
const lensConnectTimeout = 10 * time.Second

const (
	noLensReason  = "zoom needs Hyprland and Quickshell"
	noLensMessage = "Zoom needs Hyprland and Quickshell on the PC."
)

// hyprLens is the Quickshell lens on Hyprland: a circle that follows the
// pointer, with the real cursor hidden while it's up.
type hyprLens struct {
	can bool
	// hiddenMark exists while pc-remote has the cursor hidden, so a restart
	// after a crash knows to bring it back, and otherwise leaves it alone.
	hiddenMark string
	hyprctl    func(args ...string) error
	// run starts Quickshell; a field so tests can stand in for it.
	run func(s pointerSettings, look lensLook) (*lensRun, error)
}

func onHyprland() bool {
	_, err := exec.LookPath("hyprctl")
	return err == nil && os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != ""
}

func newLensDriver(string) lensDriver {
	l := &hyprLens{
		hiddenMark: filepath.Join(runtimeDir(), "pc-remote-cursor-hidden"),
		hyprctl:    func(args ...string) error { _, err := runCommand(append([]string{"hyprctl"}, args...)...); return err },
		run:        startQuickshellLens,
	}
	// The lens files and its socket need a private runtime folder.
	if _, err := exec.LookPath("qs"); err == nil && runtimeDir() != "" {
		l.can = onHyprland()
	}
	return l
}

func (l *hyprLens) available() bool { return l.can }

func (l *hyprLens) start(s pointerSettings, look lensLook) (*lensRun, error) {
	return l.run(s, look)
}

// watchConsoleClose has nothing to do on Linux: the signal handler covers it.
func watchConsoleClose(func()) {}

// restore undoes anything a crash could have left behind: a hidden cursor.
func (l *hyprLens) restore() {
	if _, err := os.Stat(l.hiddenMark); err == nil {
		l.hideCursor(false)
	}
}

// hideCursor uses the Lua config of newer Hyprland, then the older keyword.
func (l *hyprLens) hideCursor(hidden bool) {
	err := l.hyprctl("eval", fmt.Sprintf("hl.config({ cursor = { invisible = %t } })", hidden))
	if err != nil {
		err = l.hyprctl("keyword", "cursor:invisible", strconv.FormatBool(hidden))
	}
	if err != nil {
		log.Printf("hiding the cursor: %v", err)
		return
	}
	if hidden {
		err = os.WriteFile(l.hiddenMark, nil, 0o600)
	} else if err = os.Remove(l.hiddenMark); errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		log.Printf("marking the cursor hidden: %v", err)
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
