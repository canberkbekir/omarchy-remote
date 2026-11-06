package cli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/canberkbekir/omarchy-remote/internal/input"
)

// fakeWindows swaps the commands setup runs on Windows for a recorder. reply
// answers each command by its first words; a missing answer fails it.
func fakeWindows(t *testing.T, reply map[string]string) *[][]string {
	t.Helper()
	var ran [][]string
	oldRun, oldElevate, oldUser, oldInput, oldDir, oldStop := winRun, elevate, currentUser, sendInputWorks, systemDir, stopServers
	stopServers = func() error {
		ran = append(ran, []string{"stop other copies"})
		return nil
	}
	systemDir = func() (string, error) { return `C:\Windows\system32`, nil }
	winRun = func(argv ...string) (string, error) {
		ran = append(ran, argv)
		for prefix, out := range reply {
			if strings.HasPrefix(strings.Join(argv, " "), prefix) {
				return out, nil
			}
		}
		return "", errors.New("exit status 1")
	}
	elevate = func(exe, params string) error {
		ran = append(ran, []string{"runas", exe, params})
		return nil
	}
	currentUser = func() string { return `PC\canberk` }
	sendInputWorks = func() bool { return true }
	t.Cleanup(func() {
		winRun, elevate, currentUser, sendInputWorks, systemDir, stopServers = oldRun, oldElevate, oldUser, oldInput, oldDir, oldStop
	})
	return &ran
}

func TestTaskXML(t *testing.T) {
	x := taskXML(`PC\canberk`, `C:\Program Files\PC Remote\pc-remote-web.exe`)
	for _, want := range []string{
		"<LogonTrigger>", `<UserId>PC\canberk</UserId>`,
		"<LogonType>InteractiveToken</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", "<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		`<Command>%SystemRoot%\System32\conhost.exe</Command>`,
		`<Arguments>--headless &#34;C:\Program Files\PC Remote\pc-remote-web.exe&#34;</Arguments>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %s:\n%s", want, x)
		}
	}
	// It runs as the person who is signed in, never as a service account.
	for _, never := range []string{"SYSTEM", "S-1-5-18", "ServiceAccount", "HighestAvailable", "Password"} {
		if strings.Contains(x, never) {
			t.Errorf("task XML has %s", never)
		}
	}
	if x := taskXML(`a<b`, `C:\x&y.exe`); strings.Contains(x, "a<b") || strings.Contains(x, `x&y`) {
		t.Errorf("not escaped: %s", x)
	}
}

func TestCreateTask(t *testing.T) {
	ran := fakeWindows(t, map[string]string{"schtasks /Create": "SUCCESS"})
	var xmlFile []byte
	recorder := winRun
	winRun = func(argv ...string) (string, error) {
		if i := slices.Index(argv, "/XML"); i > 0 {
			xmlFile, _ = os.ReadFile(argv[i+1])
		}
		return recorder(argv...)
	}
	if err := createTask(`C:\pc\pc-remote-web.exe`); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 {
		t.Fatalf("ran %q", *ran)
	}
	argv := (*ran)[0]
	want := []string{"schtasks", "/Create", "/TN", "PC Remote", "/XML"}
	if len(argv) != 7 || !slices.Equal(argv[:5], want) || argv[6] != "/F" {
		t.Fatalf("argv = %q", argv)
	}
	if slices.Contains(argv, "/RU") || slices.Contains(argv, "SYSTEM") {
		t.Errorf("argv names an account: %q", argv)
	}
	// schtasks reads the file as UTF-16 with a byte order mark.
	if len(xmlFile) < 2 || xmlFile[0] != 0xFF || xmlFile[1] != 0xFE {
		t.Fatalf("XML file isn't UTF-16LE: % x", xmlFile[:min(len(xmlFile), 4)])
	}
	units := make([]uint16, (len(xmlFile)-2)/2)
	for i := range units {
		units[i] = uint16(xmlFile[2+2*i]) | uint16(xmlFile[3+2*i])<<8
	}
	if text := string(utf16.Decode(units)); !strings.Contains(text, `C:\pc\pc-remote-web.exe`) {
		t.Errorf("XML file = %s", text)
	}
	if _, err := os.Stat(argv[5]); !os.IsNotExist(err) {
		t.Errorf("left the XML file behind: %v", err)
	}
}

func TestRestartTask(t *testing.T) {
	ran := fakeWindows(t, map[string]string{"schtasks /Run": "SUCCESS"})
	if err := restartTask("127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"schtasks", "/End", "/TN", "PC Remote"},
		{"stop other copies"},
		{"schtasks", "/Run", "/TN", "PC Remote"},
	}
	if !slices.EqualFunc(*ran, want, slices.Equal) {
		t.Errorf("ran %q", *ran)
	}
}

func TestRestartTaskWaitsForOldServer(t *testing.T) {
	ran := fakeWindows(t, map[string]string{"schtasks /Run": "SUCCESS"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	stopWait = 300 * time.Millisecond
	t.Cleanup(func() { stopWait = 5 * time.Second })
	if err := restartTask(l.Addr().String()); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("err = %v", err)
	}
	for _, argv := range *ran {
		if slices.Contains(argv, "/Run") {
			t.Errorf("started a second server while the old one answers")
		}
	}
}

func TestUTF16Text(t *testing.T) {
	var b []byte
	for _, u := range utf16.Encode([]rune("\ufeff<Arguments>C:\\Çağ\\x.exe</Arguments>")) {
		b = append(b, byte(u), byte(u>>8))
	}
	if got := utf16Text(string(b)); got != `<Arguments>C:\Çağ\x.exe</Arguments>` {
		t.Errorf("utf16Text = %q", got)
	}
	if got := utf16Text("plain"); got != "plain" {
		t.Errorf("utf16Text = %q", got)
	}
}

func TestFirewallRuleArgv(t *testing.T) {
	got := addFirewallRuleArgv(8377)
	want := []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=PC-Remote-8377",
		"dir=in", "action=allow", "protocol=TCP", "localport=8377", "profile=private", "remoteip=localsubnet"}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %q", got)
	}
}

func TestFirewallCommand(t *testing.T) {
	fakeWindows(t, nil)
	exe, params, err := firewallCommand([]int{8377, 8378})
	if err != nil {
		t.Fatal(err)
	}
	netsh := `C:\Windows\system32\netsh.exe`
	if exe != `C:\Windows\system32\cmd.exe` || params != "/c "+netsh+" advfirewall firewall add rule name=PC-Remote-8377 dir=in action=allow protocol=TCP localport=8377 profile=private remoteip=localsubnet && "+
		netsh+" advfirewall firewall add rule name=PC-Remote-8378 dir=in action=allow protocol=TCP localport=8378 profile=private remoteip=localsubnet" {
		t.Errorf("elevates %s %s", exe, params)
	}
	systemDir = func() (string, error) { return `C:\Win & dows`, nil }
	if _, _, err := firewallCommand([]int{8377}); err == nil {
		t.Error("ran from a path cmd would split")
	}
}

func TestFirewallOpenPorts(t *testing.T) {
	fakeWindows(t, map[string]string{"netsh advfirewall firewall show rule name=PC-Remote-8377": "Rule Name: PC-Remote-8377"})
	open := firewallOpenPorts([]int{8377, 8378})
	if !open[8377] || open[8378] {
		t.Errorf("open = %v", open)
	}
}

func TestPlanWindows(t *testing.T) {
	ran := fakeWindows(t, nil)
	s := testSystem(t)
	s.Executable = `C:\pc\pc-remote-web.exe`
	s.Firewall = true
	s.ServiceUnit = false
	a := answers{Access: "lan", Party: true, OpenFirewall: true, Install: true}
	actions := plan(s, a)
	got := labels(actions)
	if len(got) != 4 || !strings.HasPrefix(got[0], "Let devices on 192.168.1.0/24 reach ports 8377 and 8378") ||
		!strings.Contains(got[2], "sign in") || !strings.HasPrefix(got[3], "Start") {
		t.Fatalf("plan = %q", got)
	}
	for i, act := range actions {
		if act.Sudo != (i == 0) {
			t.Errorf("%q: Sudo = %v", act.Label, act.Sudo)
		}
	}
	// One permission prompt opens both ports.
	if err := actions[0].Run(); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || (*ran)[0][0] != "runas" || (*ran)[0][1] != `C:\Windows\system32\cmd.exe` ||
		strings.Count((*ran)[0][2], "localport=") != 2 {
		t.Errorf("ran %q", *ran)
	}

	s.FirewallOpen = map[int]bool{8377: true, 8378: true}
	s.ServiceUnit = true
	if got := labels(plan(s, a)); len(got) != 2 {
		t.Errorf("set-up plan = %q", got)
	}
}

func TestElevationRefused(t *testing.T) {
	fakeWindows(t, nil)
	elevate = func(string, string) error { return errElevationCancelled }
	s := testSystem(t)
	err := firewallActions(s, []int{8377})[0].Run()
	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Errorf("err = %v", err)
	}
}

func TestTaskInstalled(t *testing.T) {
	exe := `C:\pc\pc-remote-web.exe`
	fakeWindows(t, map[string]string{"schtasks /Query": "<Arguments>--headless \"" + strings.ToUpper(exe) + "\"</Arguments>"})
	if !taskInstalled(exe) {
		t.Error("task for this exe not found")
	}
	fakeWindows(t, map[string]string{"schtasks /Query": `<Arguments>--headless &#34;C:\A&amp;B\pc-remote-web.exe&#34;</Arguments>`})
	if !taskInstalled(`C:\A&B\pc-remote-web.exe`) {
		t.Error("task for an exe path with & not found")
	}
	if taskInstalled(`D:\other\pc-remote-web.exe`) {
		t.Error("task for another exe counts")
	}
	fakeWindows(t, nil)
	if taskInstalled(exe) {
		t.Error("missing task counts")
	}
}

func TestNetworkCategory(t *testing.T) {
	ran := fakeWindows(t, map[string]string{"powershell": "Public\r\n"})
	if got, err := NetworkCategory("192.168.1.42"); err != nil || got != "Public" {
		t.Errorf("networkCategory = %q, %v", got, err)
	}
	script := func() string { argv := (*ran)[len(*ran)-1]; return argv[len(argv)-1] }
	if !strings.Contains(script(), "'192.168.1.42'") {
		t.Errorf("script = %s", script())
	}
	NetworkCategory("1.2.3.4'; Remove-Item x; '")
	if strings.Contains(script(), "Remove-Item") {
		t.Errorf("address not checked: %s", script())
	}
}

func TestDoctorWindows(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("AppData", appData)
	t.Setenv("PC_REMOTE_ADDR", "127.0.0.1:1")
	fakeRelease(t, "v0.0.1", nil, nil)
	os.MkdirAll(filepath.Join(appData, "pc-remote"), 0o700)
	fakeWindows(t, map[string]string{"powershell": "Public\r\n"})

	var out strings.Builder
	if code := runDoctor(&out); code != 1 {
		t.Errorf("exit %d", code)
	}
	text := out.String()
	for _, want := range []string{"Keyboard and trackpad", "Logon task", "pc-remote-web setup", "Public", "Firewall", "Answering"} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, text)
		}
	}
	for _, never := range []string{"systemctl", "journalctl", "sudo", "ufw", "Session helper"} {
		if strings.Contains(text, never) {
			t.Errorf("doctor output has %q:\n%s", never, text)
		}
	}
}

func TestUsageWindows(t *testing.T) {
	var out strings.Builder
	Run([]string{"help"}, &out)
	if !strings.Contains(out.String(), "pc-remote-web setup") || strings.Contains(out.String(), "Hyprland") {
		t.Errorf("usage = %s", out.String())
	}
}

// Setup installs what's missing with winget, by its full path, one package
// at a time, so the driver's installer can ask for permission itself.
func TestInstallPackagesWithWinget(t *testing.T) {
	defer func(old func(string, ...string) error) { runInteractive = old }(runInteractive)
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	var ran [][]string
	runInteractive = func(exe string, args ...string) error {
		ran = append(ran, append([]string{exe}, args...))
		return nil
	}

	err := installPackages([]string{input.ViGEmBusPackage})()
	if err == nil || !strings.Contains(err.Error(), input.ViGEmBusURL) || len(ran) != 0 {
		t.Fatalf("without winget: err %v, ran %q; want the download link and nothing run", err, ran)
	}

	winget := filepath.Join(local, "Microsoft", "WindowsApps", "winget.exe")
	os.MkdirAll(filepath.Dir(winget), 0o700)
	os.WriteFile(winget, nil, 0o600)
	if err := installPackages([]string{input.ViGEmBusPackage})(); err != nil {
		t.Fatal(err)
	}
	want := []string{winget, "install", "--exact", "--id", input.ViGEmBusPackage, "--silent",
		"--accept-package-agreements", "--accept-source-agreements"}
	if len(ran) != 1 || !slices.Equal(ran[0], want) {
		t.Fatalf("ran %q, want %q", ran, want)
	}

	runInteractive = func(string, ...string) error { return errors.New("exit status 1") }
	if err := installPackages([]string{input.ViGEmBusPackage})(); err == nil || !strings.Contains(err.Error(), input.ViGEmBusPackage) {
		t.Fatalf("a failed install: err = %v", err)
	}
}

func TestInstallCommandText(t *testing.T) {
	if got, want := installHint([]string{input.ViGEmBusPackage}), "winget install --exact --id ViGEm.ViGEmBus"; got != want {
		t.Errorf("installHint = %q, want %q", got, want)
	}
}

// Tailscale's installer puts it in Program Files; a console opened before
// that doesn't have it on PATH.
func TestTailscaleToolFallsBackToProgramFiles(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	programs := t.TempDir()
	t.Setenv("ProgramFiles", programs)
	if tool, ok := tailscaleTool(); ok {
		t.Fatalf("found %q with no Tailscale", tool)
	}
	exe := filepath.Join(programs, "Tailscale", "tailscale.exe")
	os.MkdirAll(filepath.Dir(exe), 0o700)
	os.WriteFile(exe, nil, 0o600)
	if tool, ok := tailscaleTool(); !ok || tool != exe {
		t.Fatalf("tailscaleTool = %q, %v; want %q", tool, ok, exe)
	}
}
