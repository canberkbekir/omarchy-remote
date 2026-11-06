package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
		if got := NewerVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("newerVersion(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

// testAsset is this PC's release archive, which the fake release serves.
func testAsset(t *testing.T) string {
	t.Helper()
	asset, err := releaseAsset()
	if err != nil {
		t.Skip(err)
	}
	return asset
}

// fakeArchive packs files into a folder in a .tar.gz or .zip, like a release.
func fakeArchive(asset string, files map[string]string) []byte {
	folder := archiveFolder(asset)
	var buf bytes.Buffer
	if strings.HasSuffix(asset, ".zip") {
		zw := zip.NewWriter(&buf)
		for file, content := range files {
			w, _ := zw.Create(folder + "/" + file)
			w.Write([]byte(content))
		}
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for file, content := range files {
		tw.WriteHeader(&tar.Header{Name: folder + "/" + file, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeRelease serves a release like GitHub's, with the given checksums file.
func fakeRelease(t *testing.T, tag string, files map[string]string, sums func(archive []byte) string) {
	t.Helper()
	name := testAsset(t)
	archive := fakeArchive(name, files)

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/download/"+tag+"/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
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
	asset, _ := releaseAsset()
	return "0000  install.sh\n" + hex.EncodeToString(sum[:]) + "  " + asset + "\n"
}

func TestUpdateRejectsBadChecksum(t *testing.T) {
	fakeRelease(t, "v9.9.9", map[string]string{"pc-remote-web": "tampered"}, func([]byte) string {
		asset, _ := releaseAsset()
		return strings.Repeat("ab", 32) + "  " + asset + "\n"
	})
	if _, err := releaseFiles("v9.9.9"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("err = %v, want a checksum error", err)
	}
}

func TestReleaseAssetNames(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
		ok                 bool
	}{
		{"linux", "amd64", "omarchy-remote-linux-amd64.tar.gz", true},
		{"linux", "arm64", "omarchy-remote-linux-arm64.tar.gz", true},
		{"windows", "amd64", "pc-remote-windows-amd64.zip", true},
		{"windows", "arm64", "", false},
		{"darwin", "arm64", "", false},
	}
	for _, tt := range tests {
		got, err := assetFor(tt.goos, tt.goarch)
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("assetFor(%s, %s) = %q, %v", tt.goos, tt.goarch, got, err)
		}
	}
}

func TestUnpackKeepsOnlyTheReleaseFolder(t *testing.T) {
	for _, asset := range []string{"omarchy-remote-linux-amd64.tar.gz", "pc-remote-windows-amd64.zip"} {
		archive := fakeArchive(asset, map[string]string{"pc-remote-web": "server", "pc-remote-web.exe": "server", "ViGEmClient.dll": "dll"})
		files, err := unpack(asset, archive)
		if err != nil {
			t.Fatalf("%s: %v", asset, err)
		}
		if string(files["ViGEmClient.dll"]) != "dll" || len(files) != 3 {
			t.Errorf("%s: files = %v", asset, files)
		}
	}
	// A file outside the release folder, or climbing out of it, is ignored.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"evil.exe", "pc-remote-windows-amd64/../evil.exe", "other/pc-remote-web.exe"} {
		w, _ := zw.Create(name)
		w.Write([]byte("x"))
	}
	zw.Close()
	files, err := unpack("pc-remote-windows-amd64.zip", buf.Bytes())
	if err != nil || len(files) != 0 {
		t.Errorf("files = %v, %v; want none", files, err)
	}
}

func TestChecksumMatchesWholeLine(t *testing.T) {
	data := []byte("archive")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	tests := []struct {
		sums string
		want bool
	}{
		{"0000  install.sh\n" + hash + "  a.zip\n", true},
		{hash + " *a.zip\r\n", true},
		{"ff" + hash + "  a.zip\n", false},
		{hash + "  a.zip.sig\n", false},
		{hash + "  b/a.zip\n", false},
		{strings.ToUpper(hash) + "  a.zip\n", true},
	}
	for _, tt := range tests {
		if got := checksumMatches(tt.sums, "a.zip", data); got != tt.want {
			t.Errorf("checksumMatches(%q) = %v", tt.sums, got)
		}
	}
}

func TestUnpackRejectsOversizedFiles(t *testing.T) {
	old := maxFileSize
	maxFileSize = 4
	t.Cleanup(func() { maxFileSize = old })
	for _, asset := range []string{"omarchy-remote-linux-amd64.tar.gz", "pc-remote-windows-amd64.zip"} {
		archive := fakeArchive(asset, map[string]string{"pc-remote-web": "12345"})
		if _, err := unpack(asset, archive); err == nil || !strings.Contains(err.Error(), "too big") {
			t.Errorf("%s: err = %v, want too big", asset, err)
		}
	}
}
