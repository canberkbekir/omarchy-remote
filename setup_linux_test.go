//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSavePasscode(t *testing.T) {
	dir := t.TempDir()
	if err := savePasscode(dir, "abcdef1"); err != nil {
		t.Fatal(err)
	}
	if got, err := loadPasscode(dir); err != nil || got != "abcdef1" {
		t.Errorf("loadPasscode = %q, %v", got, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "passcode"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if savePasscode(dir, "a b c d e") == nil {
		t.Error("saved a passcode with spaces")
	}
}

func TestSetupYesLeavesBrokenConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	path := filepath.Join(home, "pc-remote", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("theme = \n"), 0o600)

	var out strings.Builder
	if code := runSetup([]string{"--yes"}, &out); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if data, _ := os.ReadFile(path); string(data) != "theme = \n" {
		t.Errorf("settings changed to %q", data)
	}
	if !strings.Contains(out.String(), "mistake") {
		t.Errorf("output doesn't explain: %q", out.String())
	}
}

func TestPlan(t *testing.T) {
	s := testSystem(t)
	lan := answers{Access: "lan", Party: true, OpenFirewall: true, Install: true}

	got := labels(plan(s, lan))
	if len(got) != 2 || !strings.HasPrefix(got[0], "Save settings") || !strings.HasPrefix(got[1], "Start") {
		t.Errorf("plain plan = %q", got)
	}

	s.Firewall = true
	s.FirewallOpen[8377] = true
	s.Checks = []check{{Label: "Media", Fix: []string{"playerctl"}}}
	s.ServiceUnit = false
	actions := plan(s, lan)
	got = labels(actions)
	if len(got) != 5 || got[0] != "Install playerctl" || got[1] != "Let devices on 192.168.1.0/24 reach port 8378" ||
		got[3] != "Add the background service" {
		t.Errorf("full plan = %q", got)
	}
	if !actions[0].Sudo || !actions[1].Sudo || actions[2].Sudo {
		t.Error("wrong actions marked as needing a password")
	}

	declined := lan
	declined.Install, declined.OpenFirewall = false, false
	sudoLabel := func(l string) bool { return strings.HasPrefix(l, "Install") || strings.HasPrefix(l, "Let") }
	if got := labels(plan(s, declined)); slices.ContainsFunc(got, sudoLabel) {
		t.Errorf("declined plan = %q", got)
	}

	ts := answers{Access: "tailscale"}
	if got := labels(plan(s, ts)); !strings.HasPrefix(got[0], "Share the app") {
		t.Errorf("tailscale plan = %q", got)
	}
	s.TailscaleServing = true
	if got := labels(plan(s, ts)); strings.HasPrefix(got[0], "Share the app") {
		t.Errorf("tailscale already serving, plan = %q", got)
	}
}
