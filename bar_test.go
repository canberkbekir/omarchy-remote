package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPresence(t *testing.T) {
	a := &app{party: newParty()}
	ask := func() presence {
		w := httptest.NewRecorder()
		a.handlePresence(w, httptest.NewRequest("GET", "/api/presence", nil))
		var p presence
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if ask().Phone {
		t.Error("a phone before any status poll")
	}
	a.phoneSeen.Store(time.Now().UnixNano())
	if !ask().Phone {
		t.Error("no phone right after a status poll")
	}
	a.phoneSeen.Store(time.Now().Add(-phoneTimeout - time.Second).UnixNano())
	if ask().Phone {
		t.Error("a phone long after the last status poll")
	}
}

func TestBarStep(t *testing.T) {
	s := testSystem(t)
	if a := defaultAnswers(s); a.Bar || slices.Contains(newWizard(s, a).steps(), stageBar) {
		t.Error("offered the bar without the Omarchy shell")
	}
	barLabel := func(l string) bool { return strings.Contains(l, "icon") }

	s.BarPluginDir = filepath.Join(t.TempDir(), barPluginID)
	a := defaultAnswers(s)
	if !a.Bar || !slices.Contains(newWizard(s, a).steps(), stageBar) {
		t.Error("a new setup doesn't offer the icon")
	}
	if got := labels(plan(s, a)); !slices.Contains(got, "Add the Omarchy Remote icon to the bar") {
		t.Errorf("plan = %q", got)
	}

	s.HasConfig = true
	if defaultAnswers(s).Bar {
		t.Error("added back for someone who said no")
	}
	s.BarPlugin = true
	if got := labels(plan(s, defaultAnswers(s))); slices.ContainsFunc(got, barLabel) {
		t.Errorf("icon already there, plan = %q", got)
	}
	a.Bar = false
	if got := labels(plan(s, a)); !slices.Contains(got, "Take the Omarchy Remote icon off the bar") {
		t.Errorf("plan = %q", got)
	}
}

func TestPresenceMatchesHandler(t *testing.T) {
	a := &app{party: newParty()}
	a.phoneSeen.Store(time.Now().UnixNano())
	if p := a.presence(); !p.Phone || p.Party.Active {
		t.Errorf("presence() = %+v", p)
	}
}
