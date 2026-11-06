//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateInstallsRelease(t *testing.T) {
	fakeRelease(t, "v9.9.9", map[string]string{"pc-remote-web": "new server", "pc-remote": "new helper", "README.md": "docs"}, goodSums)
	latest, err := LatestVersion(time.Second)
	if err != nil || latest != "v9.9.9" {
		t.Fatalf("latestVersion = %q, %v", latest, err)
	}
	files, err := releaseFiles(latest)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pc-remote-web"), []byte("old server"), 0o755)
	if err := installRelease(dir, files); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"pc-remote-web": "new server", "pc-remote": "new helper"} {
		path := filepath.Join(dir, file)
		data, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if string(data) != want || info.Mode().Perm() != 0o755 {
			t.Errorf("%s = %q, mode %v", file, data, info.Mode())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Error("installed a file that isn't a program")
	}
}
