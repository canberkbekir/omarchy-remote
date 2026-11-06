package cli

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/api"
	"github.com/canberkbekir/omarchy-remote/internal/config"
	"github.com/canberkbekir/omarchy-remote/internal/run"
)

// The Omarchy bar widget: an icon that shows whether a phone is connected,
// and a card with the app's QR code and the passcode. Setup installs it into
// the shell's plugin folder; the server refreshes it after an update.
const barPluginID = "canberkbekir.omarchy-remote"

//go:embed omarchy
var barPluginFiles embed.FS

// BarPluginDir is where the shell looks for the widget.
func BarPluginDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "omarchy", "plugins", barPluginID), nil
}

// barPluginContents is every file of the widget. pc-remote.sh points the
// widget at this program, wherever it's installed.
func barPluginContents(executable string) (map[string][]byte, error) {
	files := map[string][]byte{
		"pc-remote.sh": []byte("#!/bin/bash\n# Written by pc-remote: runs the program that installed this widget.\nexec " +
			shellQuote(executable) + " \"$@\"\n"),
	}
	err := fs.WalkDir(barPluginFiles, "omarchy", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := barPluginFiles.ReadFile(path)
		files[strings.TrimPrefix(path, "omarchy/")] = data
		return err
	})
	return files, err
}

// WriteBarPlugin puts the widget's files in dir, and reports whether any changed.
func WriteBarPlugin(dir, executable string) (bool, error) {
	files, err := barPluginContents(executable)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	changed := false
	for name, data := range files {
		path := filepath.Join(dir, name)
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			continue
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// installBarPlugin writes the widget and turns it on. The shell picks up new
// files on its own; enabling it adds the icon to the bar.
func installBarPlugin(dir, executable string) error {
	_, err := WriteBarPlugin(dir, executable)
	if err != nil {
		return err
	}
	if _, err = run.Command("omarchy-shell", "shell", "rescanPlugins"); err != nil {
		return fmt.Errorf("the Omarchy bar didn't answer. Log in to Omarchy and run `pc-remote setup` again (%v)", err)
	}
	// The shell rescans in the background, so the plugin may not be known yet.
	var out []byte
	for range 10 {
		out, err = exec.Command("omarchy", "plugin", "enable", barPluginID).CombinedOutput()
		if err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("couldn't add it to the bar: %s", strings.TrimSpace(string(out)))
}

// removeBarPlugin takes the widget off the bar and deletes it.
func removeBarPlugin(dir string) error {
	// Disabling fails when the shell never saw it; deleting it is what counts.
	run.Command("omarchy", "plugin", "disable", barPluginID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	run.Command("omarchy-shell", "shell", "rescanPlugins")
	return nil
}

// barStatus is what `pc-remote status --json` prints for the widget.
type barStatus struct {
	Running bool   `json:"running"`
	Phone   bool   `json:"phone"`
	Party   bool   `json:"party"`
	Code    string `json:"code,omitempty"`
	Players int    `json:"players"`
	Problem string `json:"problem,omitempty"`

	// With --connect: how to open the app. Label, URL and QR repeat the
	// first address, for bar cards from before there were several.
	Name      string          `json:"name,omitempty"`
	Label     string          `json:"label,omitempty"`
	URL       string          `json:"url,omitempty"`
	QR        []string        `json:"qr,omitempty"`
	Addresses []ConnectChoice `json:"addresses,omitempty"`
}

// runStatus is `pc-remote status`.
func runStatus(args []string, out io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print JSON, for the bar widget")
	connect := flags.Bool("connect", false, "also give the app's address and its QR code")
	flags.SetOutput(out)
	if err := flags.Parse(args); err != nil {
		return 2
	}

	var st barStatus
	dir, err := config.Dir()
	var c config.Config
	if err == nil {
		c, err = config.Load(dir)
	}
	configured := err == nil
	var passcode string
	if err == nil {
		passcode, err = config.LoadPasscode(dir)
	}
	if err != nil {
		st.Problem = err.Error()
	} else if !serviceRunning(c) {
		st.Problem = notRunningProblem
	} else if p, err := api.AskPresence(c, passcode); err != nil {
		st.Problem = "Omarchy Remote isn't answering: " + err.Error()
	} else {
		st.Running, st.Phone, st.Party, st.Code = true, p.Phone, p.Party.Active, p.Party.Code
		for _, player := range p.Party.Players {
			if player.Connected {
				st.Players++
			}
		}
	}
	if *connect && configured {
		st.Name, st.Addresses = c.Name, ConnectChoices(dir, c)
		if len(st.Addresses) > 0 {
			first := st.Addresses[0]
			st.Label, st.URL, st.QR = first.Label, first.URL, first.QR
		}
	}

	if *asJSON {
		json.NewEncoder(out).Encode(st)
		return 0
	}
	switch {
	case st.Problem != "":
		fmt.Fprintln(out, st.Problem)
		return 1
	case st.Phone:
		fmt.Fprintln(out, "Running. A phone is connected.")
	default:
		fmt.Fprintln(out, "Running. No phone is connected.")
	}
	if st.Party {
		fmt.Fprintf(out, "Party %s: %d of 3 friends joined.\n", st.Code, st.Players)
	}
	for _, a := range st.Addresses {
		fmt.Fprintln(out, a.Label+":", a.URL)
	}
	return 0
}
