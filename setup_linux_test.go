//go:build linux

package main

import (
	"os"
	"path/filepath"
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
