package main

import (
	"encoding/json"
	"runtime"
	"strconv"
	"testing"
)

// The phone reads these names, so the old fields must keep their exact JSON
// and features only adds a field at the end.
func TestStatusJSON(t *testing.T) {
	s := status{
		Name: "pc", Theme: "t1", Volume: 40, Muted: true, Media: true,
		Nightlight: true, Output: "Speakers", Features: platformFeatures(),
	}
	got, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	const before = `{"name":"pc","theme":"t1","volume":40,"muted":true,"media":true,"playing":false,"nightlight":true,"nowPlaying":null,"output":"Speakers","zooming":false`
	want := map[string]string{
		"linux": before + `,"features":{"power":true,"screens":true,"nightlight":true,"brightness":true,"outputs":true,"nowPlaying":true,"gamepad":true,"zoom":true,"party":true}}`,
		"windows": before + `,"features":{"power":true,"screens":true,"nightlight":false,"brightness":false,"outputs":` +
			strconv.FormatBool(s.Features.Outputs) + `,"nowPlaying":false,"gamepad":false,"zoom":false,"party":false}}`,
	}[runtime.GOOS]
	if string(got) != want {
		t.Errorf("status JSON\n got %s\nwant %s", got, want)
	}
}
