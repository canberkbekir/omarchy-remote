package main

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows lens: a round, click-through window that follows the pointer
// and shows the screen under it magnified by a Magnifier control. The
// screen itself is never transformed, so a crash can't leave it zoomed: the
// window goes with the process.

const (
	noLensReason  = "zoom needs the Windows Magnification API"
	noLensMessage = "Zoom needs the Windows Magnification API on the PC."
)

var (
	magnification              = windows.NewLazySystemDLL("Magnification.dll")
	procMagInitialize          = magnification.NewProc("MagInitialize")
	procMagUninitialize        = magnification.NewProc("MagUninitialize")
	procMagSetWindowSource     = magnification.NewProc("MagSetWindowSource")
	procMagSetWindowTransform  = magnification.NewProc("MagSetWindowTransform")
	procMagSetWindowFilterList = magnification.NewProc("MagSetWindowFilterList")

	procGetCursorPos                 = trayUser32.NewProc("GetCursorPos")
	procSetThreadDpiAwarenessContext = trayUser32.NewProc("SetThreadDpiAwarenessContext")
	procSetWindowPos                 = trayUser32.NewProc("SetWindowPos")
	procSetWindowRgn                 = trayUser32.NewProc("SetWindowRgn")
	procSetLayeredWindowAttributes   = trayUser32.NewProc("SetLayeredWindowAttributes")
	procShowWindow                   = trayUser32.NewProc("ShowWindow")
	procInvalidateRect               = trayUser32.NewProc("InvalidateRect")
	procPostQuitMessage              = trayUser32.NewProc("PostQuitMessage")
	procMonitorFromPoint             = trayUser32.NewProc("MonitorFromPoint")
	procDestroyWindow                = trayUser32.NewProc("DestroyWindow")
	procFillRect                     = trayUser32.NewProc("FillRect")

	lensGdi32             = windows.NewLazySystemDLL("gdi32.dll")
	procCreateEllipticRgn = lensGdi32.NewProc("CreateEllipticRgn")
	procCreateSolidBrush  = lensGdi32.NewProc("CreateSolidBrush")
	procDeleteObject      = lensGdi32.NewProc("DeleteObject")

	procSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
)

const (
	wsChild   = 0x40000000
	wsVisible = 0x10000000
	// Layered and transparent: clicks go through to what's under the lens.
	wsExLayered      = 0x00080000
	wsExTransparent  = 0x00000020
	wsExTopmost      = 0x00000008
	wsExNoActivate   = 0x08000000
	lwaAlpha         = 0x2
	swShowNoActivate = 4
	swpNoActivate    = 0x0010
	swpNoSize        = 0x0001
	hwndTopmost      = ^uintptr(0) // (HWND)-1
	wmClose          = 0x0010
	wmDestroy        = 0x0002
	wmEraseBkgnd     = 0x0014
	mwFilterExclude  = 0
	monitorNearest   = 2
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2: real pixels for the
	// pointer, the screen and the lens alike.
	dpiPerMonitorAwareV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4
)

// magLens runs one lens window at a time.
type magLens struct {
	mu   sync.Mutex
	last *lensRun // the newest run, which a new one waits out
}

func newLensDriver(string) lensDriver { return &magLens{} }

func zoomAvailable() bool {
	for _, proc := range []*windows.LazyProc{procMagInitialize, procMagUninitialize, procMagSetWindowSource, procMagSetWindowTransform} {
		if proc.Find() != nil {
			return false
		}
	}
	return true
}

func (l *magLens) available() bool { return zoomAvailable() }

// The real pointer stays: it sits at the center of the lens, on the spot a
// click lands.
func (l *magLens) hideCursor(bool) {}

// Nothing outlives the process.
func (l *magLens) restore() {}

// lensWindow is the running lens, for the window procedure.
type lensWindow struct {
	host, view windows.HWND
	brush      uintptr // the ring color
	size       int32
	zoom       int
	last       lensPlacement
}

var (
	lensClassOnce sync.Once
	lensClassErr  error
	lensClass     *uint16
	// activeLens is the one lens up, read only on its own thread.
	activeLens *lensWindow
)

func registerLensClass() error {
	lensClassOnce.Do(func() {
		instance, _, _ := procGetModuleHandle.Call(0)
		lensClass, _ = windows.UTF16PtrFromString("PCRemoteLens")
		class := wndClassEx{
			WndProc:   windows.NewCallback(lensWindowProc),
			Instance:  windows.Handle(instance),
			ClassName: lensClass,
		}
		class.Size = uint32(unsafe.Sizeof(class))
		if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); r == 0 {
			lensClassErr = fmt.Errorf("RegisterClassEx: %w", err)
		}
	})
	return lensClassErr
}

func lensWindowProc(wnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTimer:
		if activeLens != nil {
			activeLens.follow()
		}
		return 0
	case wmEraseBkgnd:
		// The ring is the host window's background.
		if activeLens != nil {
			rect := [4]int32{0, 0, activeLens.size, activeLens.size}
			procFillRect.Call(wParam, uintptr(unsafe.Pointer(&rect)), activeLens.brush)
		}
		return 1
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(wnd, message, wParam, lParam)
	return r
}

// start runs the lens on its own locked thread: the Magnifier control wants
// every call from the thread that called MagInitialize, and a window's
// messages go to the thread that made it.
func (l *magLens) start(s pointerSettings, look lensLook) (*lensRun, error) {
	// A restart stops the old lens just before this one: let it go first.
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last != nil {
		select {
		case <-l.last.done:
		case <-time.After(2 * time.Second):
			return nil, errors.New("the last lens didn't close")
		}
	}
	if err := registerLensClass(); err != nil {
		return nil, err
	}
	run := &lensRun{done: make(chan struct{})}
	ready := make(chan error, 1)
	var host windows.HWND
	go func() {
		// The thread stays locked to the end, so Go retires it with its
		// changed DPI awareness instead of handing it to other goroutines.
		runtime.LockOSThread()
		defer close(run.done)
		w, err := openLens(lensSizes[s.Lens], s.Zoom, look)
		if err != nil {
			ready <- err
			return
		}
		host = w.host
		ready <- nil
		var m msg
		for {
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
		activeLens = nil
		procDeleteObject.Call(w.brush)
		procMagUninitialize.Call()
	}()
	if err := <-ready; err != nil {
		<-run.done
		return nil, err
	}
	var once sync.Once
	run.stop = func() { once.Do(func() { procPostMessage.Call(uintptr(host), wmClose, 0, 0) }) }
	l.last = run
	return run, nil
}

// openLens makes the lens: a round window in the ring color, with the round
// Magnifier control inside the ring.
func openLens(size, zoom int, look lensLook) (*lensWindow, error) {
	if procSetThreadDpiAwarenessContext.Find() == nil { // Windows 10 1607 and up; older ones scale
		procSetThreadDpiAwarenessContext.Call(dpiPerMonitorAwareV2)
	}
	if r, _, err := procMagInitialize.Call(); r == 0 {
		return nil, fmt.Errorf("MagInitialize: %w", err)
	}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorRef(look.orDefault().Ring)))
	fail := func(err error) (*lensWindow, error) {
		activeLens = nil
		procDeleteObject.Call(brush)
		procMagUninitialize.Call()
		return nil, err
	}
	instance, _, _ := procGetModuleHandle.Call(0)
	host, _, err := procCreateWindowEx.Call(wsExLayered|wsExTransparent|wsExTopmost|wsExToolWin|wsExNoActivate,
		uintptr(unsafe.Pointer(lensClass)), 0, wsPopup, 0, 0, uintptr(size), uintptr(size), 0, 0, instance, 0)
	if host == 0 {
		return fail(fmt.Errorf("CreateWindowEx: %w", err))
	}
	procSetLayeredWindowAttributes.Call(host, 0, 255, lwaAlpha)
	region, _, _ := procCreateEllipticRgn.Call(0, 0, uintptr(size+1), uintptr(size+1))
	procSetWindowRgn.Call(host, region, 1) // the window owns the region now

	inner := size - 2*lensRing
	magClass, _ := windows.UTF16PtrFromString("Magnifier")
	view, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(magClass)), 0, wsChild|wsVisible,
		lensRing, lensRing, uintptr(inner), uintptr(inner), host, 0, instance, 0)
	if view == 0 {
		procDestroyWindow.Call(host)
		return fail(fmt.Errorf("making the Magnifier control: %w", err))
	}
	region, _, _ = procCreateEllipticRgn.Call(0, 0, uintptr(inner+1), uintptr(inner+1))
	procSetWindowRgn.Call(view, region, 1)

	// The lens magnifies the screen, not itself.
	if procMagSetWindowFilterList.Find() == nil {
		procMagSetWindowFilterList.Call(view, mwFilterExclude, 1, uintptr(unsafe.Pointer(&host)))
	}
	z := float32(zoom)
	transform := [9]float32{z, 0, 0, 0, z, 0, 0, 0, 1}
	if r, _, err := procMagSetWindowTransform.Call(view, uintptr(unsafe.Pointer(&transform))); r == 0 {
		procDestroyWindow.Call(host)
		return fail(fmt.Errorf("MagSetWindowTransform: %w", err))
	}

	w := &lensWindow{host: windows.HWND(host), view: windows.HWND(view), brush: brush, size: int32(size), zoom: zoom}
	activeLens = w
	w.follow()
	procShowWindow.Call(host, swShowNoActivate)
	if r, _, err := procSetTimer.Call(host, 1, uintptr(lensFeedEvery/time.Millisecond), 0); r == 0 {
		procDestroyWindow.Call(host)
		return fail(fmt.Errorf("SetTimer: %w", err))
	}
	return w, nil
}

// follow moves the lens onto the pointer, on the monitor the pointer is on.
func (w *lensWindow) follow() {
	var at struct{ X, Y int32 }
	if r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&at))); r == 0 {
		return // the secure desktop (UAC, Ctrl+Alt+Del) hides the pointer for a bit
	}
	p := placeLens(at.X, at.Y, w.size, w.zoom, monitorAt(at.X, at.Y))
	if p != w.last {
		procSetWindowPos.Call(uintptr(w.host), hwndTopmost, uintptr(p.WinX), uintptr(p.WinY), 0, 0, swpNoActivate|swpNoSize)
		w.last = p
	}
	// The source is set every tick: that's what redraws the lens.
	src := [4]int32{p.Src.Left, p.Src.Top, p.Src.Left + p.Src.Width, p.Src.Top + p.Src.Height}
	procMagSetWindowSource.Call(uintptr(w.view), uintptr(unsafe.Pointer(&src)))
	procInvalidateRect.Call(uintptr(w.view), 0, 1)
}

// monitorAt is the monitor under a point, in desktop pixels.
func monitorAt(x, y int32) screenRect {
	point := uintptr(uint32(x)) | uintptr(uint32(y))<<32 // POINT by value
	monitor, _, _ := procMonitorFromPoint.Call(point, monitorNearest)
	info := struct {
		Size          uint32
		Monitor, Work [4]int32
		Flags         uint32
	}{}
	info.Size = uint32(unsafe.Sizeof(info))
	if r, _, _ := procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info))); r == 0 {
		w, _, _ := procGetSystemMetrics.Call(0)
		h, _, _ := procGetSystemMetrics.Call(1)
		return screenRect{Width: int32(w), Height: int32(h)}
	}
	m := info.Monitor
	return screenRect{Left: m[0], Top: m[1], Width: m[2] - m[0], Height: m[3] - m[1]}
}

var consoleOnce sync.Once

// watchConsoleClose runs closeZoom before the console goes: Ctrl+C, closing
// the window, logoff, shutdown. It returns FALSE, so Go's own handler still
// turns the event into a signal for the shutdown in main.
func watchConsoleClose(closeZoom func()) {
	consoleOnce.Do(func() {
		handler := windows.NewCallback(func(event uint32) uintptr {
			closeZoom()
			return 0
		})
		if r, _, err := procSetConsoleCtrlHandler.Call(handler, 1); r == 0 {
			log.Printf("SetConsoleCtrlHandler: %v", err)
		}
	})
}
