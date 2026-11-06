package server

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/canberkbekir/omarchy-remote/internal/system"
)

// handleArt serves whatever cover the OS reads, but only when it is an image.
func TestHandleArt(t *testing.T) {
	var picture bytes.Buffer
	png.Encode(&picture, image.NewGray(image.Rect(0, 0, 2, 2)))
	saved := readCover
	defer func() { readCover = saved }()

	for _, tc := range []struct {
		name  string
		data  []byte
		err   error
		code  int
		ctype string
	}{
		{"png", picture.Bytes(), nil, http.StatusOK, "image/png"},
		{"not an image", []byte("<html>hi</html>"), nil, http.StatusNotFound, "application/json"},
		{"no cover", nil, system.ErrNoArt, http.StatusNotFound, "application/json"},
	} {
		readCover = func() ([]byte, error) { return tc.data, tc.err }
		w := httptest.NewRecorder()
		handleArt(w, httptest.NewRequest("GET", "/api/art", nil))
		if w.Code != tc.code || w.Header().Get("Content-Type") != tc.ctype || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q %q", tc.name, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
		}
		if tc.code == http.StatusOK && !bytes.Equal(w.Body.Bytes(), tc.data) {
			t.Errorf("%s: body differs", tc.name)
		}
	}
}
