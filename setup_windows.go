package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows side of setup. pc-remote-web.exe is the only program: it runs
// the server and the subcommands. A Task Scheduler logon task starts the
// server in the signed-in user's session (a service runs in session 0, where
// SendInput reaches no desktop). Only the firewall rules need an
// administrator, so only that step asks Windows for permission. It elevates
// cmd.exe and netsh.exe from System32, never this program: this program may
// sit in a folder any of the user's programs can write to.

const (
	cliName             = "pc-remote-web"
	adminPrefix         = ""
	elevationNote       = "asks Windows for permission"
	elevateOnceNote     = "Windows asks for permission once, after you review."
	tailscaleNeedsAdmin = false
	configRestartNote   = "# `pc-remote-web setup --yes` to restart it. See the README.\n\n"
	restartHint         = "Restart Omarchy Remote to use it: pc-remote-web setup --yes"
	notRunningProblem   = `Omarchy Remote isn't running. Start it with: schtasks /Run /TN "PC Remote"`

	taskName = "PC Remote"
)

// These run the outside programs; tests swap them for fakes.
var (
	winRun         = runCommand
	elevate        = shellExecuteRunas
	currentUser    = currentUsername
	sendInputWorks = trySendInput
	stopServers    = stopOtherCopies
)

var errElevationCancelled = errors.New("Windows didn't get permission")

func currentUsername() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// trySendInput sends a pointer move of nothing. SendInput fails when the
// program runs where there is no desktop to type into, like a service.
func trySendInput() bool {
	var m mouseInput
	m.mi.flags = mouseeventfMove
	return sendMouse(m) == nil
}

// commandError adds what the program printed to its error.
func commandError(err error, out string) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		out += string(exit.Stderr)
	}
	if out = strings.TrimSpace(out); out != "" {
		return fmt.Errorf("%v: %s", err, out)
	}
	return err
}

func installPackages([]string) func() error {
	return func() error { return errors.New("there's nothing to install on Windows") }
}

func firewallRuleName(port int) string { return "PC-Remote-" + strconv.Itoa(port) }

// addFirewallRuleArgv lets devices on the local subnet reach port, only while
// Windows calls the network Private.
func addFirewallRuleArgv(port int) []string {
	return []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=" + firewallRuleName(port),
		"dir=in", "action=allow", "protocol=TCP", "localport=" + strconv.Itoa(port), "profile=private", "remoteip=localsubnet"}
}

// firewallOpenPorts reports which ports already have the rule setup adds.
func firewallOpenPorts(ports []int) map[int]bool {
	open := map[int]bool{}
	for _, p := range ports {
		_, err := winRun("netsh", "advfirewall", "firewall", "show", "rule", "name="+firewallRuleName(p))
		open[p] = err == nil
	}
	return open
}

func portList(ports []int) string {
	var s []string
	for _, p := range ports {
		s = append(s, strconv.Itoa(p))
	}
	if len(s) == 1 {
		return "port " + s[0]
	}
	return "ports " + strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// systemDir is System32, from Windows rather than PATH or the environment.
var systemDir = func() (string, error) { return windows.GetSystemDirectory() }

// firewallCommand is the elevated command line: cmd.exe /c with one netsh
// per port, so Windows asks once. The ports are numbers and the paths come
// from Windows, so nothing in it needs quoting.
func firewallCommand(ports []int) (exe, params string, err error) {
	dir, err := systemDir()
	if err != nil {
		return "", "", err
	}
	if strings.ContainsAny(dir, " \"&^%|<>()") {
		return "", "", fmt.Errorf("can't run netsh from %s", dir)
	}
	var rules []string
	for _, p := range ports {
		argv := addFirewallRuleArgv(p)
		argv[0] = filepath.Join(dir, "netsh.exe")
		rules = append(rules, strings.Join(argv, " "))
	}
	return filepath.Join(dir, "cmd.exe"), "/c " + strings.Join(rules, " && "), nil
}

// firewallActions opens every blocked port in one elevated run.
func firewallActions(s system, blocked []int) []action {
	from := s.LANSubnet
	if from == "" {
		from = "your home network"
	}
	return []action{{
		Label: fmt.Sprintf("Let devices on %s reach %s on private networks", from, portList(blocked)),
		Sudo:  true,
		Run: func() error {
			exe, params, err := firewallCommand(blocked)
			if err != nil {
				return err
			}
			err = elevate(exe, params)
			if errors.Is(err, errElevationCancelled) {
				return fmt.Errorf("%w, so the firewall wasn't changed", err)
			}
			return err
		},
	}}
}

// firewallHint is what to run to open the ports by hand.
func firewallHint(_ string, ports []int) string {
	var rules []string
	for _, p := range ports {
		rules = append(rules, strings.Join(addFirewallRuleArgv(p), " "))
	}
	return "in a terminal run as administrator: " + strings.Join(rules, "; ")
}

func firewallExplain(_ string, ports []int) string {
	return "Windows Firewall blocks incoming connections unless a rule allows them. This adds a rule for " + portList(ports) +
		" that lets in devices on your local network only, and only while Windows calls the network Private."
}

// platformSetup: Windows has no setup-only subcommands.
func platformSetup([]string, io.Writer) (int, bool) { return 0, false }

const (
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
	swHide                = 0
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	icon       uintptr
	process    windows.Handle
}

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteRunas runs exe as administrator, which shows the Windows
// permission prompt, and waits for it to finish.
func shellExecuteRunas(exe, params string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	args, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return err
	}
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess | seeMaskNoAsync, verb: verb, file: file, parameters: args, show: swHide}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if err == windows.ERROR_CANCELLED {
			return errElevationCancelled
		}
		return err
	}
	if info.process == 0 {
		return errors.New("Windows didn't start the elevated step")
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("the elevated step failed (exit %d)", code)
	}
	return nil
}

func serveTailscale(port int) func() error {
	return func() error {
		cmd := exec.Command("tailscale", "serve", "--bg", strconv.Itoa(port))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
}

// taskXML describes the logon task: it starts when this user signs in, runs
// in their session with their normal rights, and keeps running (schtasks
// would stop it after 72 hours by default). conhost --headless runs the
// console program without a window.
func taskXML(username, exe string) string {
	esc := xmlEscape
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Starts Omarchy Remote when you sign in. Added by pc-remote-web setup.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(username) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(username) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>4</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%SystemRoot%\System32\conhost.exe</Command>
      <Arguments>--headless ` + esc(`"`+exe+`"`) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// createTask adds or replaces the logon task. schtasks reads the XML as
// UTF-16 with a byte order mark.
func createTask(exe string) error {
	if exe == "" {
		return errors.New("can't find this program's path")
	}
	username := currentUser()
	if username == "" {
		return errors.New("can't tell who is signed in")
	}
	var data bytes.Buffer
	data.Write([]byte{0xFF, 0xFE})
	binary.Write(&data, binary.LittleEndian, utf16.Encode([]rune(taskXML(username, exe))))
	f, err := os.CreateTemp("", "pc-remote-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data.Bytes())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if out, err := winRun("schtasks", "/Create", "/TN", taskName, "/XML", f.Name(), "/F"); err != nil {
		return commandError(err, out)
	}
	return nil
}

// taskInstalled reports whether the logon task exists and starts this program.
func taskInstalled(exe string) bool {
	out, err := winRun("schtasks", "/Query", "/TN", taskName, "/XML")
	if err != nil || exe == "" {
		return false
	}
	out = strings.ToLower(utf16Text(out))
	return strings.Contains(out, strings.ToLower(exe)) || strings.Contains(out, strings.ToLower(xmlEscape(exe)))
}

// utf16Text decodes output that schtasks wrote as UTF-16.
func utf16Text(s string) string {
	b := []byte(s)
	if len(b) < 2 || len(b)%2 != 0 || !strings.Contains(s, "\x00") {
		return s
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimPrefix(string(utf16.Decode(units)), "\ufeff")
}

func autostartActions(s system) []action {
	var actions []action
	if !s.ServiceUnit {
		actions = append(actions, action{
			Label: "Start Omarchy Remote whenever you sign in, with a Task Scheduler task",
			Run:   func() error { return createTask(s.Executable) },
		})
	}
	return append(actions, action{
		Label: "Start Omarchy Remote now",
		Run:   func() error { return restartTask(s.Existing.listenAddr()) },
	})
}

// stopWait is how long restartTask waits for the old server to let go of its port.
var stopWait = 5 * time.Second

// restartTask stops the task and the server it started, waits until the old
// server at addr stops answering, and starts the task again, so the server
// reads the new settings. /End only stops conhost, not the server under it.
func restartTask(addr string) error {
	winRun("schtasks", "/End", "/TN", taskName) // fails when it isn't running
	if err := stopServers(); err != nil {
		return err
	}
	for deadline := time.Now().Add(stopWait); answering(addr); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("another Omarchy Remote is still running on %s. Close it, then run %s setup again", addr, cliName)
		}
	}
	if out, err := winRun("schtasks", "/Run", "/TN", taskName); err != nil {
		return commandError(err, out)
	}
	return nil
}

// stopOtherCopies ends every other process running this same program file:
// the server the task started, and any copy started by hand. Copies from
// other folders are left alone.
func stopOtherCopies() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == uint32(os.Getpid()) || !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(self)) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, entry.ProcessID)
		if err != nil {
			continue // another user's, or already gone
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil && strings.EqualFold(windows.UTF16ToString(buf[:size]), self) {
			windows.TerminateProcess(h, 0)
		}
		windows.CloseHandle(h)
	}
	return nil
}

// restartService restarts a running server, like systemctl try-restart: one
// that isn't running stays stopped.
func restartService() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	c, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if !serviceRunning(c) {
		return nil
	}
	return restartTask(c.listenAddr())
}

// serviceRunning asks the port, since the task may be running a server that
// failed to start.
func serviceRunning(c config) bool { return answering(c.listenAddr()) }

func answering(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// serviceFailed: Task Scheduler doesn't say quickly, so setup waits it out.
func serviceFailed() bool { return false }

func startFailure() error {
	exe, _ := os.Executable()
	return fmt.Errorf("it didn't start. Run %s in a terminal to see why", exe)
}

// prepareConsole turns on escape codes in the console, for colors and the
// QR code.
func prepareConsole() {
	for _, h := range []windows.Handle{windows.Stdout, windows.Stderr} {
		var mode uint32
		if windows.GetConsoleMode(h, &mode) == nil {
			windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	}
}
