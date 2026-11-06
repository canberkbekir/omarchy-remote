package system

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadArtOverHTTPS(t *testing.T) {
	var picture bytes.Buffer
	png.Encode(&picture, image.NewGray(image.Rect(0, 0, 2, 2)))
	mux := http.NewServeMux()
	mux.HandleFunc("/cover.png", func(w http.ResponseWriter, r *http.Request) { w.Write(picture.Bytes()) })
	mux.HandleFunc("/huge", func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, maxArtBytes+1)) })
	mux.HandleFunc("/missing", http.NotFound)
	plain := httptest.NewServer(mux)
	defer plain.Close()
	mux.HandleFunc("/to-http", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/cover.png", http.StatusFound)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	saved := artClient.Transport
	artClient.Transport = server.Client().Transport
	defer func() { artClient.Transport = saved }()

	if data, err := readArt(server.URL + "/cover.png"); err != nil || !bytes.Equal(data, picture.Bytes()) {
		t.Fatalf("cover = %d bytes, %v", len(data), err)
	}
	for _, path := range []string{"/huge", "/missing", "/to-http"} {
		if _, err := readArt(server.URL + path); err == nil {
			t.Errorf("%s should fail", path)
		}
	}
	if _, err := readArt(plain.URL + "/cover.png"); err == nil {
		t.Error("plain http covers should be refused")
	}
}

func TestArtOnlyFromPublicAddresses(t *testing.T) {
	for address, ok := range map[string]bool{
		"93.184.215.14:443":       true,
		"[2606:4700::1111]:443":   true,
		"127.0.0.1:443":           false,
		"[::1]:443":               false,
		"192.168.0.1:443":         false,
		"10.0.0.1:443":            false,
		"169.254.169.254:443":     false,
		"100.100.100.100:443":     false,
		"[fd7a:115c:a1e0::1]:443": false,
		"0.0.0.0:443":             false,
	} {
		if got := publicOnly("tcp", address, nil) == nil; got != ok {
			t.Errorf("%s allowed = %v, want %v", address, got, ok)
		}
	}
}
