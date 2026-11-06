package server

import (
	"net/http"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/api"
)

// phoneTimeout is how long after its last status poll a phone still counts as
// connected. The app polls every 3 seconds while it's open.
const phoneTimeout = 10 * time.Second

// handlePresence tells the bar widget whether a phone is using the app.
func (a *app) handlePresence(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.presence())
}

// presence is whether a phone uses the app and how the party stands, for the
// bar widget and the Windows tray icon.
func (a *app) presence() api.Presence {
	seen := time.Unix(0, a.phoneSeen.Load())
	return api.Presence{Phone: time.Since(seen) < phoneTimeout, Party: a.party.view()}
}
