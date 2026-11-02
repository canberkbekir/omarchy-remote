package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testSystem(t *testing.T) system {
	t.Helper()
	t.Setenv("PC_REMOTE_ADDR", "")
	dir := t.TempDir()
	c, _ := loadConfig(dir)
	c.Name = "archbox"
	return system{
		Dir: dir, Hostname: "archbox", Existing: c,
		LANAddress: "192.168.1.42", LANSubnet: "192.168.1.0/24",
		FirewallOpen: map[int]bool{}, ServiceUnit: true, Executable: "/usr/bin/pc-remote-web",
	}
}

func TestRenderConfigLoadsBack(t *testing.T) {
	s := testSystem(t)
	s.KeepPort, s.Existing.Port = true, 9000
	a := answers{Name: `Ada's "Den" ☕`, Theme: "omarchy", Access: "tailscale", Party: false}
	dir := writeConfig(t, renderConfig(s, a))
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != a.Name || c.Theme != "omarchy" || c.Access != "tailscale" || c.GuestPort != 0 || c.Port != 9000 {
		t.Errorf("config = %+v", c)
	}
}

func TestRenderConfigLeavesPortOut(t *testing.T) {
	s := testSystem(t)
	text := renderConfig(s, answers{Name: "PC", Theme: "default", Access: "lan", Party: true})
	if strings.Contains(text, "port   =") || strings.Contains(text, "listen") {
		t.Errorf("port or listen written without being set:\n%s", text)
	}
	if !strings.Contains(text, "guest_port = 8378") {
		t.Errorf("guest port missing:\n%s", text)
	}
}

func TestCleanPCName(t *testing.T) {
	for in, want := range map[string]string{
		"  Studio  ":            "Studio",
		"":                      "archbox",
		"a\nb":                  "ab",
		strings.Repeat("x", 50): strings.Repeat("x", 40),
	} {
		if got := cleanPCName(in, "archbox"); got != want {
			t.Errorf("cleanPCName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupPasscode(t *testing.T) {
	if got := groupPasscode("k7m2pq9xra"); got != "k7m2 pq9x ra" {
		t.Errorf("got %q", got)
	}
	if got := groupPasscode("abcd"); got != "abcd" {
		t.Errorf("got %q", got)
	}
}

func TestUFWState(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "ufw.conf")
	rules := filepath.Join(dir, "user.rules")
	os.WriteFile(conf, []byte("# comment\nENABLED=yes\n"), 0o600)
	os.WriteFile(rules, []byte(strings.Join([]string{
		"### tuple ### allow tcp 8377 0.0.0.0/0 any 192.168.0.0/24 in",
		"### tuple ### allow udp 8378 0.0.0.0/0 any 0.0.0.0/0 in",
		"### tuple ### allow any 22 0.0.0.0/0 any 0.0.0.0/0 in",
		"### tuple ### deny tcp 9000 0.0.0.0/0 any 0.0.0.0/0 in",
	}, "\n")), 0o600)

	on, open := ufwState(conf, rules)
	if !on || !open[8377] || open[8378] || !open[22] || open[9000] {
		t.Errorf("on = %v, open = %v", on, open)
	}
	if on, open := ufwState(conf, filepath.Join(dir, "unreadable")); !on || open != nil {
		t.Errorf("unreadable rules: on = %v, open = %v", on, open)
	}
	os.WriteFile(conf, []byte("ENABLED=no\n"), 0o600)
	if on, _ := ufwState(conf, rules); on {
		t.Error("disabled ufw reported as on")
	}
}

func TestServesPort(t *testing.T) {
	status := "https://pc.tail.ts.net (tailnet only)\n|-- / proxy http://127.0.0.1:8377\n"
	if !servesPort(status, 8377) {
		t.Error("8377 not found")
	}
	if servesPort(status, 837) || servesPort(status, 8378) {
		t.Error("wrong port matched")
	}
}

func labels(actions []action) []string {
	var out []string
	for _, a := range actions {
		out = append(out, a.Label)
	}
	return out
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

func key(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func press(w wizard, keys ...string) wizard {
	for _, k := range keys {
		m, _ := w.Update(key(k))
		w = m.(wizard)
	}
	return w
}

func TestWizardWalkthrough(t *testing.T) {
	s := testSystem(t)
	s.Tailscale = true
	w := newWizard(s, defaultAnswers(s))

	w = press(w, "enter") // past the check
	if w.stage != stageConnect {
		t.Fatalf("stage = %v", w.stage)
	}
	w = press(w, "down", "enter") // Home Wi-Fi and Tailscale
	if w.answers.Access != "both" || w.stage != stageName {
		t.Fatalf("access = %q, stage = %v", w.answers.Access, w.stage)
	}
	w = press(w, "enter", "enter", "down", "enter") // keep the name and theme, no party
	if w.answers.Name != "archbox" || w.answers.Party || w.stage != stageReview {
		t.Fatalf("answers = %+v, stage = %v", w.answers, w.stage)
	}
	w = press(w, "esc")
	if w.stage != stageParty {
		t.Fatalf("esc from review went to %v", w.stage)
	}
	w = press(w, "enter", "enter")
	if !w.confirmed {
		t.Error("not confirmed")
	}
}

func TestWizardSkipsUnavailableTailscale(t *testing.T) {
	s := testSystem(t)
	w := newWizard(s, defaultAnswers(s))
	w = press(w, "enter", "down", "enter")
	if w.answers.Access != "lan" {
		t.Errorf("picked %q while Tailscale is off", w.answers.Access)
	}
}

func TestWizardAsksAboutFirewall(t *testing.T) {
	s := testSystem(t)
	s.Firewall = true
	w := newWizard(s, defaultAnswers(s))
	if got := w.steps(); got[len(got)-1] != stageFirewall {
		t.Fatalf("steps = %v", got)
	}
	s.FirewallOpen = map[int]bool{8377: true, 8378: true}
	w = newWizard(s, defaultAnswers(s))
	if slices.Contains(w.steps(), stageFirewall) {
		t.Error("firewall step shown though the ports are open")
	}
}

// The QR code read back from the half blocks matches the code it was drawn from.
func TestQRBlock(t *testing.T) {
	text := "http://192.168.1.42:8377"
	rows := qrRows(text)
	lines := strings.Split(qrBlock(newStyles(io.Discard), text), "\n")
	const quiet = 2
	size := len(rows) + 2*quiet
	if len(lines) != (size+1)/2 {
		t.Fatalf("%d lines for %d modules", len(lines), size)
	}
	for ly, line := range lines {
		for x, r := range []rune(line) {
			top := r == '█' || r == '▀'
			bottom := r == '█' || r == '▄'
			for dy, light := range []bool{top, bottom} {
				y, mx := ly*2+dy-quiet, x-quiet
				if y < 0 || mx < 0 || y >= len(rows) || mx >= len(rows) {
					continue
				}
				if want := rows[y][mx] == '0'; light != want {
					t.Fatalf("module %d,%d light = %v, want %v", mx, y, light, want)
				}
			}
		}
	}
}

func TestBothAccess(t *testing.T) {
	s := testSystem(t)
	s.Firewall = true
	s.TailscaleName = "pc.tail.ts.net"
	a := answers{Name: "PC", Theme: "default", Access: "both", OpenFirewall: true}

	dir := writeConfig(t, renderConfig(s, a))
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.listenAddr(); got != "0.0.0.0:8377" {
		t.Errorf("listenAddr = %q", got)
	}

	got := labels(plan(s, a))
	if len(got) < 2 || got[0] != "Let devices on 192.168.1.0/24 reach port 8377" || !strings.HasPrefix(got[1], "Share the app") {
		t.Errorf("plan = %q", got)
	}

	addresses := appAddresses(s, a)
	if len(addresses) != 2 || addresses[0].url != "https://pc.tail.ts.net" || addresses[1].url != "http://192.168.1.42:8377" {
		t.Errorf("addresses = %v", addresses)
	}
}
