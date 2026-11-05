//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBarPlugin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), barPluginID)
	changed, err := writeBarPlugin(dir, "/home/me/it's/pc-remote-web")
	if err != nil || !changed {
		t.Fatalf("first write: changed %v, %v", changed, err)
	}
	for _, name := range []string{"manifest.json", "Widget.qml", "Card.qml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
	var manifest struct{ ID string }
	data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.ID != barPluginID {
		t.Errorf("manifest id = %q, %v", manifest.ID, err)
	}
	helper, _ := os.ReadFile(filepath.Join(dir, "pc-remote.sh"))
	if !strings.HasSuffix(string(helper), "exec '/home/me/it'\\''s/pc-remote-web' \"$@\"\n") {
		t.Errorf("helper = %q", helper)
	}
	if info, _ := os.Stat(filepath.Join(dir, "pc-remote.sh")); info.Mode().Perm()&0o100 == 0 {
		t.Error("helper isn't executable")
	}

	if changed, _ := writeBarPlugin(dir, "/home/me/it's/pc-remote-web"); changed {
		t.Error("rewrote files that hadn't changed")
	}
	if changed, _ := writeBarPlugin(dir, "/usr/bin/pc-remote-web"); !changed {
		t.Error("kept the old program's path")
	}
}
