package main

import "testing"

func TestOutputLabel(t *testing.T) {
	cases := map[string]string{
		"AD104 High Definition Audio Controller Digital Stereo (HDMI) [VG249Q3R]": "VG249Q3R",
		"Sound Blaster Play! 3 Analog Stereo":                                     "Sound Blaster Play! 3",
		"Ryzen HD Audio Controller Analog Stereo":                                 "PC audio jack",
	}
	for description, want := range cases {
		if got := outputLabel(description); got != want {
			t.Errorf("outputLabel(%q) = %q, want %q", description, got, want)
		}
	}
}
