package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The Omarchy bar widget: an icon that shows whether a phone is connected,
// and a card with the app's QR code and the passcode. Setup installs it into
// the shell's plugin folder; the server refreshes it after an update.
const barPluginID = "canberkbekir.omarchy-remote"

//go:embed omarchy
var barPluginFiles embed.FS

// phoneTimeout is how long after its last status poll a phone still counts as
// connected. The app polls every 3 seconds while it's open.
const phoneTimeout = 10 * time.Second

// barPluginDir is where the shell looks for the widget.
func barPluginDir() (string, error) {
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

// writeBarPlugin puts the widget's files in dir, and reports whether any changed.
func writeBarPlugin(dir, executable string) (bool, error) {
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
	_, err := writeBarPlugin(dir, executable)
	if err != nil {
		return err
	}
	if _, err = runCommand("omarchy-shell", "shell", "rescanPlugins"); err != nil {
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
	runCommand("omarchy", "plugin", "disable", barPluginID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	runCommand("omarchy-shell", "shell", "rescanPlugins")
	return nil
}

// syncBarPlugin keeps an installed widget in step with this version, so an
// update reaches the bar too. The shell reloads changed plugin files itself.
func syncBarPlugin() {
	dir, err := barPluginDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return
	}
	if _, err := writeBarPlugin(dir, executable); err != nil {
		fmt.Fprintln(os.Stderr, "updating the bar widget:", err)
	}
}

type presence struct {
	Phone bool      `json:"phone"`
	Party partyView `json:"party"`
}

// handlePresence tells the bar widget whether a phone is using the app.
func (a *app) handlePresence(w http.ResponseWriter, r *http.Request) {
	seen := time.Unix(0, a.phoneSeen.Load())
	writeJSON(w, http.StatusOK, presence{Phone: time.Since(seen) < phoneTimeout, Party: a.party.view()})
}

// barStatus is what `pc-remote status --json` prints for the widget.
type barStatus struct {
	Running bool   `json:"running"`
	Phone   bool   `json:"phone"`
	Party   bool   `json:"party"`
	Code    string `json:"code,omitempty"`
	Players int    `json:"players"`
	Problem string `json:"problem,omitempty"`

	// With --connect: how to open the app.
	Name  string   `json:"name,omitempty"`
	Label string   `json:"label,omitempty"`
	URL   string   `json:"url,omitempty"`
	QR    []string `json:"qr,omitempty"`
}

// askPresence asks the running server, the way the phone does.
func askPresence(c config, passcode string) (presence, error) {
	host, port, err := net.SplitHostPort(c.listenAddr())
	if err != nil {
		return presence{}, err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	req, err := http.NewRequest("GET", "http://"+net.JoinHostPort(host, port)+"/api/presence", nil)
	if err != nil {
		return presence{}, err
	}
	req.Header.Set("Authorization", "Bearer "+passcode)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return presence{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return presence{}, errors.New(resp.Status)
	}
	var p presence
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&p)
	return p, err
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
	dir, err := configDir()
	var c config
	if err == nil {
		c, err = loadConfig(dir)
	}
	configured := err == nil
	var passcode string
	if err == nil {
		passcode, err = loadPasscode(dir)
	}
	if err != nil {
		st.Problem = err.Error()
	} else if _, err := runCommand("systemctl", "--user", "is-active", "--quiet", "pc-remote-web.service"); err != nil {
		st.Problem = "Omarchy Remote isn't running. Start it with: systemctl --user start pc-remote-web"
	} else if p, err := askPresence(c, passcode); err != nil {
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
		s := system{Dir: dir, Existing: c, LANAddress: lanAddress()}
		if viaTailscale(c.Access) {
			s.TailscaleName, _ = tailscaleName()
		}
		first := appAddresses(s, answers{Access: c.Access})[0]
		st.Name, st.Label, st.URL, st.QR = c.Name, first.label, first.url, qrRows(first.url)
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
	if st.URL != "" {
		fmt.Fprintln(out, st.Label+":", st.URL)
	}
	return 0
}
