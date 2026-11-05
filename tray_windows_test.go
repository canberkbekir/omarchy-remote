package main

import (
	"runtime"
	"testing"
	"unsafe"
)

// The hand-written structs must match the Windows headers, or the calls read
// past them or ignore fields.
func TestTrayStructSizes(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("sizes checked for amd64")
	}
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"NOTIFYICONDATAW", unsafe.Sizeof(notifyIconData{}), 976},
		{"WNDCLASSEXW", unsafe.Sizeof(wndClassEx{}), 80},
		{"MSG", unsafe.Sizeof(msg{}), 48},
	} {
		if c.got != c.want {
			t.Errorf("%s is %d bytes, want %d", c.name, c.got, c.want)
		}
	}
}

func TestLoadTrayIcon(t *testing.T) {
	icon, err := loadTrayIcon()
	if err != nil || icon == 0 {
		t.Fatalf("loadTrayIcon() = %v, %v", icon, err)
	}
}

func TestTrayTipFitsTheStruct(t *testing.T) {
	tr := &tray{tip: trayTip(string(make([]rune, 400)), presence{Phone: true})}
	d := tr.data(nifTip)
	if d.Tip[len(d.Tip)-1] != 0 {
		t.Error("tooltip isn't zero-terminated")
	}
}
