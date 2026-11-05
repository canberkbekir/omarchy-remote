package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v0.1.3", "v0.1.2", true},
		{"v0.2.0", "v0.1.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.1.2", "v0.1.2", false},
		{"v0.1.1", "v0.1.2", false},
		{"v0.1.3", "dev", false},
		{"v0.1.3", "v0.1.2-3-gabcdef0", false},
		{"0.1.3", "v0.1.2", false},
	}
	for _, tt := range tests {
		if got := newerVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("newerVersion(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

// fakeRelease serves a release like GitHub's, with the given checksums file.
func fakeRelease(t *testing.T, tag string, files map[string]string, sums func(archive []byte) string) {
	t.Helper()
	name := "omarchy-remote-linux-" + runtime.GOARCH
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for file, content := range files {
		tw.WriteHeader(&tar.Header{Name: name + "/" + file, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/download/"+tag+"/"+name+".tar.gz", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sums(archive)))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	old := releases
	releases = server.URL
	t.Cleanup(func() { releases = old })
}

func goodSums(archive []byte) string {
	sum := sha256.Sum256(archive)
	return "0000  install.sh\n" + hex.EncodeToString(sum[:]) + "  omarchy-remote-linux-" + runtime.GOARCH + ".tar.gz\n"
}

func TestUpdateRejectsBadChecksum(t *testing.T) {
	fakeRelease(t, "v9.9.9", map[string]string{"pc-remote-web": "tampered"}, func([]byte) string {
		return strings.Repeat("ab", 32) + "  omarchy-remote-linux-" + runtime.GOARCH + ".tar.gz\n"
	})
	if _, err := releaseFiles("v9.9.9"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("err = %v, want a checksum error", err)
	}
}
