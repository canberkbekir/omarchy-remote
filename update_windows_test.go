package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeInstall points the updater at a fake installed copy in a temp folder,
// running version v0.1.0, with a server listening at addr ("" for none).
func fakeInstall(t *testing.T, addr string) string {
	t.Helper()
	t.Setenv("PC_REMOTE_ADDR", "") // set in CI; it would win over the fake config
	dir := t.TempDir()
	exe := filepath.Join(dir, "pc-remote-web.exe")
	os.WriteFile(exe, []byte("old server"), 0o755)
	os.WriteFile(filepath.Join(dir, "ViGEmClient.dll"), []byte("old dll"), 0o644)

	oldVersion, oldExe, oldConfig := version, executable, updateConfig
	version = "v0.1.0"
	executable = func() (string, error) { return exe, nil }
	if addr == "" {
		addr = "127.0.0.1:1"
	}
	updateConfig = func() (config, error) { return config{Listen: addr}, nil }
	t.Cleanup(func() { version, executable, updateConfig = oldVersion, oldExe, oldConfig })
	return dir
}

var newRelease = map[string]string{
	"pc-remote-web.exe":       "new server",
	"ViGEmClient.dll":         "new dll",
	"ViGEmClient-LICENSE.txt": "MIT",
	"README.md":               "docs",
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWindowsUpdateRestartsRunningTask(t *testing.T) {
	ran := fakeWindows(t, map[string]string{"schtasks /Run": "SUCCESS"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	dir := fakeInstall(t, l.Addr().String())
	stopServers = func() error {
		*ran = append(*ran, []string{"stop other copies"})
		// The old server must be gone before its files are moved.
		if got := readFile(t, filepath.Join(dir, "pc-remote-web.exe")); got != "old server" {
			t.Errorf("swapped the server before stopping it: %q", got)
		}
		l.Close()
		return nil
	}
	fakeRelease(t, "v9.9.9", newRelease, goodSums)

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	want := [][]string{
		{"schtasks", "/End", "/TN", "PC Remote"},
		{"stop other copies"},
		{"schtasks", "/Run", "/TN", "PC Remote"},
	}
	if !slices.EqualFunc(*ran, want, slices.Equal) {
		t.Errorf("ran %q", *ran)
	}
	for file, want := range map[string]string{
		"pc-remote-web.exe":     "new server",
		"pc-remote-web.exe.old": "old server",
		"ViGEmClient.dll":       "new dll",
		"ViGEmClient.dll.old":   "old dll",
	} {
		if got := readFile(t, filepath.Join(dir, file)); got != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
	for _, file := range []string{"README.md", "ViGEmClient-LICENSE.txt", "pc-remote-web.exe.new"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			t.Errorf("installed %s", file)
		}
	}
	if !strings.Contains(out.String(), "v0.1.0 → v9.9.9") || !strings.Contains(out.String(), "Restarted") {
		t.Errorf("output: %s", out.String())
	}
}

func TestWindowsUpdateLeavesStoppedTaskStopped(t *testing.T) {
	ran := fakeWindows(t, nil)
	dir := fakeInstall(t, "")
	fakeRelease(t, "v9.9.9", newRelease, goodSums)

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if len(*ran) != 0 {
		t.Errorf("ran %q, want nothing", *ran)
	}
	if got := readFile(t, filepath.Join(dir, "pc-remote-web.exe")); got != "new server" {
		t.Errorf("server = %q", got)
	}
}

func TestWindowsUpdateRollsBackOnFailure(t *testing.T) {
	fakeWindows(t, nil)
	dir := fakeInstall(t, "")
	// A leftover .old that can't be removed stops the second swap.
	os.MkdirAll(filepath.Join(dir, "ViGEmClient.dll.old", "busy"), 0o755)
	fakeRelease(t, "v9.9.9", newRelease, goodSums)

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 1 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	for file, want := range map[string]string{"pc-remote-web.exe": "old server", "ViGEmClient.dll": "old dll"} {
		if got := readFile(t, filepath.Join(dir, file)); got != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
	for _, file := range []string{"pc-remote-web.exe.old", "pc-remote-web.exe.new", "ViGEmClient.dll.new"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			t.Errorf("left %s behind", file)
		}
	}
}

func TestWindowsUpdateRejectsBadChecksum(t *testing.T) {
	ran := fakeWindows(t, nil)
	dir := fakeInstall(t, "")
	fakeRelease(t, "v9.9.9", newRelease, func([]byte) string {
		return strings.Repeat("ab", 32) + "  pc-remote-windows-amd64.zip\n"
	})

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 1 || !strings.Contains(out.String(), "checksum") {
		t.Errorf("code %d: %s", code, out.String())
	}
	if got := readFile(t, filepath.Join(dir, "pc-remote-web.exe")); got != "old server" {
		t.Errorf("server = %q", got)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %q", *ran)
	}
}

func TestWindowsUpdateLeavesWingetCopiesToWinget(t *testing.T) {
	fakeWindows(t, nil)
	fakeInstall(t, "")
	exe := filepath.Join(t.TempDir(), "Microsoft", "WinGet", "Packages", "canberkbekir.OmarchyRemote_Microsoft.Winget.Source_8wekyb3d8bbwe", "pc-remote-web.exe")
	executable = func() (string, error) { return exe, nil }
	fakeRelease(t, "v9.9.9", newRelease, goodSums)

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 1 || !strings.Contains(out.String(), "winget upgrade") {
		t.Errorf("code %d: %s", code, out.String())
	}
}

func TestRemoveOldFiles(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []string{"pc-remote-web.exe", "pc-remote-web.exe.old", "ViGEmClient.dll.old", "notes.old"} {
		os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o644)
	}
	removeOldFiles(filepath.Join(dir, "pc-remote-web.exe"))
	for file, kept := range map[string]bool{"pc-remote-web.exe": true, "pc-remote-web.exe.old": false, "ViGEmClient.dll.old": false, "notes.old": true} {
		if _, err := os.Stat(filepath.Join(dir, file)); (err == nil) != kept {
			t.Errorf("%s kept = %v, want %v", file, err == nil, kept)
		}
	}
}

func TestWindowsUpdateRestartsTaskWhenStopFails(t *testing.T) {
	ran := fakeWindows(t, map[string]string{"schtasks /Run": "SUCCESS"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	dir := fakeInstall(t, l.Addr().String())
	stopWait = 300 * time.Millisecond
	t.Cleanup(func() { stopWait = 5 * time.Second })
	fakeRelease(t, "v9.9.9", newRelease, goodSums)

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 1 || !strings.Contains(out.String(), "still running") {
		t.Errorf("code %d: %s", code, out.String())
	}
	if last := (*ran)[len(*ran)-1]; !slices.Contains(last, "/Run") {
		t.Errorf("ran %q, want the task started again", *ran)
	}
	if got := readFile(t, filepath.Join(dir, "pc-remote-web.exe")); got != "old server" {
		t.Errorf("server = %q", got)
	}
}

func TestWindowsUpdateWarnsWithoutSettings(t *testing.T) {
	fakeWindows(t, nil)
	fakeInstall(t, "")
	updateConfig = func() (config, error) { return config{}, errors.New("broken config") }
	fakeRelease(t, "v9.9.9", newRelease, goodSums)

	var out bytes.Buffer
	if code := runUpdate(nil, &out); code != 0 || !strings.Contains(out.String(), "broken config") || !strings.Contains(out.String(), "schtasks") {
		t.Errorf("code %d: %s", code, out.String())
	}
}

func TestPackageManagedCoversBothWingetScopes(t *testing.T) {
	for exe, managed := range map[string]bool{
		`C:\Users\a\AppData\Local\Microsoft\WinGet\Packages\x\pc-remote-web.exe`: true,
		`C:\Program Files\WinGet\Packages\x\pc-remote-web.exe`:                   true,
		`C:\Tools\pc-remote\pc-remote-web.exe`:                                   false,
	} {
		if got := packageManaged(exe) != ""; got != managed {
			t.Errorf("packageManaged(%s) = %v", exe, got)
		}
	}
}
