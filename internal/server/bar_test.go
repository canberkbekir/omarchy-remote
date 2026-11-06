package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/api"
)

func TestPresence(t *testing.T) {
	a := &app{party: newParty()}
	ask := func() api.Presence {
		w := httptest.NewRecorder()
		a.handlePresence(w, httptest.NewRequest("GET", "/api/presence", nil))
		var p api.Presence
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if ask().Phone {
		t.Error("a phone before any status poll")
	}
	a.phoneSeen.Store(time.Now().UnixNano())
	if !ask().Phone {
		t.Error("no phone right after a status poll")
	}
	a.phoneSeen.Store(time.Now().Add(-phoneTimeout - time.Second).UnixNano())
	if ask().Phone {
		t.Error("a phone long after the last status poll")
	}
}

func TestPresenceMatchesHandler(t *testing.T) {
	a := &app{party: newParty()}
	a.phoneSeen.Store(time.Now().UnixNano())
	if p := a.presence(); !p.Phone || p.Party.Active {
		t.Errorf("presence() = %+v", p)
	}
}
