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
		Dir: dir, DefaultName: "archbox", Existing: c,
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
	w = press(w, "enter", "enter", "down", "enter", "enter") // keep the name, theme and passcode, no party
	if w.answers.Name != "archbox" || w.answers.Party || w.answers.Passcode != "" || w.stage != stageReview {
		t.Fatalf("answers = %+v, stage = %v", w.answers, w.stage)
	}
	w = press(w, "esc")
	if w.stage != stagePasscode {
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
	if len(got) < 2 || !strings.HasPrefix(got[0], "Let devices on 192.168.1.0/24 reach port 8377") || !strings.HasPrefix(got[1], "Share the app") {
		t.Errorf("plan = %q", got)
	}

	addresses := appAddresses(s, a)
	if len(addresses) != 2 || addresses[0].url != "https://pc.tail.ts.net" || addresses[1].url != "http://192.168.1.42:8377" {
		t.Errorf("addresses = %v", addresses)
	}
}

func TestPasscodeProblem(t *testing.T) {
	for p, ok := range map[string]bool{
		"hunter2":               true,
		"28072000":              true,
		"abc12":                 false,
		"two words":             false,
		strings.Repeat("x", 65): false,
	} {
		if got := passcodeProblem(p) == ""; got != ok {
			t.Errorf("passcodeProblem(%q) accepted = %v, want %v", p, got, ok)
		}
	}
}

func TestWizardPasscodeStep(t *testing.T) {
	s := testSystem(t)
	s.HasPasscode = true
	w := newWizard(s, defaultAnswers(s))
	w = press(w, "enter", "enter", "enter", "enter", "enter") // to the passcode step
	if w.stage != stagePasscode {
		t.Fatalf("stage = %v", w.stage)
	}
	if c := w.choices(); c[0].title != "Keep the current one" || c[1].value != "choose" || len(c) != 2 {
		t.Fatalf("choices = %+v", c)
	}

	w = press(w, "down", "enter")
	if w.stage != stagePasscodeEntry {
		t.Fatalf("stage = %v", w.stage)
	}
	w = press(w, "abc", "enter")
	if w.problem == "" || w.typing != 0 {
		t.Error("a short passcode was accepted")
	}
	w = press(w, "def1", "enter", "abcdef2", "enter")
	if w.problem == "" || w.answers.Passcode != "" {
		t.Error("passcodes that don't match were accepted")
	}
	w = press(w, "abcdef1", "enter")
	if w.answers.Passcode != "abcdef1" || w.stage != stageReview {
		t.Fatalf("passcode = %q, stage = %v", w.answers.Passcode, w.stage)
	}
	if !strings.Contains(w.View(), "Your own") || strings.Contains(w.View(), "abcdef1") {
		t.Error("review should say a new passcode was chosen, without showing it")
	}
	if got := labels(plan(s, w.answers)); !slices.Contains(got, "Save your new passcode") {
		t.Errorf("plan = %q", got)
	}

	// Going back to keep the current one drops the typed passcode.
	w = press(w, "esc", "up", "enter")
	if w.answers.Passcode != "" {
		t.Error("typed passcode kept after choosing to keep the current one")
	}
}

func TestWizardFirstPasscode(t *testing.T) {
	s := testSystem(t)
	w := newWizard(s, defaultAnswers(s))
	w = press(w, "enter", "enter", "enter", "enter", "enter")
	if c := w.choices(); c[0].title != "Make one for me" {
		t.Errorf("choices without a passcode = %+v", c)
	}
}

func TestBrokenConfigIsKept(t *testing.T) {
	dir := writeConfig(t, "name = \"Den\"\ntheme = \n")
	_, found, _, _, problem := readExisting(dir)
	if !found || problem == nil {
		t.Fatalf("found = %v, problem = %v; want a problem", found, problem)
	}

	s := testSystem(t)
	s.Dir, s.ConfigProblem = dir, problem.Error()
	actions := plan(s, defaultAnswers(s))
	i := slices.IndexFunc(actions, func(a action) bool { return strings.HasPrefix(a.Label, "Keep a copy") })
	if i < 0 || !strings.HasPrefix(actions[i+1].Label, "Save settings") {
		t.Fatalf("no copy before saving: %v", labels(actions))
	}
	for _, act := range actions[i : i+2] {
		if err := act.Run(); err != nil {
			t.Fatal(err)
		}
	}
	if old, _ := os.ReadFile(filepath.Join(dir, "config.toml.bak")); !strings.Contains(string(old), "Den") {
		t.Errorf("the copy holds %q", old)
	}
	if _, err := loadConfig(dir); err != nil {
		t.Errorf("new settings don't load: %v", err)
	}
}

func TestChassisName(t *testing.T) {
	for code, want := range map[string]string{"3": "Desktop", "10": "Laptop", "31": "Laptop", "1": "PC", "": "PC"} {
		if got := chassisName(code); got != want {
			t.Errorf("chassisName(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestPlainHTTPNote(t *testing.T) {
	if plainHTTPNote("lan") == "" || plainHTTPNote("both") == "" || plainHTTPNote("tailscale") != "" {
		t.Error("the note should show exactly when the home address is used")
	}
}

func TestUpdateHook(t *testing.T) {
	s := testSystem(t)
	if a := defaultAnswers(s); a.AutoUpdate {
		t.Error("offered updates without Omarchy")
	}
	if got := labels(plan(s, answers{Access: "lan", AutoUpdate: true})); slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "omarchy update") }) {
		t.Errorf("plan without Omarchy = %q", got)
	}

	s.OmarchyHooks = filepath.Join(t.TempDir(), "hooks")
	s.Executable = "/home/me/it's/pc-remote-web"
	a := defaultAnswers(s)
	if !a.AutoUpdate {
		t.Error("a new setup doesn't update with Omarchy")
	}
	if w := newWizard(s, a); !slices.Contains(w.steps(), stageUpdates) {
		t.Errorf("steps = %v", w.steps())
	}
	for _, act := range plan(s, a) {
		if strings.Contains(act.Label, "omarchy update") {
			if err := act.Run(); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, err := os.ReadFile(s.updateHookPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "exec '/home/me/it'\\''s/pc-remote-web' update\n") {
		t.Errorf("hook = %q", data)
	}

	s.UpdateHook, s.HasConfig = true, true
	if got := labels(plan(s, defaultAnswers(s))); slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "omarchy update") }) {
		t.Errorf("hook already there, plan = %q", got)
	}
	a.AutoUpdate = false
	for _, act := range plan(s, a) {
		if strings.HasPrefix(act.Label, "Stop updating") {
			if err := act.Run(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(s.updateHookPath()); !os.IsNotExist(err) {
		t.Error("hook still there after turning it off")
	}

	s.UpdateHook = false
	if defaultAnswers(s).AutoUpdate {
		t.Error("turned back on for someone who said no")
	}
}

// Only Omarchy has a desktop theme for the phone to follow.
func TestThemeChoicesFollowTheDesktop(t *testing.T) {
	s := testSystem(t)
	w := newWizard(s, defaultAnswers(s))
	w.stage = stageTheme
	var values []string
	for _, c := range w.choices() {
		values = append(values, c.value)
	}
	if got := slices.Contains(values, "omarchy"); got != omarchyDesktop {
		t.Fatalf("theme choices %q: offers Omarchy %v, want %v", values, got, omarchyDesktop)
	}
	if !slices.Contains(values, "default") {
		t.Fatalf("theme choices %q lack the default", values)
	}
}

// A PC with Tailscale installed but logged out says how to log in there.
func TestTailscaleLoginHint(t *testing.T) {
	s := testSystem(t)
	s.TailscaleName = "pc.tailnet.ts.net"
	w := newWizard(s, defaultAnswers(s))
	w.stage = stageConnect
	for _, c := range w.choices() {
		if c.value == "tailscale" && c.disabled != "Needs Tailscale. "+tailscaleLogin+", then run setup again." {
			t.Fatalf("disabled = %q", c.disabled)
		}
	}
}

func TestOmarchyThemeOnlyOnOmarchy(t *testing.T) {
	s := testSystem(t)
	s.HasConfig = true
	s.Existing.Theme = "omarchy"
	want := "omarchy"
	if !omarchyDesktop {
		want = "default"
	}
	if got := defaultAnswers(s).Theme; got != want {
		t.Fatalf("theme = %q, want %q", got, want)
	}
}
