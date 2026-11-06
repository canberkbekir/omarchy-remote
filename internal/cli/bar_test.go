package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
