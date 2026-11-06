package server

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/canberkbekir/omarchy-remote/internal/run"
)

// fakeStart swaps run.Start for a recorder, so no test reboots the PC.
func fakeStart(t *testing.T) *[][]string {
	return fakeStartErr(t, nil)
}

func fakeStartErr(t *testing.T, err error) *[][]string {
	t.Helper()
	var started [][]string
	old := run.Start
	run.Start = func(argv ...string) error {
		started = append(started, argv)
		return err
	}
	t.Cleanup(func() { run.Start = old })
	return &started
}

func TestHandlePowerUnknownAction(t *testing.T) {
	fakeStart(t)
	r := httptest.NewRequest("POST", "/api/power", strings.NewReader(`{"action":"hibernate"}`))
	w := httptest.NewRecorder()
	handlePower(w, r)
	if w.Code != 400 {
		t.Errorf("status %d, want 400: %s", w.Code, w.Body)
	}
}

func TestHandlePowerStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		body string
	}{
		{nil, 200, `{"ok":true}`},
		{errors.New("no shutdown.exe"), 500, `Command failed.`},
	} {
		fakeStartErr(t, tc.err)
		r := httptest.NewRequest("POST", "/api/power", strings.NewReader(`{"action":"reboot"}`))
		w := httptest.NewRecorder()
		handlePower(w, r)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("start error %v: got %d %s, want %d %s", tc.err, w.Code, w.Body, tc.code, tc.body)
		}
	}
}
