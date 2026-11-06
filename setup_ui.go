package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// The wizard uses the terminal's own palette, so it follows the Omarchy theme.
type styles struct {
	r                       *lipgloss.Renderer
	title, accent, dim, bad lipgloss.Style
	good, code, tag, faint  lipgloss.Style
}

func newStyles(out io.Writer) styles {
	r := lipgloss.NewRenderer(out)
	return styles{
		r:      r,
		title:  r.NewStyle().Bold(true),
		accent: r.NewStyle().Foreground(lipgloss.Color("4")).Bold(true),
		dim:    r.NewStyle().Foreground(lipgloss.Color("8")),
		bad:    r.NewStyle().Foreground(lipgloss.Color("1")),
		good:   r.NewStyle().Foreground(lipgloss.Color("2")),
		code:   r.NewStyle().Foreground(lipgloss.Color("6")),
		tag:    r.NewStyle().Foreground(lipgloss.Color("2")),
		faint:  r.NewStyle().Faint(true),
	}
}

const contentWidth = 62

type stage int

const (
	stageCheck stage = iota
	stageConnect
	stageName
	stageTheme
	stageParty
	stagePasscode
	stagePasscodeEntry // typing a passcode of your own, part of the passcode step
	stageUpdates
	stageBar
	stageFirewall
	stageReview
)

// choice is one line in a list: what it is, and what happens if you pick it.
type choice struct {
	value    string
	title    string
	desc     string
	tag      string // e.g. "recommended"
	disabled string // why it can't be picked
}

type wizard struct {
	s         system
	answers   answers
	stage     stage
	cursor    int
	name      textinput.Model
	secret    [2]textinput.Model // the new passcode, then the same again
	typing    int                // which of the two has focus
	problem   string             // why the typed passcode was refused
	width     int
	help      bool
	confirmed bool
	st        styles
}

func newWizard(s system, a answers) wizard {
	name := textinput.New()
	name.SetValue(a.Name)
	name.Placeholder = s.DefaultName
	name.CharLimit = 40
	name.Prompt = "› "
	w := wizard{s: s, answers: a, name: name, width: 80, st: newStyles(os.Stdout)}
	w.name.PromptStyle = w.st.accent
	for i := range w.secret {
		w.secret[i] = textinput.New()
		w.secret[i].EchoMode = textinput.EchoPassword
		w.secret[i].EchoCharacter = '•'
		w.secret[i].CharLimit = maxPasscodeLength
		w.secret[i].Prompt = "› "
		w.secret[i].PromptStyle = w.st.accent
	}
	w.enter(stageCheck)
	return w
}

// steps are the numbered questions; updates and the bar show only on Omarchy,
// and the firewall only when needed.
func (w wizard) steps() []stage {
	steps := []stage{stageConnect, stageName, stageTheme, stageParty, stagePasscode}
	if w.s.OmarchyHooks != "" {
		steps = append(steps, stageUpdates)
	}
	if w.s.BarPluginDir != "" {
		steps = append(steps, stageBar)
	}
	if len(w.s.firewallBlocks(w.answers)) > 0 {
		steps = append(steps, stageFirewall)
	}
	return steps
}

func (w wizard) Init() tea.Cmd { return nil }

func (w *wizard) enter(st stage) tea.Cmd {
	w.stage, w.help, w.cursor = st, false, 0
	choices := w.choices()
	current := w.currentValue()
	for i, c := range choices {
		if c.value == current && c.disabled == "" {
			w.cursor = i
		}
	}
	w.name.Blur()
	switch st {
	case stageName:
		return w.name.Focus()
	case stagePasscodeEntry:
		w.typing, w.problem = 0, ""
		w.secret[0].SetValue(w.answers.Passcode)
		w.secret[1].SetValue(w.answers.Passcode)
		w.secret[1].Blur()
		return w.secret[0].Focus()
	}
	return nil
}

// step is the numbered step a stage belongs to.
func (w wizard) step() stage {
	if w.stage == stagePasscodeEntry {
		return stagePasscode
	}
	return w.stage
}

func (w wizard) currentValue() string {
	a := w.answers
	switch w.stage {
	case stageCheck:
		return yesNo(a.Install)
	case stageConnect:
		return a.Access
	case stageTheme:
		return a.Theme
	case stageParty:
		return yesNo(a.Party)
	case stageUpdates:
		return yesNo(a.AutoUpdate)
	case stageBar:
		return yesNo(a.Bar)
	case stageFirewall:
		return yesNo(a.OpenFirewall)
	case stagePasscode:
		if a.Passcode != "" {
			return "choose"
		}
	}
	return ""
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// next moves to the stage after the current one.
func (w *wizard) next() tea.Cmd {
	if w.stage == stageCheck {
		return w.enter(stageConnect)
	}
	steps := w.steps()
	i := slices.Index(steps, w.step())
	if i+1 < len(steps) {
		return w.enter(steps[i+1])
	}
	return w.enter(stageReview)
}

func (w *wizard) back() tea.Cmd {
	switch w.stage {
	case stageCheck:
		return nil
	case stageReview:
		steps := w.steps()
		return w.enter(steps[len(steps)-1])
	case stagePasscodeEntry:
		return w.enter(stagePasscode)
	}
	steps := w.steps()
	i := slices.Index(steps, w.stage)
	if i <= 0 {
		return w.enter(stageCheck)
	}
	return w.enter(steps[i-1])
}

func (w wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		return w, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return w, tea.Quit
		case "esc", "shift+tab":
			return w, w.back()
		}
		if w.stage == stageName {
			if msg.String() == "enter" {
				w.answers.Name = cleanPCName(w.name.Value(), w.s.DefaultName)
				w.name.SetValue(w.answers.Name)
				return w, w.next()
			}
			var cmd tea.Cmd
			w.name, cmd = w.name.Update(msg)
			return w, cmd
		}
		if w.stage == stagePasscodeEntry {
			return w.typePasscode(msg)
		}
		choices := w.choices()
		switch msg.String() {
		case "q":
			return w, tea.Quit
		case "?":
			w.help = !w.help
		case "up", "k":
			w.cursor = w.move(choices, -1)
		case "down", "j", "tab":
			w.cursor = w.move(choices, 1)
		case "enter", " ":
			if len(choices) == 0 {
				return w, w.next()
			}
			c := choices[w.cursor]
			if c.disabled != "" {
				return w, nil
			}
			return w.pick(c.value)
		}
	}
	return w, nil
}

// move steps the cursor, skipping choices that can't be picked.
func (w wizard) move(choices []choice, by int) int {
	for i := w.cursor + by; i >= 0 && i < len(choices); i += by {
		if choices[i].disabled == "" {
			return i
		}
	}
	return w.cursor
}

func (w wizard) pick(value string) (tea.Model, tea.Cmd) {
	yes := value == "yes"
	switch w.stage {
	case stageCheck:
		w.answers.Install = yes
	case stageConnect:
		w.answers.Access = value
	case stageTheme:
		w.answers.Theme = value
	case stageParty:
		w.answers.Party = yes
	case stageUpdates:
		w.answers.AutoUpdate = yes
	case stageBar:
		w.answers.Bar = yes
	case stageFirewall:
		w.answers.OpenFirewall = yes
	case stagePasscode:
		if value == "choose" {
			return w, w.enter(stagePasscodeEntry)
		}
		w.answers.Passcode = ""
	case stageReview:
		if yes {
			w.confirmed = true
			return w, tea.Quit
		}
		return w, w.enter(stageConnect)
	}
	return w, w.next()
}

// typePasscode handles keys while choosing a passcode: enter moves to the
// second box, then checks that both match.
func (w wizard) typePasscode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "enter" || msg.String() == "tab" {
		if w.typing == 0 {
			if w.problem = passcodeProblem(w.secret[0].Value()); w.problem != "" {
				return w, nil
			}
			w.typing = 1
			w.secret[0].Blur()
			return w, w.secret[1].Focus()
		}
		if msg.String() == "tab" {
			return w, nil
		}
		if w.secret[0].Value() != w.secret[1].Value() {
			w.problem = "The two don't match. Type it again."
			w.secret[1].SetValue("")
			return w, nil
		}
		w.answers.Passcode = w.secret[0].Value()
		return w, w.next()
	}
	w.problem = ""
	var cmd tea.Cmd
	w.secret[w.typing], cmd = w.secret[w.typing].Update(msg)
	return w, cmd
}

// question is what a stage asks: a title, a sentence on why, a longer
// explanation behind "?", and the choices.
type question struct {
	title, why, more string
	choices          []choice
}

func (w wizard) choices() []choice { return w.question().choices }

func (w wizard) question() question {
	s, a := w.s, w.answers
	switch w.stage {
	case stageCheck:
		packages := s.missingPackages()
		if len(packages) == 0 {
			return question{}
		}
		return question{
			title: "Install what's missing?",
			choices: []choice{
				{value: "yes", title: "Install it", desc: "Runs " + installHint(packages) + " after you review."},
				{value: "no", title: "Skip for now", desc: "Those features won't work until it's installed."},
			},
		}

	case stageConnect:
		both := choice{value: "both", title: "Home Wi-Fi and Tailscale",
			desc: "Any phone on your Wi-Fi, plus your own phone from anywhere."}
		tailscale := choice{value: "tailscale", title: "Tailscale only",
			desc: "Works on mobile data too, and every connection is encrypted. Phones without Tailscale can't connect."}
		switch {
		case s.Tailscale:
			both.tag = "Tailscale is on ✓"
		case s.TailscaleName == "" && !hasTailscale():
			both.disabled = "Needs Tailscale. Install it, then run setup again."
			tailscale.disabled = both.disabled
		default:
			both.disabled = "Needs Tailscale. " + tailscaleLogin + ", then run setup again."
			tailscale.disabled = both.disabled
		}
		return question{
			title: "How will your phone reach this PC?",
			why:   "You can change this later by running setup again.",
			more: "Same Wi-Fi is the simplest: the phone opens this PC's address on your home network. " +
				"Anyone on that network can see the passcode as it's sent, so use it on a network you trust.\n\n" +
				"Tailscale is a free app that links your devices privately. Install it on the PC and the phone, " +
				"and the remote works from anywhere over HTTPS.\n\n" +
				"With both, the app answers on your home network as before, and also through Tailscale.",
			choices: []choice{
				{value: "lan", title: "Same Wi-Fi", tag: "recommended",
					desc: "Works when your phone is on your home network."},
				both,
				tailscale,
			},
		}

	case stageName:
		return question{
			title: "What should the app call this PC?",
			why:   "It's the title at the top of the app on your phone.",
		}

	case stageTheme:
		var choices []choice
		if omarchyDesktop {
			omarchy := choice{value: "omarchy", title: "Match my Omarchy theme",
				desc: "When you change the theme here, the phone follows."}
			if s.OmarchyTheme != "" {
				omarchy.tag = "now: " + s.OmarchyTheme
			}
			choices = append(choices, omarchy)
		}
		choices = append(choices, choice{value: "default", title: "Dark with a lime accent",
			desc: "The app's own look. Stays the same whatever your desktop does."})
		for _, t := range s.CustomThemes {
			choices = append(choices, choice{value: t, title: titleCase(strings.ReplaceAll(t, "-", " ")),
				desc: "Your theme from " + tildePath(s.Dir) + "/themes/" + t + ".css"})
		}
		return question{
			title:   "Pick the app's colors",
			why:     "Only the look changes. Every theme has the same buttons.",
			more:    "You can also restyle anything with " + tildePath(s.Dir) + "/user.css. The README lists the theme variables.",
			choices: choices,
		}

	case stageParty:
		return question{
			title: "Let friends join games as players 2 to 4?",
			why:   "Friends on your Wi-Fi scan a code and get a controller. Only a controller: no volume, no keyboard, no files.",
			more: fmt.Sprintf("You start a party from the Game tab, and it shows a QR code. Each friend who scans it "+
				"becomes the next player on a virtual Xbox controller. The party page uses port %d, and only while a party is running.", s.guestPort()),
			choices: []choice{
				{value: "yes", title: "Yes, allow parties", desc: "Nothing opens until you start a party."},
				{value: "no", title: "No", desc: "Only you can use the controller."},
			},
		}

	case stagePasscode:
		first := choice{value: "keep", title: "Keep the current one", tag: "recommended",
			desc: "Phones that are signed in stay signed in."}
		if !s.HasPasscode {
			first = choice{value: "keep", title: "Make one for me", tag: "recommended",
				desc: "A random 10-character passcode, shown at the end."}
		}
		return question{
			title: "Which passcode should phones use?",
			why:   "Anyone with the passcode can control this PC, so keep it to yourself.",
			more: "The passcode is asked once on each phone, then remembered. Five wrong tries block that device for a minute. " +
				"You can see it any time with " + cliName + " passcode.",
			choices: []choice{first, {value: "choose", title: "Choose my own",
				desc: "Something you'll remember. Phones sign in again with it."}},
		}

	case stagePasscodeEntry:
		return question{
			title: "Choose your passcode",
			why: fmt.Sprintf("At least %d characters, no spaces. Longer is safer: a short one or one that's only digits is easy to guess.",
				minPasscodeLength),
		}

	case stageUpdates:
		return question{
			title: "Update Omarchy Remote when you update Omarchy?",
			why:   "omarchy update then gets the latest release too, and keeps your settings.",
			more: "This adds a small script to " + tildePath(s.updateHookPath()) + ", which Omarchy runs after each update. " +
				"It runs pc-remote update, which checks the download before installing it. You can always update by hand with pc-remote update.",
			choices: []choice{
				{value: "yes", title: "Yes, update together", tag: "recommended", desc: "Nothing else on your PC changes."},
				{value: "no", title: "No", desc: "Run pc-remote update yourself."},
			},
		}

	case stageBar:
		return question{
			title: "Show Omarchy Remote in the Omarchy bar?",
			why:   "A phone icon that lights up while your phone is connected. Click it for the QR code and passcode.",
			more: "It's a bar plugin in " + tildePath(s.BarPluginDir) + ", kept current when Omarchy Remote updates. " +
				"Move it like any other bar widget. It also highlights while a party is running.",
			choices: []choice{
				{value: "yes", title: "Yes, add the icon", tag: "recommended", desc: "It goes on the right of the bar."},
				{value: "no", title: "No", desc: "The bar stays as it is."},
			},
		}

	case stageFirewall:
		ports := s.firewallBlocks(a)
		what := "the app"
		if len(ports) == 1 && a.Party && ports[0] == s.guestPort() {
			what = "your friends' phones"
		}
		from := s.LANSubnet
		if from == "" {
			from = "your home network"
		}
		return question{
			title: "Your firewall may block " + what + ". Open it for your home network?",
			why:   "Only devices on " + from + " get through. Nothing from the internet.",
			more:  firewallExplain(from, ports),
			choices: []choice{
				{value: "yes", title: "Open it", tag: "recommended", desc: elevateOnceNote},
				{value: "no", title: "I'll do it myself", desc: "Run: " + firewallHint(from, ports)},
			},
		}

	case stageReview:
		return question{
			title: "Everything look right?",
			choices: []choice{
				{value: "yes", title: "Set it up"},
				{value: "no", title: "Change something"},
			},
		}
	}
	return question{}
}

func ufwRules(from string, ports []int) string {
	var rules []string
	for _, p := range ports {
		rules = append(rules, fmt.Sprintf("sudo ufw allow from %s to any port %d proto tcp", from, p))
	}
	return strings.Join(rules, "; ")
}

func hasTailscale() bool {
	_, ok := tailscaleTool()
	return ok
}

func (w wizard) View() string {
	st := w.st
	width := min(contentWidth, max(w.width-4, 30))
	wrap := st.r.NewStyle().Width(width)
	q := w.question()

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(st.dim.Render("  Omarchy Remote · setup"))
	b.WriteString("\n\n")

	switch w.stage {
	case stageCheck:
		b.WriteString(w.welcome(width))
	case stageReview:
		b.WriteString(w.review(width))
	default:
		b.WriteString(indent(w.stepBar(), 2))
		b.WriteString("\n\n")
	}

	if q.title != "" {
		b.WriteString(indent(st.title.Render(wrap.Render(q.title)), 2) + "\n")
	}
	if q.why != "" {
		b.WriteString(indent(st.dim.Render(wrap.Render(q.why)), 2) + "\n")
	}
	if w.help && q.more != "" {
		b.WriteString("\n" + indent(wrap.Render(q.more), 4) + "\n")
	}
	if q.title != "" {
		b.WriteString("\n")
	}

	if w.stage == stageName {
		b.WriteString("  " + w.name.View() + "\n\n")
		b.WriteString(indent(st.dim.Render("enter keeps this name · esc back"), 2))
		return b.String() + "\n"
	}
	if w.stage == stagePasscodeEntry {
		b.WriteString("  " + st.dim.Render("New passcode   ") + w.secret[0].View() + "\n")
		b.WriteString("  " + st.dim.Render("Type it again  ") + w.secret[1].View() + "\n\n")
		if w.problem != "" {
			b.WriteString("  " + st.bad.Render("✗ "+w.problem) + "\n\n")
		}
		b.WriteString(indent(st.dim.Render("enter next · esc back · ctrl+c quit"), 2))
		return b.String() + "\n"
	}
	for i, c := range q.choices {
		b.WriteString(w.renderChoice(c, i == w.cursor, width))
	}

	hints := []string{"↑↓ move", "enter choose"}
	if len(q.choices) == 0 {
		hints = []string{"enter start"}
	}
	if w.stage != stageCheck {
		hints = append(hints, "esc back")
	}
	if q.more != "" {
		if w.help {
			hints = append(hints, "? less")
		} else {
			hints = append(hints, "? tell me more")
		}
	}
	hints = append(hints, "ctrl+c quit")
	b.WriteString("\n" + indent(st.dim.Render(strings.Join(hints, " · ")), 2) + "\n")
	return b.String()
}

func (w wizard) welcome(width int) string {
	st := w.st
	card := st.r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).
		Padding(0, 2).Width(width).Render(
		st.title.Render("Turn your phone into a remote for this PC.") + "\n" +
			st.dim.Render("A few questions, about a minute. Nothing changes until you confirm."))

	var b strings.Builder
	b.WriteString(indent(card, 2) + "\n\n")
	b.WriteString(indent(st.title.Render("Checking this PC"), 2) + "\n")
	for _, c := range w.s.Checks {
		mark, detail := st.good.Render("✓"), st.dim.Render("ready")
		if !c.OK {
			mark, detail = st.bad.Render("✗"), st.bad.Render(c.Detail)
		}
		fmt.Fprintf(&b, "    %s  %-32s %s\n", mark, c.Label, detail)
	}
	if w.s.ConfigProblem != "" {
		note := "Your settings in " + tildePath(filepath.Join(w.s.Dir, "config.toml")) + " have a mistake: " + w.s.ConfigProblem +
			"\nSetup starts from the defaults and keeps a copy of the old file. To fix it by hand instead, press ctrl+c."
		b.WriteString("\n" + indent(st.bad.Render(st.r.NewStyle().Width(width).Render(note)), 2) + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func (w wizard) stepBar() string {
	st := w.st
	steps := w.steps()
	names := map[stage]string{stageConnect: "Connect", stageName: "Name", stageTheme: "Colors",
		stageParty: "Party", stagePasscode: "Passcode", stageUpdates: "Updates", stageBar: "Bar", stageFirewall: "Firewall"}
	at := slices.Index(steps, w.step())
	var parts []string
	for i, s := range steps {
		switch {
		case i < at:
			parts = append(parts, st.good.Render("✓ "+names[s]))
		case i == at:
			parts = append(parts, st.accent.Render("● "+names[s]))
		default:
			parts = append(parts, st.dim.Render("○ "+names[s]))
		}
	}
	return strings.Join(parts, st.dim.Render(" ── ")) + st.dim.Render(fmt.Sprintf("    %d of %d", at+1, len(steps)))
}

func (w wizard) review(width int) string {
	st, s, a := w.st, w.s, w.answers
	connect := "Same Wi-Fi"
	switch a.Access {
	case "tailscale":
		connect = "Tailscale only"
	case "both":
		connect = "Home Wi-Fi and Tailscale"
	}
	theme := titleCase(strings.ReplaceAll(a.Theme, "-", " "))
	switch a.Theme {
	case "omarchy":
		theme = "Match Omarchy"
		if s.OmarchyTheme != "" {
			theme += " (" + s.OmarchyTheme + ")"
		}
	case "default":
		theme = "Dark with a lime accent"
	}
	party := "Not allowed"
	if a.Party {
		party = "Allowed"
	}
	passcode := "Keep the current one"
	switch {
	case a.Passcode != "":
		passcode = "Your own"
	case !s.HasPasscode:
		passcode = "Made for you"
	}
	rows := [][2]string{{"Connect", connect}, {"Name", a.Name}, {"Colors", theme}, {"Party", party}, {"Passcode", passcode}}
	if s.OmarchyHooks != "" {
		updates := "By hand with " + cliName + " update"
		if a.AutoUpdate {
			updates = "With omarchy update"
		}
		rows = append(rows, [2]string{"Updates", updates})
	}
	if s.BarPluginDir != "" {
		icon := "Not shown"
		if a.Bar {
			icon = "Phone icon"
		}
		rows = append(rows, [2]string{"Bar", icon})
	}

	var b strings.Builder
	b.WriteString(indent(st.title.Render("Your choices"), 2) + "\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "    %s %s\n", st.dim.Render(fmt.Sprintf("%-9s", r[0])), r[1])
	}
	if note := plainHTTPNote(a.Access); note != "" {
		b.WriteString("\n" + indent(st.dim.Render(st.r.NewStyle().Width(width-4).Render(note)), 4) + "\n")
	}
	b.WriteString("\n" + indent(st.title.Render("What happens next"), 2) + "\n")
	for i, act := range plan(s, a) {
		label := act.Label
		if act.Sudo {
			label += st.dim.Render(" · " + elevationNote)
		}
		label = st.r.NewStyle().Width(width - 7).Render(label)
		fmt.Fprintf(&b, "    %s %s\n", st.dim.Render(fmt.Sprintf("%d.", i+1)), indent(label, 7)[7:])
	}
	b.WriteString("\n")
	return b.String()
}

// plainHTTPNote says what the home address doesn't protect, so it's a choice
// made knowingly.
func plainHTTPNote(access string) string {
	switch access {
	case "lan":
		return "Heads up: on home Wi-Fi the connection isn't encrypted, so someone else on your network could see the passcode. Tailscale encrypts it."
	case "both":
		return "Heads up: the home address isn't encrypted, so someone else on your network could see the passcode. The Tailscale address is encrypted."
	}
	return ""
}

func (w wizard) renderChoice(c choice, selected bool, width int) string {
	st := w.st
	marker, title := "  ", c.title
	switch {
	case c.disabled != "":
		title = st.faint.Render(title)
	case selected:
		marker, title = st.accent.Render("›")+" ", st.accent.Render(title)
	}
	line := marker + title
	if c.tag != "" {
		gap := max(width-lipgloss.Width(line)-lipgloss.Width(c.tag), 2)
		line += strings.Repeat(" ", gap) + st.tag.Render(c.tag)
	}
	desc := c.desc
	if c.disabled != "" {
		desc = c.disabled
	}
	out := "  " + line + "\n"
	if desc == "" {
		return out
	}
	text := st.r.NewStyle().Width(width - 4).Render(desc)
	if c.disabled != "" || !selected {
		text = st.dim.Render(text)
	}
	return out + indent(text, 6) + "\n\n"
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

func terminalWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	return 80
}

// readyCard is the last screen: the QR code to scan, what to do with it, and the passcode.
func readyCard(st styles, addresses []address, passcode string, width int) string {
	steps := strings.Join([]string{
		st.accent.Render("1") + "  Scan this with your phone's camera",
		st.accent.Render("2") + "  Tap Share, then Add to Home Screen",
		st.accent.Render("3") + "  Open the new icon and enter the passcode",
	}, "\n\n")
	code := qrBlock(st, addresses[0].url)
	var top string
	if width >= 90 {
		top = lipgloss.JoinHorizontal(lipgloss.Center, code, "   ", steps)
	} else {
		top = code + "\n\n" + steps
	}
	var rows strings.Builder
	for _, a := range addresses {
		rows.WriteString(st.dim.Render(fmt.Sprintf("%-10s", a.label)) + st.code.Render(a.url) + "\n")
	}
	body := st.good.Render("✓ Ready") + "\n\n" + top + "\n\n" + rows.String() +
		st.dim.Render("Passcode  ") + st.title.Render(groupPasscode(passcode)) + st.dim.Render("   spaces don't matter")
	card := st.r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("2")).Padding(1, 3).Render(body)
	return indent(card, 2)
}

// qrBlock draws a QR code with half blocks, two rows per line, dark on light
// whatever the terminal's colors, with the quiet border scanners need.
func qrBlock(st styles, text string) string {
	rows := qrRows(text)
	if rows == nil {
		return ""
	}
	const quiet = 2
	size := len(rows) + 2*quiet
	light := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x < 0 || y < 0 || x >= len(rows) || y >= len(rows) || rows[y][x] == '0'
	}
	paint := st.r.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#000000"))
	var lines []string
	for y := 0; y < size; y += 2 {
		var line strings.Builder
		for x := range size {
			top, bottom := light(x, y), y+1 < size && light(x, y+1)
			switch {
			case top && bottom:
				line.WriteString("█")
			case top:
				line.WriteString("▀")
			case bottom:
				line.WriteString("▄")
			default:
				line.WriteString(" ")
			}
		}
		lines = append(lines, paint.Render(line.String()))
	}
	return strings.Join(lines, "\n")
}
