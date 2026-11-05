package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Brightness on Windows: external monitors over DDC/CI through dxva2, and a
// laptop's own panel through WMI, which only PowerShell reaches without a
// COM vtable for IWbemServices. A monitor uses WMI only when its device
// matches a WMI brightness instance, so a TV without DDC/CI never gets the
// panel's value.

// winDisplay is one monitor as Windows lists it.
type winDisplay struct {
	name   string  // \\.\DISPLAY1, the name the phone sends back
	label  string  // what the phone shows
	handle uintptr // HMONITOR
	wmiKey string  // DISPLAY\BOE0812\4&2d7c5f3a&0&UID8388688, see wmiKey
}

// Win32 and WMI sit behind variables so tests can stand in for monitors.
var (
	enumDisplays = win32Displays
	ddcRead      = win32DDCRead
	ddcWrite     = win32DDCWrite
	wmiRun       = runWMI
	wmiInstances = cachedWMIInstances()
)

var (
	errNoDDC   = errors.New("the monitor doesn't answer DDC/CI")
	errNoRange = errors.New("the monitor reports no brightness range")
	// errWMIGone: WMI no longer has the instance, so the cached list is stale.
	errWMIGone = fmt.Errorf("%w: WMI no longer has this monitor", errNoRetry)
)

// wmiGoneExit is the exit code the read and set scripts use when the
// instance is missing, so it can be told apart from PowerShell failing.
const wmiGoneExit = 3

// wmiError marks every WMI failure as not worth a retry: unlike DDC/CI, WMI
// doesn't drop requests, and a failing PowerShell can take commandTimeout.
func wmiError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == wmiGoneExit {
		return errWMIGone
	}
	if errors.Is(err, errNoRetry) {
		return err
	}
	return fmt.Errorf("%w: %w", errNoRetry, err)
}

// The instance name and level reach PowerShell as environment variables,
// never as script text, so nothing a monitor reports can become code.
const (
	// A machine without WMI brightness, like a desktop or a VM, makes
	// Get-CimInstance fail with "Not supported"; that is a clean empty list.
	wmiListScript = `try { Get-CimInstance -Namespace root/WMI -ClassName WmiMonitorBrightness -ErrorAction Stop | ForEach-Object InstanceName } catch { }`
	wmiReadScript = `$m = @(Get-CimInstance -Namespace root/WMI -ClassName WmiMonitorBrightness -ErrorAction Stop | Where-Object InstanceName -eq $env:PCR_WMI_INSTANCE); ` +
		`if ($m.Count -ne 1) { exit 3 }; $m[0].CurrentBrightness`
	wmiSetScript = `$m = @(Get-CimInstance -Namespace root/WMI -ClassName WmiMonitorBrightnessMethods -ErrorAction Stop | Where-Object InstanceName -eq $env:PCR_WMI_INSTANCE); ` +
		`if ($m.Count -ne 1) { exit 3 }; ` +
		`Invoke-CimMethod -InputObject $m[0] -MethodName WmiSetBrightness -Arguments @{Timeout = [uint32]1; Brightness = [byte]$env:PCR_WMI_LEVEL} -ErrorAction Stop | Out-Null`
)

func listMonitors() ([]monitor, error) {
	displays, err := enumDisplays()
	if err != nil {
		return nil, err
	}
	monitors := make([]monitor, len(displays))
	for i, d := range displays {
		monitors[i] = monitor{Name: d.name, Label: d.label, On: true}
	}
	return monitors, nil
}

func findDisplay(name string) (winDisplay, error) {
	displays, err := enumDisplays()
	if err != nil {
		return winDisplay{}, err
	}
	for _, d := range displays {
		if d.name == name {
			return d, nil
		}
	}
	return winDisplay{}, errors.New("unknown monitor")
}

func platformReadBrightness(name string) (int, error) {
	d, err := findDisplay(name)
	if err != nil {
		return 0, err
	}
	return readDisplay(d)
}

func readDisplay(d winDisplay) (int, error) {
	value, err := readDisplayOnce(d)
	if err == nil {
		brightnessSeen.Store(true)
	}
	return value, err
}

func readDisplayOnce(d winDisplay) (int, error) {
	cur, lo, hi, err := ddcRead(d.handle)
	if err == nil {
		return toPercent(cur, lo, hi)
	}
	instance, ok := wmiInstanceFor(d.wmiKey)
	if !ok {
		return 0, err
	}
	out, err := wmiRun(wmiReadScript, "PCR_WMI_INSTANCE="+instance)
	if err != nil {
		return 0, wmiFailed(err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, wmiError(err)
	}
	return value, nil
}

func platformWriteBrightness(name string, target int) error {
	d, err := findDisplay(name)
	if err != nil {
		return err
	}
	target = min(max(target, 0), 100)
	// The monitor's own range comes with every read, so DDC/CI writes read first.
	if _, lo, hi, err := ddcRead(d.handle); err == nil {
		if hi <= lo {
			return errNoRange
		}
		return ddcWrite(d.handle, fromPercent(target, lo, hi))
	}
	instance, ok := wmiInstanceFor(d.wmiKey)
	if !ok {
		return errNoDDC
	}
	if _, err := wmiRun(wmiSetScript, "PCR_WMI_INSTANCE="+instance, "PCR_WMI_LEVEL="+strconv.Itoa(target)); err != nil {
		return wmiFailed(err)
	}
	return nil
}

// platformForgetMonitor: Windows keeps nothing about a monitor between requests.
func platformForgetMonitor(string) {}

// Whether any monitor answers, over DDC/CI or WMI, is worked out in the
// background: a probe can take seconds, and /api/status must not wait for it.
// The phone polls the status, so it shows brightness once a probe succeeds.
// One success is enough for good; a failed probe is tried again after
// probeEvery, since DDC/CI drops requests and a monitor may have been asleep.
// A VM's virtual monitor answers neither and never shows brightness.
var (
	brightnessSeen atomic.Bool // some monitor reported a brightness
	probeMu        sync.Mutex
	probing        bool
	lastProbe      time.Time
	probes         sync.WaitGroup
	probeEvery     = time.Minute
)

func brightnessAvailable() bool {
	if brightnessSeen.Load() {
		return true
	}
	probeMu.Lock()
	defer probeMu.Unlock()
	if !probing && (lastProbe.IsZero() || time.Since(lastProbe) >= probeEvery) {
		probing, lastProbe = true, time.Now()
		probes.Add(1)
		go func() {
			defer probes.Done()
			probeBrightness()
			probeMu.Lock()
			probing = false
			probeMu.Unlock()
		}()
	}
	return false
}

// probeBrightness asks every monitor, a few times each like a monitor that
// has answered before, and reports whether one did.
func probeBrightness() bool {
	displays, err := enumDisplays()
	if err != nil {
		return false
	}
	for _, d := range displays {
		for attempt := range brightnessAttempts {
			if attempt > 0 {
				time.Sleep(brightnessPause)
			}
			_, err := readDisplay(d)
			if err == nil {
				return true
			}
			if errors.Is(err, errNoRetry) {
				break
			}
		}
	}
	return false
}

// wmiRetryAfter: a WMI listing that failed outright, say PowerShell timing out
// on a cold start, is tried again after this long.
var wmiRetryAfter = time.Minute

// wmiCache lists WMI's brightness instances once: they belong to built-in
// panels, which rarely come and go. Machines without one, like desktops and
// VMs, list none, and WMI is not asked again. When an instance disappears
// (the display driver reinstalled, say), forget makes the next request list again.
type wmiCache struct {
	mu       sync.Mutex
	names    []string
	listed   bool
	failedAt time.Time
}

func cachedWMIInstances() *wmiCache { return &wmiCache{} }

func (c *wmiCache) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listed || !c.failedAt.IsZero() && time.Since(c.failedAt) < wmiRetryAfter {
		return c.names
	}
	out, err := wmiRun(wmiListScript)
	if err != nil {
		c.failedAt = time.Now()
		return nil
	}
	c.names, c.listed = strings.Fields(out), true
	return c.names
}

func (c *wmiCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names, c.listed, c.failedAt = nil, false, time.Time{}
}

// wmiFailed turns a PowerShell failure into wmiError's, and drops the cached
// instance list when the instance is gone, so the next request lists again.
func wmiFailed(err error) error {
	err = wmiError(err)
	if errors.Is(err, errWMIGone) {
		wmiInstances.forget()
	}
	return err
}

// wmiInstanceFor finds the WMI instance of a monitor: WMI names it after the
// monitor's device instance plus "_0".
func wmiInstanceFor(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	for _, instance := range wmiInstances.list() {
		if len(instance) > len(key) && strings.EqualFold(instance[:len(key)], key) && instance[len(key)] == '_' {
			return instance, true
		}
	}
	return "", false
}

// wmiKey turns a monitor's device interface name,
// \\?\DISPLAY#BOE0812#4&2d7c5f3a&0&UID8388688#{e6f07b5f-...}, into the device
// instance WMI names its brightness after: DISPLAY\BOE0812\4&2d7c5f3a&0&UID8388688.
func wmiKey(interfaceName string) string {
	rest, ok := strings.CutPrefix(interfaceName, `\\?\`)
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "#")
	if len(parts) != 4 || !strings.HasPrefix(parts[3], "{") || slices.Contains(parts[:3], "") {
		return ""
	}
	return strings.Join(parts[:3], `\`)
}

// displayLabel prefers the monitor's own name. Windows calls most monitors
// "Generic PnP Monitor", which says nothing, so those get their number.
func displayLabel(name, device string) string {
	if device == "" || strings.HasPrefix(device, "Generic") {
		return "Display " + strings.TrimPrefix(name, `\\.\DISPLAY`)
	}
	return device
}

func toPercent(cur, lo, hi uint32) (int, error) {
	if hi <= lo {
		return 0, errNoRange
	}
	cur = min(max(cur, lo), hi)
	return int(math.Round(float64(cur-lo) * 100 / float64(hi-lo))), nil
}

func fromPercent(percent int, lo, hi uint32) uint32 {
	return lo + uint32(math.Round(float64(percent)*float64(hi-lo)/100))
}

func runWMI(script string, env ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	// No console window flashes up when pc-remote runs without one.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	return string(out), err
}

var (
	brightUser32             = windows.NewLazySystemDLL("user32.dll")
	procEnumDisplayMonitors  = brightUser32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfo       = brightUser32.NewProc("GetMonitorInfoW")
	procEnumDisplayDevices   = brightUser32.NewProc("EnumDisplayDevicesW")
	dxva2                    = windows.NewLazySystemDLL("dxva2.dll")
	procNumPhysicalMonitors  = dxva2.NewProc("GetNumberOfPhysicalMonitorsFromHMONITOR")
	procGetPhysicalMonitors  = dxva2.NewProc("GetPhysicalMonitorsFromHMONITOR")
	procDestroyPhysical      = dxva2.NewProc("DestroyPhysicalMonitors")
	procGetMonitorBrightness = dxva2.NewProc("GetMonitorBrightness")
	procSetMonitorBrightness = dxva2.NewProc("SetMonitorBrightness")
)

const eddGetDeviceInterfaceName = 0x1

type monitorInfoEx struct {
	size          uint32
	monitor, work windows.Rect
	flags         uint32
	device        [32]uint16
}

type displayDevice struct {
	size   uint32
	name   [32]uint16
	str    [128]uint16
	flags  uint32
	id     [128]uint16
	regKey [128]uint16
}

// physicalMonitor is PHYSICAL_MONITOR. The header packs it to 1 byte, which
// changes nothing here: the handle comes first and the size stays a multiple of 8.
type physicalMonitor struct {
	handle      windows.Handle
	description [128]uint16
}

// EnumDisplayMonitors reports each monitor to a callback. Callbacks are a
// limited resource in Go, so there is one, and a lock around its results.
var (
	monitorsMu    sync.Mutex
	monitorsFound []uintptr
	monitorFound  = windows.NewCallback(func(hmonitor, _, _, _ uintptr) uintptr {
		monitorsFound = append(monitorsFound, hmonitor)
		return 1
	})
)

func win32Displays() ([]winDisplay, error) {
	monitorsMu.Lock()
	monitorsFound = nil
	ok, _, err := procEnumDisplayMonitors.Call(0, 0, monitorFound, 0)
	handles := monitorsFound
	monitorsMu.Unlock()
	if ok == 0 {
		return nil, err
	}
	var displays []winDisplay
	for _, h := range handles {
		info := monitorInfoEx{size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		if ok, _, _ := procGetMonitorInfo.Call(h, uintptr(unsafe.Pointer(&info))); ok == 0 {
			continue
		}
		name := windows.UTF16ToString(info.device[:])
		device, id := monitorDevice(name)
		displays = append(displays, winDisplay{name: name, label: displayLabel(name, device), handle: h, wmiKey: wmiKey(id)})
	}
	slices.SortFunc(displays, func(a, b winDisplay) int { return cmp.Compare(a.name, b.name) })
	return displays, nil
}

// monitorDevice returns the name and device interface name of the first
// monitor on a display adapter output.
func monitorDevice(adapter string) (string, string) {
	name, err := windows.UTF16PtrFromString(adapter)
	if err != nil {
		return "", ""
	}
	dd := displayDevice{size: uint32(unsafe.Sizeof(displayDevice{}))}
	if ok, _, _ := procEnumDisplayDevices.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&dd)), eddGetDeviceInterfaceName); ok == 0 {
		return "", ""
	}
	return windows.UTF16ToString(dd.str[:]), windows.UTF16ToString(dd.id[:])
}

// ddcMu keeps DDC/CI to one request at a time, like app.ddc does for the
// handlers, so the background probe never talks over a handler's request.
var ddcMu sync.Mutex

// withPhysicalMonitor opens the DDC/CI handle behind an HMONITOR and closes it after.
func withPhysicalMonitor(hmonitor uintptr, use func(windows.Handle) error) error {
	ddcMu.Lock()
	defer ddcMu.Unlock()
	if err := dxva2.Load(); err != nil {
		return err
	}
	var count uint32
	if ok, _, _ := procNumPhysicalMonitors.Call(hmonitor, uintptr(unsafe.Pointer(&count))); ok == 0 || count == 0 {
		return errNoDDC
	}
	monitors := make([]physicalMonitor, count)
	if ok, _, _ := procGetPhysicalMonitors.Call(hmonitor, uintptr(count), uintptr(unsafe.Pointer(&monitors[0]))); ok == 0 {
		return errNoDDC
	}
	defer func() {
		procDestroyPhysical.Call(uintptr(count), uintptr(unsafe.Pointer(&monitors[0])))
		runtime.KeepAlive(monitors)
	}()
	return use(monitors[0].handle)
}

func win32DDCRead(hmonitor uintptr) (cur, lo, hi uint32, err error) {
	err = withPhysicalMonitor(hmonitor, func(h windows.Handle) error {
		if ok, _, _ := procGetMonitorBrightness.Call(uintptr(h), uintptr(unsafe.Pointer(&lo)), uintptr(unsafe.Pointer(&cur)), uintptr(unsafe.Pointer(&hi))); ok == 0 {
			return errNoDDC
		}
		return nil
	})
	return cur, lo, hi, err
}

func win32DDCWrite(hmonitor uintptr, raw uint32) error {
	return withPhysicalMonitor(hmonitor, func(h windows.Handle) error {
		if ok, _, _ := procSetMonitorBrightness.Call(uintptr(h), uintptr(raw)); ok == 0 {
			return errNoDDC
		}
		return nil
	})
}
