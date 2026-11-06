package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uninstallFixture(t *testing.T, reply map[string]string) (*[][]string, string) {
	t.Helper()
	ran := fakeWindows(t, reply)
	t.Setenv("APPDATA", t.TempDir())
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "passcode"), []byte("1234-5678\n"), 0o600)
	return ran, dir
}

func ranLine(ran [][]string, prefix string) string {
	for _, argv := range ran {
		if line := strings.Join(argv, " "); strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// Uninstall stops the server and takes away what setup added, in one
// permission prompt, and keeps the settings unless asked.
func TestUninstall(t *testing.T) {
	ran, dir := uninstallFixture(t, map[string]string{
		"schtasks /Query":  "PC Remote",
		"schtasks /Delete": "SUCCESS",
		"netsh advfirewall firewall show rule name=PC-Remote-8377": "Rule Name: PC-Remote-8377",
	})
	var out strings.Builder
	code, handled := runCommandLine([]string{"uninstall"}, &out)
	if !handled || code != 0 {
		t.Fatalf("uninstall = %d, %v\n%s", code, handled, out.String())
	}
	for _, want := range []string{"schtasks /End /TN PC Remote", "stop other copies", "schtasks /Delete /TN PC Remote /F"} {
		if ranLine(*ran, want) == "" {
			t.Errorf("didn't run %q; ran %q", want, *ran)
		}
	}
	runas := ranLine(*ran, "runas")
	if !strings.Contains(runas, "delete rule name=PC-Remote-8377") || strings.Contains(runas, "8378") {
		t.Errorf("elevated %q, want only the 8377 rule deleted", runas)
	}
	if _, err := os.Stat(filepath.Join(dir, "passcode")); err != nil {
		t.Errorf("the settings went without --purge: %v", err)
	}
	if !strings.Contains(out.String(), "ViGEmBus") {
		t.Errorf("output doesn't say the driver stays:\n%s", out.String())
	}
}

func TestUninstallPurge(t *testing.T) {
	ran, dir := uninstallFixture(t, nil) // no task, no rules
	var out strings.Builder
	if code, _ := runCommandLine([]string{"uninstall", "--purge"}, &out); code != 0 {
		t.Fatalf("uninstall --purge = %d\n%s", code, out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("settings folder still there: %v", err)
	}
	if ranLine(*ran, "runas") != "" || ranLine(*ran, "schtasks /Delete") != "" {
		t.Errorf("removed things that weren't there: %q", *ran)
	}
}

func TestUninstallElevationRefused(t *testing.T) {
	_, _ = uninstallFixture(t, map[string]string{
		"netsh advfirewall firewall show rule name=PC-Remote-8378": "Rule Name: PC-Remote-8378",
	})
	elevate = func(string, string) error { return errElevationCancelled }
	var out strings.Builder
	if code, _ := runCommandLine([]string{"uninstall"}, &out); code != 1 {
		t.Fatalf("uninstall = %d, want 1\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "PC-Remote-8378") {
		t.Errorf("doesn't say which rule stayed:\n%s", out.String())
	}
}

func TestUninstallUsage(t *testing.T) {
	var out strings.Builder
	if code, _ := runCommandLine([]string{"uninstall", "--bogus"}, &out); code != 2 {
		t.Fatalf("bad flag = %d, want 2", code)
	}
	out.Reset()
	runCommandLine([]string{"help"}, &out)
	if !strings.Contains(out.String(), "pc-remote-web uninstall") {
		t.Errorf("usage lacks uninstall:\n%s", out.String())
	}
}

func TestFirewallDeleteCommand(t *testing.T) {
	fakeWindows(t, nil)
	exe, params, err := firewallDeleteCommand([]int{8377, 8378})
	if err != nil {
		t.Fatal(err)
	}
	netsh := `C:\Windows\system32\netsh.exe`
	want := "/c " + netsh + " advfirewall firewall delete rule name=PC-Remote-8377 & " + netsh + " advfirewall firewall delete rule name=PC-Remote-8378"
	if exe != `C:\Windows\system32\cmd.exe` || params != want {
		t.Errorf("elevates %s %s\nwant %s", exe, params, want)
	}
}
