package main

import (
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The tray icon: Windows' stand-in for the Omarchy bar widget. Its tooltip
// says whether a phone is connected; clicking it shows the passcode page, and
// its menu opens the app, starts or ends a party, or quits.

//go:embed tray.ico
var trayIcon []byte

var (
	trayUser32                 = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassEx        = trayUser32.NewProc("RegisterClassExW")
	procCreateWindowEx         = trayUser32.NewProc("CreateWindowExW")
	procDefWindowProc          = trayUser32.NewProc("DefWindowProcW")
	procGetMessage             = trayUser32.NewProc("GetMessageW")
	procTranslateMessage       = trayUser32.NewProc("TranslateMessage")
	procDispatchMessage        = trayUser32.NewProc("DispatchMessageW")
	procPostMessage            = trayUser32.NewProc("PostMessageW")
	procRegisterWindowMessage  = trayUser32.NewProc("RegisterWindowMessageW")
	procSetTimer               = trayUser32.NewProc("SetTimer")
	procCreatePopupMenu        = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenu             = trayUser32.NewProc("AppendMenuW")
	procSetMenuDefaultItem     = trayUser32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu         = trayUser32.NewProc("TrackPopupMenu")
	procDestroyMenu            = trayUser32.NewProc("DestroyMenu")
	procSetForegroundWindow    = trayUser32.NewProc("SetForegroundWindow")
	procGetSystemMetrics       = trayUser32.NewProc("GetSystemMetrics")
	procCreateIconFromResource = trayUser32.NewProc("CreateIconFromResourceEx")
	procGetModuleHandle        = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	trayShell32                = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIcon        = trayShell32.NewProc("Shell_NotifyIconW")
	procShellExecute           = trayShell32.NewProc("ShellExecuteW")
)

const (
	wmNull        = 0x0000
	wmContextMenu = 0x007B
	wmTimer       = 0x0113
	wmApp         = 0x8000
	trayCallback  = wmApp + 1 // the icon's mouse and keyboard events
	trayQuitMsg   = wmApp + 2 // Quit from the menu, run on the tray thread
	ninSelect     = 0x0400    // WM_USER: click, with NOTIFYICON_VERSION_4
	ninKeySelect  = 0x0401    // Enter or Space on the icon

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4
	nifMessage    = 0x01
	nifIcon       = 0x02
	nifTip        = 0x04
	nifShowTip    = 0x80
	notifyIconV4  = 4

	wsPopup      = 0x80000000
	wsExToolWin  = 0x00000080
	smCxSmIcon   = 49
	smCySmIcon   = 50
	mfString     = 0x0000
	mfGrayed     = 0x0001
	mfChecked    = 0x0008
	mfSeparator  = 0x0800
	tpmRightBtn  = 0x0002
	tpmNoNotify  = 0x0080
	tpmReturnCmd = 0x0100
	swShowNormal = 1

	trayRefresh = 2000 // ms between tooltip updates
)

// notifyIconData is NOTIFYICONDATAW.
type notifyIconData struct {
	Size            uint32
	Wnd             windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32 // also uTimeout
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     windows.Handle
}

// wndClassEx is WNDCLASSEXW.
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

// msg is MSG.
type msg struct {
	Wnd     windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

type tray struct {
	app            *app
	base           string // the server's local address
	wnd            windows.HWND
	icon           windows.Handle
	taskbarCreated uint32
	added          bool
	tip            string
}

// activeTray is the running tray, for closeDesktopIntegration.
var activeTray atomic.Pointer[tray]

// desktopIntegration ties the server into the desktop: on Windows, the tray
// icon.
func desktopIntegration(a *app, addr string) {
	base, err := localURL(addr)
	if err != nil {
		log.Printf("tray: %v", err)
		return
	}
	go func() {
		if err := runTray(a, base); err != nil {
			log.Printf("tray icon: %v", err)
		}
	}()
}

// runTray owns the icon's window, so it keeps its OS thread: Windows sends a
// window's messages to the thread that made it.
func runTray(a *app, base string) error {
	runtime.LockOSThread()
	t := &tray{app: a, base: base}
	icon, err := loadTrayIcon()
	if err != nil {
		return err
	}
	t.icon = icon

	instance, _, _ := procGetModuleHandle.Call(0)
	className, _ := windows.UTF16PtrFromString("PCRemoteTray")
	class := wndClassEx{
		WndProc:   windows.NewCallback(t.windowProc),
		Instance:  windows.Handle(instance),
		ClassName: className,
	}
	class.Size = uint32(unsafe.Sizeof(class))
	if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); r == 0 {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}
	// A hidden top-level window, not a message-only one: only top-level
	// windows hear the TaskbarCreated broadcast after explorer restarts.
	title, _ := windows.UTF16PtrFromString("PC Remote")
	wnd, _, err := procCreateWindowEx.Call(wsExToolWin, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsPopup, 0, 0, 0, 0, 0, 0, instance, 0)
	if wnd == 0 {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}
	t.wnd = windows.HWND(wnd)
	created, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(created)))
	t.taskbarCreated = uint32(r)
	activeTray.Store(t)

	// At logon the server can start before explorer: the timer retries, and
	// TaskbarCreated adds the icon once the taskbar is up.
	t.add()
	defer t.remove()
	if r, _, err := procSetTimer.Call(wnd, 1, trayRefresh, 0); r == 0 {
		return fmt.Errorf("SetTimer: %w", err)
	}
	var m msg
	for {
		r, _, err := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(r) {
		case -1:
			return fmt.Errorf("GetMessage: %w", err)
		case 0:
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// loadTrayIcon makes the embedded icon at the size the notification area
// draws, picking the best image from tray.ico.
func loadTrayIcon() (windows.Handle, error) {
	cx, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySmIcon)
	image, err := pickIcon(trayIcon, int(cx))
	if err != nil {
		return 0, err
	}
	icon, _, err := procCreateIconFromResource.Call(uintptr(unsafe.Pointer(&image[0])),
		uintptr(len(image)), 1, 0x00030000, cx, cy, 0)
	if icon == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx: %w", err)
	}
	return windows.Handle(icon), nil
}

func (t *tray) data(flags uint32) notifyIconData {
	d := notifyIconData{Wnd: t.wnd, ID: 1, Flags: flags, CallbackMessage: trayCallback, Icon: t.icon}
	d.Size = uint32(unsafe.Sizeof(d))
	tip, _ := windows.UTF16FromString(t.tip)
	copy(d.Tip[:len(d.Tip)-1], tip)
	return d
}

func notify(message uint32, d *notifyIconData) bool {
	r, _, _ := procShellNotifyIcon.Call(uintptr(message), uintptr(unsafe.Pointer(d)))
	return r != 0
}

// add puts the icon in the notification area, as after explorer restarts.
func (t *tray) add() {
	t.tip = trayTip(t.app.name, t.app.presence())
	d := t.data(nifMessage | nifIcon | nifTip | nifShowTip)
	notify(nimDelete, &d) // a leftover icon from before a restart makes NIM_ADD fail
	if !notify(nimAdd, &d) {
		t.added = false
		return
	}
	d.Version = notifyIconV4
	notify(nimSetVersion, &d)
	t.added = true
}

// refresh updates the tooltip when the phone or the party changes.
func (t *tray) refresh() {
	if !t.added {
		t.add()
		return
	}
	tip := trayTip(t.app.name, t.app.presence())
	if tip == t.tip {
		return
	}
	t.tip = tip
	d := t.data(nifTip | nifShowTip)
	if !notify(nimModify, &d) {
		t.add() // the taskbar went away without telling us
	}
}

func (t *tray) remove() {
	removeIcon(t.wnd)
	t.added = false
}

// removeIcon takes the icon away. Shell_NotifyIcon works from any thread, so
// closeDesktopIntegration can call it from the signal handler.
func removeIcon(wnd windows.HWND) {
	d := notifyIconData{Wnd: wnd, ID: 1}
	d.Size = uint32(unsafe.Sizeof(d))
	notify(nimDelete, &d)
}

// closeDesktopIntegration removes the tray icon before the server exits, so
// no dead icon stays until the mouse passes over it.
func closeDesktopIntegration() {
	if t := activeTray.Load(); t != nil {
		removeIcon(t.wnd)
	}
}

func (t *tray) windowProc(wnd, message, wParam, lParam uintptr) uintptr {
	switch {
	case uint32(message) == t.taskbarCreated && t.taskbarCreated != 0:
		t.add()
		return 0
	case message == wmTimer:
		t.refresh()
		return 0
	case message == trayQuitMsg:
		t.remove()
		t.app.pointer.close()
		os.Exit(0)
	case message == trayCallback:
		// NOTIFYICON_VERSION_4: the event is in the low word of lParam,
		// the menu's anchor point in wParam.
		switch lParam & 0xFFFF {
		case ninSelect, ninKeySelect:
			t.command(trayShowPasscode)
		case wmContextMenu:
			t.menu(int32(int16(wParam&0xFFFF)), int32(int16(wParam>>16&0xFFFF)))
		}
		return 0
	}
	r, _, _ := procDefWindowProc.Call(wnd, message, wParam, lParam)
	return r
}

func (t *tray) menu(x, y int32) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	item := func(flags uintptr, id int, text string) {
		p, _ := windows.UTF16PtrFromString(text)
		procAppendMenu.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(p)))
	}
	item(mfString, trayShowPasscode, "Show passcode and QR code")
	item(mfString, trayOpenApp, "Open web app")
	party := uintptr(mfString)
	label := "Start party"
	if t.app.party.active() {
		party |= mfChecked
		label = "End party"
	}
	// Off in config.toml, or no game controller on this PC yet.
	if t.app.guestPort == 0 || !partyAvailable {
		party |= mfGrayed
	}
	item(party, trayParty, label)
	item(mfSeparator, 0, "")
	item(mfString, trayQuit, "Quit")
	procSetMenuDefaultItem.Call(menu, trayShowPasscode, 0)

	// Without the foreground the menu stays open after a click elsewhere,
	// and without the WM_NULL it closes at once the next time.
	procSetForegroundWindow.Call(uintptr(t.wnd))
	id, _, _ := procTrackPopupMenu.Call(menu, tpmRightBtn|tpmNoNotify|tpmReturnCmd,
		uintptr(x), uintptr(y), 0, uintptr(t.wnd), 0)
	procPostMessage.Call(uintptr(t.wnd), wmNull, 0, 0)
	t.command(int(id))
}

// command runs a menu command off the tray thread: opening the browser or
// the guest port can take a moment, and the icon must keep answering.
func (t *tray) command(id int) {
	go t.app.trayCommand(id, t.base, openBrowser, func() {
		procPostMessage.Call(uintptr(t.wnd), trayQuitMsg, 0, 0)
	})
}

// openBrowser shows url in the default browser.
func openBrowser(url string) error {
	// ShellExecute may hand the link to a COM handler, which wants an STA.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr, _, _ := procCoInitializeEx.Call(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); int32(hr) >= 0 {
		defer windows.CoUninitialize() // S_FALSE needs balancing too
	}
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	// ShellExecute returns a value above 32 on success.
	if r, _, err := procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, swShowNormal); r <= 32 {
		if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
			err = fmt.Errorf("ShellExecute returned %d", r)
		}
		return err
	}
	return nil
}
