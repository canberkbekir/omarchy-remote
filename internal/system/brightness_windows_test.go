package system

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeScreens stands in for Win32 and WMI: DDC lists the handles that answer
// DDC/CI with their raw value, wmi the WMI instances and their percent.
type fakeScreens struct {
	displays []winDisplay
	ddc      map[uintptr]uint32
	lo, hi   uint32
	wmi      map[string]int
	listFail bool // the WMI listing itself fails, as on a PowerShell timeout
	readFail bool // PowerShell fails on a read, as on a timeout
	scripts  []string
	envs     [][]string
}

func useScreens(t *testing.T, f *fakeScreens) {
	probes.Wait()
	enum, read, write, run, list := enumDisplays, ddcRead, ddcWrite, wmiRun, wmiInstances
	pause, every, retry := BrightnessPause, probeEvery, wmiRetryAfter
	BrightnessPause = 0
	brightnessSeen.Store(false)
	lastProbe = time.Time{}
	enumDisplays = func() ([]winDisplay, error) { return f.displays, nil }
	ddcRead = func(h uintptr) (uint32, uint32, uint32, error) {
		v, ok := f.ddc[h]
		if !ok {
			return 0, 0, 0, errors.New("no DDC/CI")
		}
		return v, f.lo, f.hi, nil
	}
	ddcWrite = func(h uintptr, raw uint32) error {
		if _, ok := f.ddc[h]; !ok {
			return errors.New("no DDC/CI")
		}
		f.ddc[h] = raw
		return nil
	}
	wmiRun = func(script string, env ...string) (string, error) {
		f.scripts = append(f.scripts, script)
		f.envs = append(f.envs, env)
		vars := map[string]string{}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			vars[k] = v
		}
		if script == wmiListScript {
			if f.listFail {
				return "", errors.New("timed out")
			}
			var names []string
			for name := range f.wmi {
				names = append(names, name)
			}
			return strings.Join(names, "\r\n") + "\r\n", nil
		}
		level, ok := f.wmi[vars["PCR_WMI_INSTANCE"]]
		if !ok {
			return "", errWMIGone
		}
		if f.readFail && script == wmiReadScript {
			return "", errors.New("timed out")
		}
		if script == wmiSetScript {
			f.wmi[vars["PCR_WMI_INSTANCE"]], _ = strconv.Atoi(vars["PCR_WMI_LEVEL"])
			return "", nil
		}
		return " " + strconv.Itoa(level) + "\r\n", nil
	}
	wmiInstances = cachedWMIInstances()
	t.Cleanup(func() {
		probes.Wait()
		enumDisplays, ddcRead, ddcWrite, wmiRun, wmiInstances = enum, read, write, run, list
		BrightnessPause, probeEvery, wmiRetryAfter = pause, every, retry
		brightnessSeen.Store(false)
		lastProbe = time.Time{}
	})
}

const panelKey = `DISPLAY\BOE0812\4&2d7c5f3a&0&UID8388688`

func laptopWithMonitor() *fakeScreens {
	return &fakeScreens{
		displays: []winDisplay{
			{name: `\\.\DISPLAY1`, label: "Built-in display", handle: 1, wmiKey: panelKey},
			{name: `\\.\DISPLAY2`, label: "DELL U2720Q", handle: 2, wmiKey: `DISPLAY\DELA1A8\5&1a2b3c&0&UID4352`},
		},
		ddc: map[uintptr]uint32{2: 30},
		lo:  0, hi: 100,
		wmi: map[string]int{panelKey + "_0": 65},
	}
}

func TestListMonitorsNamesEveryDisplay(t *testing.T) {
	useScreens(t, laptopWithMonitor())
	got, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	want := []Monitor{
		{Name: `\\.\DISPLAY1`, Label: "Built-in display", On: true},
		{Name: `\\.\DISPLAY2`, Label: "DELL U2720Q", On: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("listMonitors = %+v, want %+v", got, want)
	}
}

func TestBrightnessUsesDDCWhenItAnswers(t *testing.T) {
	f := laptopWithMonitor()
	useScreens(t, f)
	if got, err := platformReadBrightness(`\\.\DISPLAY2`); err != nil || got != 30 {
		t.Fatalf("read = %d, %v; want 30", got, err)
	}
	if err := platformWriteBrightness(`\\.\DISPLAY2`, 80); err != nil || f.ddc[2] != 80 {
		t.Fatalf("write: %v, DDC at %d", err, f.ddc[2])
	}
	for _, s := range f.scripts {
		if s != wmiListScript {
			t.Fatalf("ran WMI for a DDC monitor: %q", s)
		}
	}
}

func TestBrightnessScalesDDCRange(t *testing.T) {
	f := laptopWithMonitor()
	f.lo, f.hi, f.ddc[2] = 0, 50, 25
	useScreens(t, f)
	if got, _ := platformReadBrightness(`\\.\DISPLAY2`); got != 50 {
		t.Fatalf("read = %d, want 50", got)
	}
	if platformWriteBrightness(`\\.\DISPLAY2`, 100); f.ddc[2] != 50 {
		t.Fatalf("DDC raw = %d, want 50", f.ddc[2])
	}
}

func TestBrightnessFallsBackToWMIForPanel(t *testing.T) {
	f := laptopWithMonitor()
	useScreens(t, f)
	if got, err := platformReadBrightness(`\\.\DISPLAY1`); err != nil || got != 65 {
		t.Fatalf("read = %d, %v; want 65", got, err)
	}
	if err := platformWriteBrightness(`\\.\DISPLAY1`, 40); err != nil || f.wmi[panelKey+"_0"] != 40 {
		t.Fatalf("write: %v, panel at %d", err, f.wmi[panelKey+"_0"])
	}
	last := f.envs[len(f.envs)-1]
	if !slices.Contains(last, "PCR_WMI_INSTANCE="+panelKey+"_0") || !slices.Contains(last, "PCR_WMI_LEVEL=40") {
		t.Fatalf("env = %q", last)
	}
}

func TestBrightnessNeverPutsPanelValueOnOtherMonitor(t *testing.T) {
	f := laptopWithMonitor()
	delete(f.ddc, 2) // a TV without DDC/CI
	useScreens(t, f)
	if _, err := platformReadBrightness(`\\.\DISPLAY2`); err == nil {
		t.Fatal("read a monitor that has neither DDC/CI nor WMI")
	}
	if err := platformWriteBrightness(`\\.\DISPLAY2`, 40); err == nil {
		t.Fatal("wrote a monitor that has neither DDC/CI nor WMI")
	}
	if f.wmi[panelKey+"_0"] != 65 {
		t.Fatalf("panel changed to %d", f.wmi[panelKey+"_0"])
	}
}

func TestBrightnessUnknownMonitor(t *testing.T) {
	useScreens(t, laptopWithMonitor())
	if _, err := platformReadBrightness(`\\.\DISPLAY9`); err == nil {
		t.Fatal("read an unknown monitor")
	}
}

func TestBrightnessAvailability(t *testing.T) {
	cases := map[string]func(*fakeScreens){
		"ddc and wmi": func(*fakeScreens) {},
		"ddc only":    func(f *fakeScreens) { f.wmi = nil },
		"wmi only":    func(f *fakeScreens) { f.ddc = nil },
	}
	for name, change := range cases {
		f := laptopWithMonitor()
		change(f)
		useScreens(t, f)
		if !probeBrightness() {
			t.Errorf("%s: probeBrightness = false", name)
		}
	}
	f := laptopWithMonitor()
	f.ddc, f.wmi = nil, nil // a VM
	useScreens(t, f)
	if probeBrightness() {
		t.Error("probeBrightness = true with neither DDC/CI nor WMI")
	}
}

func TestWMIKeyFromInterfaceName(t *testing.T) {
	cases := map[string]string{
		`\\?\DISPLAY#BOE0812#4&2d7c5f3a&0&UID8388688#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`: panelKey,
		`\\?\display#boe0812#4&2d7c5f3a&0&uid8388688#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`: `display\boe0812\4&2d7c5f3a&0&uid8388688`,
		``:            ``,
		`garbage`:     ``,
		`\\?\DISPLAY`: ``,
	}
	for in, want := range cases {
		if got := wmiKey(in); got != want {
			t.Errorf("wmiKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWMIInstanceMatchIgnoresCase(t *testing.T) {
	f := laptopWithMonitor()
	f.wmi = map[string]int{strings.ToLower(panelKey) + "_0": 20}
	useScreens(t, f)
	if got, err := platformReadBrightness(`\\.\DISPLAY1`); err != nil || got != 20 {
		t.Fatalf("read = %d, %v; want 20", got, err)
	}
}

func TestPercentConversions(t *testing.T) {
	if _, err := toPercent(5, 10, 10); err == nil {
		t.Error("toPercent accepted an empty range")
	}
	if got, _ := toPercent(10, 10, 110); got != 0 {
		t.Errorf("toPercent(min) = %d", got)
	}
	if got, _ := toPercent(200, 10, 110); got != 100 {
		t.Errorf("toPercent(above max) = %d, want 100", got)
	}
	if got := fromPercent(50, 10, 110); got != 60 {
		t.Errorf("fromPercent(50) = %d, want 60", got)
	}
}

func TestDisplayLabel(t *testing.T) {
	cases := []struct{ name, device, want string }{
		{`\\.\DISPLAY2`, "DELL U2720Q", "DELL U2720Q"},
		{`\\.\DISPLAY2`, "Generic PnP Monitor", "Display 2"},
		{`\\.\DISPLAY3`, "", "Display 3"},
	}
	for _, c := range cases {
		if got := displayLabel(c.name, c.device); got != c.want {
			t.Errorf("displayLabel(%q, %q) = %q, want %q", c.name, c.device, got, c.want)
		}
	}
}

// TestWin32DisplaysCallsSucceed runs the real calls: whatever monitors this
// machine has, listing them and asking for DDC/CI must not fail or crash.
// A VM's monitors don't answer DDC/CI, so values are checked on real hardware
// by hand (see WINDOWS_PORT.md).
func TestWin32DisplaysCallsSucceed(t *testing.T) {
	displays, err := win32Displays()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range displays {
		t.Logf("%s %q wmiKey=%q", d.name, d.label, d.wmiKey)
		if !strings.HasPrefix(d.name, `\\.\`) || d.label == "" {
			t.Errorf("display %+v", d)
		}
		if cur, lo, hi, err := win32DDCRead(d.handle); err == nil && (hi <= lo || cur > hi) {
			t.Errorf("%s: DDC/CI range %d..%d at %d", d.name, lo, hi, cur)
		}
	}
}

// waitAvailable polls brightnessAvailable the way the phone polls the status.
func waitAvailable() bool {
	for range 100 {
		if brightnessAvailable() {
			return true
		}
		probes.Wait()
	}
	return brightnessAvailable()
}

func TestBrightnessAvailableProbesInBackground(t *testing.T) {
	useScreens(t, laptopWithMonitor())
	if !waitAvailable() {
		t.Fatal("brightness never became available")
	}
}

func TestBrightnessAvailableStaysOffInVM(t *testing.T) {
	f := laptopWithMonitor()
	f.ddc, f.wmi = nil, nil
	useScreens(t, f)
	probeEvery = time.Hour
	if waitAvailable() {
		t.Fatal("brightness available with neither DDC/CI nor WMI")
	}
	calls := 0
	enumDisplays = func() ([]winDisplay, error) { calls++; return f.displays, nil }
	brightnessAvailable()
	probes.Wait()
	if calls != 0 {
		t.Fatalf("probed again %d times within probeEvery", calls)
	}
}

func TestBrightnessAvailableRetriesFailedProbe(t *testing.T) {
	f := laptopWithMonitor()
	f.ddc, f.wmi = map[uintptr]uint32{}, nil // the monitor is asleep at first
	useScreens(t, f)
	probeEvery = 0
	brightnessAvailable()
	probes.Wait()
	f.ddc[2] = 50
	if !waitAvailable() {
		t.Fatal("a failed probe was never tried again")
	}
}

func TestBrightnessSeenByHandlerCounts(t *testing.T) {
	useScreens(t, laptopWithMonitor())
	probeEvery = time.Hour
	lastProbe = time.Now() // no probe will run
	platformReadBrightness(`\\.\DISPLAY2`)
	if !brightnessAvailable() {
		t.Fatal("a monitor that answered a read does not count")
	}
}

func TestWMIListRetriedAfterFailure(t *testing.T) {
	f := laptopWithMonitor()
	f.listFail = true
	useScreens(t, f)
	wmiRetryAfter = time.Hour
	if _, err := platformReadBrightness(`\\.\DISPLAY1`); err == nil {
		t.Fatal("read the panel while WMI could not be listed")
	}
	f.listFail = false
	if _, err := platformReadBrightness(`\\.\DISPLAY1`); err == nil {
		t.Fatal("listed WMI again before wmiRetryAfter")
	}
	wmiRetryAfter = 0
	if got, err := platformReadBrightness(`\\.\DISPLAY1`); err != nil || got != 65 {
		t.Fatalf("read = %d, %v; want 65 once WMI lists again", got, err)
	}
}

func TestWMIEmptyListIsCached(t *testing.T) {
	f := laptopWithMonitor()
	f.wmi = nil
	useScreens(t, f)
	wmiRetryAfter = 0
	platformReadBrightness(`\\.\DISPLAY1`)
	platformReadBrightness(`\\.\DISPLAY1`)
	if len(f.scripts) != 1 {
		t.Fatalf("ran %d WMI scripts for a machine without WMI brightness, want 1 listing", len(f.scripts))
	}
}

func TestDDCWriteRejectsEmptyRange(t *testing.T) {
	f := laptopWithMonitor()
	f.lo, f.hi = 40, 40
	useScreens(t, f)
	if err := platformWriteBrightness(`\\.\DISPLAY2`, 80); err == nil {
		t.Fatal("wrote a monitor with an empty brightness range")
	}
	if f.ddc[2] != 30 {
		t.Fatalf("DDC raw = %d, want unchanged 30", f.ddc[2])
	}
}

func countScripts(f *fakeScreens, script string) int {
	n := 0
	for _, s := range f.scripts {
		if s == script {
			n++
		}
	}
	return n
}

func TestWMIFailureIsNotRetried(t *testing.T) {
	f := laptopWithMonitor()
	useScreens(t, f)
	a := &Brightness{}
	if got := a.Read(`\\.\DISPLAY1`); got == nil || *got != 65 {
		t.Fatalf("readBrightness = %v, want 65", got)
	}
	f.readFail = true
	before := countScripts(f, wmiReadScript)
	if got := a.Read(`\\.\DISPLAY1`); got != nil {
		t.Fatalf("readBrightness = %d, want nil", *got)
	}
	if runs := countScripts(f, wmiReadScript) - before; runs != 1 {
		t.Fatalf("ran PowerShell %d times for one failed read, want 1", runs)
	}
}

func TestDDCFailureIsStillRetried(t *testing.T) {
	f := laptopWithMonitor()
	useScreens(t, f)
	a := &Brightness{}
	a.Read(`\\.\DISPLAY2`)
	tries := 0
	ddcRead = func(uintptr) (uint32, uint32, uint32, error) {
		tries++
		if tries == 1 {
			return 0, 0, 0, errNoDDC
		}
		return 30, 0, 100, nil
	}
	if got := a.Read(`\\.\DISPLAY2`); got == nil || *got != 30 {
		t.Fatalf("readBrightness = %v, want 30 after one dropped DDC request", got)
	}
}

func TestWMIInstanceGoneRelists(t *testing.T) {
	f := laptopWithMonitor()
	useScreens(t, f)
	wmiRetryAfter = time.Hour
	if _, err := platformReadBrightness(`\\.\DISPLAY1`); err != nil {
		t.Fatal(err)
	}
	delete(f.wmi, panelKey+"_0") // the driver came back under a new instance
	f.wmi[panelKey+"_1"] = 25
	if _, err := platformReadBrightness(`\\.\DISPLAY1`); err == nil {
		t.Fatal("read an instance WMI no longer has")
	}
	if got, err := platformReadBrightness(`\\.\DISPLAY1`); err != nil || got != 25 {
		t.Fatalf("read = %d, %v; want 25 from the new instance", got, err)
	}
	if lists := countScripts(f, wmiListScript); lists != 2 {
		t.Fatalf("listed WMI %d times, want 2", lists)
	}
}

func TestWMIGoneFromExitCode(t *testing.T) {
	if !errors.Is(wmiError(&exec.ExitError{ProcessState: exitState(t, 3)}), errWMIGone) {
		t.Fatal("exit code 3 is not errWMIGone")
	}
	err := wmiError(errors.New("timed out"))
	if !errors.Is(err, errNoRetry) || errors.Is(err, errWMIGone) {
		t.Fatalf("wmiError(timeout) = %v", err)
	}
}

// exitState runs cmd /c exit code to get a real ProcessState with that code.
func exitState(t *testing.T, code int) *os.ProcessState {
	t.Helper()
	err := exec.Command("cmd", "/c", "exit", strconv.Itoa(code)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Skipf("cmd exit %d: %v", code, err)
	}
	return exit.ProcessState
}
