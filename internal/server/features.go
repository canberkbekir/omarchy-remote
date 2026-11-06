package server

import (
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/canberkbekir/omarchy-remote/internal/system"
)

const (
	maxTypedRunes     = 2000
	maxClipboardBytes = 64 << 10
)

// readCover returns the current track's cover image. Each OS reads it its
// own way in system.Cover; tests swap it.
var readCover = system.Cover

// handleArt serves the current track's cover. It comes from the player,
// never from the request.
func handleArt(w http.ResponseWriter, r *http.Request) {
	data, err := readCover()
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

func handleOutputs(w http.ResponseWriter, r *http.Request) {
	outputs, err := system.Outputs()
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
	outputs, err := system.Outputs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read audio outputs.")
		return
	}
	if !slices.ContainsFunc(outputs, func(o system.Output) bool { return o.Name == body.Name }) {
		writeError(w, http.StatusBadRequest, "Unknown audio output.")
		return
	}
	if err := system.SetOutput(body.Name); err != nil {
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
	if err := system.SetScreens(*body.On); err != nil {
		writeError(w, http.StatusInternalServerError, "Command failed.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
		if err := system.TypeText(body.Text); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not type the text.")
			return
		}
	}
	if body.Enter {
		if err := system.PressEnter(); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not press Enter.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleClipboardRead(w http.ResponseWriter, r *http.Request) {
	text := system.ReadClipboard()
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
	if err := system.WriteClipboard(body.Text); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not set the clipboard.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
