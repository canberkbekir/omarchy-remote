package main

import (
	"strings"
	"testing"
)

// The list reads the real endpoints, so it is only checked when the PC has
// some. Switching between two of them is checked end to end in the VM.
func TestListOutputsWindows(t *testing.T) {
	outputs, err := listOutputs()
	if err != nil {
		t.Fatalf("listOutputs: %v", err)
	}
	if len(outputs) == 0 {
		t.Skip("no audio endpoint on this machine")
	}
	active := 0
	for _, o := range outputs {
		if !strings.HasPrefix(o.Name, "{") || o.Label == "" {
			t.Errorf("output %+v: want an endpoint id and a name", o)
		}
		if o.Active {
			active++
		}
	}
	if active > 1 {
		t.Errorf("%d active outputs, want at most 1", active)
	}
}

func TestSetOutputUnknownWindows(t *testing.T) {
	if !platformFeatures().Outputs {
		t.Skip("output switching is not available on this machine")
	}
	if err := setOutput("{0.0.0.00000000}.{00000000-0000-0000-0000-000000000000}"); err == nil {
		t.Error("setOutput with an unknown id succeeded")
	}
}
