package main

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxArtBytes       = 8 << 20
	maxTypedRunes     = 2000
	maxClipboardBytes = 64 << 10
)

type track struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Player   string  `json:"player"`
	Art      string  `json:"art"`
	Position float64 `json:"position"`
	Length   float64 `json:"length"`
	Playing  bool    `json:"playing"`
}

type output struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

const trackFormat = "{{status}}\t{{playerName}}\t{{title}}\t{{artist}}\t{{mpris:artUrl}}\t{{position}}\t{{mpris:length}}"

// readTrack returns what the current media player is playing, and the raw
// art URL it reported, or nil when nothing is loaded.
func readTrack() (*track, string) {
	text, err := runCommand("playerctl", "metadata", "--format", trackFormat)
	if err != nil {
		return nil, ""
	}
	fields := strings.Split(strings.TrimRight(text, "\n"), "\t")
	if len(fields) != 7 || fields[2] == "" || fields[0] == "Stopped" {
		return nil, ""
	}
	position, _ := strconv.ParseFloat(fields[5], 64)
	length, _ := strconv.ParseFloat(fields[6], 64)
	t := &track{
		Playing:  fields[0] == "Playing",
		Player:   fields[1],
		Title:    fields[2],
		Artist:   fields[3],
		Position: position / 1e6,
		Length:   length / 1e6,
	}
	art := fields[4]
	// Served by /api/art, so the phone never contacts the cover's server.
	if strings.HasPrefix(art, "https://") || strings.HasPrefix(art, "file://") {
		h := fnv.New64a()
		h.Write([]byte(art))
		t.Art = "/api/art?v=" + strconv.FormatUint(h.Sum64(), 36)
	}
	return t, art
}

var errNoArt = errors.New("no cover art")

// publicOnly keeps cover fetches away from this PC and the local network. It
// checks the address actually dialed, so redirects and DNS can't get around it.
func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || tailnetRange.Contains(ip) {
		return errNoArt
	}
	return nil
}

var artClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext:       (&net.Dialer{Control: publicOnly}).DialContext,
		ForceAttemptHTTP2: true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 5 {
			return errNoArt
		}
		return nil
	},
}

func readArt(art string) ([]byte, error) {
	location, err := url.Parse(art)
	if err != nil {
		return nil, err
	}
	switch location.Scheme {
	case "file":
		info, err := os.Stat(location.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxArtBytes {
			return nil, errNoArt
		}
		return os.ReadFile(location.Path)
	case "https":
		resp, err := artClient.Get(location.String())
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, errNoArt
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtBytes+1))
		if err != nil || len(data) > maxArtBytes {
			return nil, errNoArt
		}
		return data, nil
	}
	return nil, errNoArt
}

// handleArt serves the current track's cover. The URL comes from the player,
// never from the request.
func handleArt(w http.ResponseWriter, r *http.Request) {
	_, art := readTrack()
	data, err := readArt(art)
	if err != nil {
		writeError(w, http.StatusNotFound, "No cover art.")
		return
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		writeError(w, http.StatusNotFound, "No cover art.")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

var bracketName = regexp.MustCompile(`\[([^\]]+)\]`)

// outputLabel turns a sound card's long description into a short name.
func outputLabel(description string) string {
	if match := bracketName.FindStringSubmatch(description); match != nil {
		return match[1]
	}
	if strings.Contains(description, "HD Audio Controller") {
		return "PC audio jack"
	}
	for _, suffix := range []string{" Analog Stereo", " Digital Stereo (HDMI)", " Digital Stereo", " Stereo"} {
		description = strings.TrimSuffix(description, suffix)
	}
	return description
}

func handleOutputs(w http.ResponseWriter, r *http.Request) {
	outputs, err := listOutputs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read audio outputs.")
		return
	}
	writeJSON(w, http.StatusOK, outputs)
}

func handleOutput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	outputs, err := listOutputs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read audio outputs.")
		return
	}
	if !slices.ContainsFunc(outputs, func(o output) bool { return o.Name == body.Name }) {
		writeError(w, http.StatusBadRequest, "Unknown audio output.")
		return
	}
	if err := setOutput(body.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	handleOutputs(w, r)
}

func handleScreens(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.On == nil {
		writeError(w, http.StatusBadRequest, "On must be true or false.")
		return
	}
	if err := setScreens(*body.On); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// runDetached runs a command without output pipes, because wl-copy keeps
// running in the background and would hold them open.
func runDetached(input string, argv ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}

func handleType(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text  string `json:"text"`
		Enter bool   `json:"enter"`
	}
	if !decodeBodyLimit(w, r, &body, 4*maxTypedRunes+256) {
		return
	}
	if utf8.RuneCountInString(body.Text) > maxTypedRunes || (body.Text == "" && !body.Enter) {
		writeError(w, http.StatusBadRequest, "Text must be 1 to 2000 characters.")
		return
	}
	if body.Text != "" {
		if err := typeText(body.Text); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not type the text.")
			return
		}
	}
	if body.Enter {
		if err := pressEnter(); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not press Enter.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleClipboardRead(w http.ResponseWriter, r *http.Request) {
	text := readClipboard()
	truncated := len(text) > maxClipboardBytes
	if truncated {
		text = strings.ToValidUTF8(text[:maxClipboardBytes], "")
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "truncated": truncated})
}

func handleClipboardWrite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeBodyLimit(w, r, &body, 2*maxClipboardBytes) {
		return
	}
	if body.Text == "" || len(body.Text) > maxClipboardBytes {
		writeError(w, http.StatusBadRequest, "Text must be 1 byte to 64 KB.")
		return
	}
	if err := writeClipboard(body.Text); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not set the clipboard.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
