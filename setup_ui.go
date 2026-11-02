package main

import (
	"fmt"
	"io"
	"os"
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
	width     int
	help      bool
	confirmed bool
	st        styles
}

func newWizard(s system, a answers) wizard {
	name := textinput.New()
	name.SetValue(a.Name)
	name.Placeholder = s.Hostname
	name.CharLimit = 40
	name.Prompt = "› "
	w := wizard{s: s, answers: a, name: name, width: 80, st: newStyles(os.Stdout)}
	w.name.PromptStyle = w.st.accent
	w.enter(stageCheck)
	return w
}

// steps are the numbered questions; the firewall one shows only when needed.
func (w wizard) steps() []stage {
	steps := []stage{stageConnect, stageName, stageTheme, stageParty}
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
	if st == stageName {
		return w.name.Focus()
	}
	w.name.Blur()
	return nil
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
	case stageFirewall:
		return yesNo(a.OpenFirewall)
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
	i := slices.Index(steps, w.stage)
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
				w.answers.Name = cleanPCName(w.name.Value(), w.s.Hostname)
				w.name.SetValue(w.answers.Name)
				return w, w.next()
			}
			var cmd tea.Cmd
			w.name, cmd = w.name.Update(msg)
			return w, cmd
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
	case stageFirewall:
		w.answers.OpenFirewall = yes
	case stageReview:
		if yes {
			w.confirmed = true
			return w, tea.Quit
		}
		return w, w.enter(stageConnect)
	}
	return w, w.next()
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
				{value: "yes", title: "Install it", desc: "Runs sudo pacman -S " + strings.Join(packages, " ") + " after you review."},
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
		case s.TailscaleName == "" && !hasTool("tailscale"):
			both.disabled = "Needs Tailscale. Install it, then run setup again."
			tailscale.disabled = both.disabled
		default:
			both.disabled = "Needs Tailscale. Log in with sudo tailscale up, then run setup again."
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
		omarchy := choice{value: "omarchy", title: "Match my Omarchy theme",
			desc: "When you change the theme here, the phone follows."}
		if s.OmarchyTheme != "" {
			omarchy.tag = "now: " + s.OmarchyTheme
		}
		choices := []choice{omarchy, {value: "default", title: "Dark with a lime accent",
			desc: "The app's own look. Stays the same whatever your desktop does."}}
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
			more:  "Omarchy turns on the ufw firewall, which blocks every incoming connection unless a rule allows it. This adds: " + ufwRules(from, ports) + ".",
			choices: []choice{
				{value: "yes", title: "Open it", tag: "recommended", desc: "Asks for your password once, after you review."},
				{value: "no", title: "I'll do it myself", desc: "Run: " + ufwRules(from, ports)},
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

func hasTool(name string) bool {
	_, ok := lookPath(name)
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
	b.WriteString("\n")
	return b.String()
}

func (w wizard) stepBar() string {
	st := w.st
	steps := w.steps()
	names := map[stage]string{stageConnect: "Connect", stageName: "Name", stageTheme: "Colors",
		stageParty: "Party", stageFirewall: "Firewall"}
	at := slices.Index(steps, w.stage)
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
	rows := [][2]string{{"Connect", connect}, {"Name", a.Name}, {"Colors", theme}, {"Party", party}}

	var b strings.Builder
	b.WriteString(indent(st.title.Render("Your choices"), 2) + "\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "    %s %s\n", st.dim.Render(fmt.Sprintf("%-9s", r[0])), r[1])
	}
	b.WriteString("\n" + indent(st.title.Render("What happens next"), 2) + "\n")
	for i, act := range plan(s, a) {
		label := act.Label
		if act.Sudo {
			label += st.dim.Render(" · asks for your password")
		}
		label = st.r.NewStyle().Width(width - 7).Render(label)
		fmt.Fprintf(&b, "    %s %s\n", st.dim.Render(fmt.Sprintf("%d.", i+1)), indent(label, 7)[7:])
	}
	b.WriteString("\n")
	return b.String()
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
