package config

import (
	"testing"
)

func TestChassisName(t *testing.T) {
	for code, want := range map[string]string{"3": "Desktop", "10": "Laptop", "31": "Laptop", "1": "PC", "": "PC"} {
		if got := chassisName(code); got != want {
			t.Errorf("chassisName(%q) = %q, want %q", code, got, want)
		}
	}
}
