package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// releases is where `pc-remote update` looks; tests point it elsewhere.
var releases = "https://github.com/canberkbekir/omarchy-remote/releases"

// ParseVersion reads a release version like v1.2.3. Builds from source, like
// v1.2.3-4-gabcdef or dev, aren't releases.
func ParseVersion(v string) ([3]int, bool) {
	var n [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// NewerVersion reports whether release a is newer than release b.
func NewerVersion(a, b string) bool {
	x, okA := ParseVersion(a)
	y, okB := ParseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// LatestVersion asks GitHub for the newest release. releases/latest redirects
// to the release's page, which ends in its tag.
func LatestVersion(timeout time.Duration) (string, error) {
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Head(releases + "/latest")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	tag := path.Base(resp.Header.Get("Location"))
	if _, ok := ParseVersion(tag); !ok || resp.StatusCode/100 != 3 {
		return "", fmt.Errorf("can't tell the latest version (%s)", resp.Status)
	}
	return tag, nil
}

func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return readAll(resp.Body, 128<<20)
}

// maxFileSize caps each file in a release; tests lower it.
var maxFileSize int64 = 64 << 20

// readAll reads r, failing instead of cutting it short past limit bytes.
func readAll(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("the download is too big (over %d MB)", limit>>20)
	}
	return data, err
}

// checksumMatches reports whether a sha256sum listing has data's checksum
// on the line for name.
func checksumMatches(sums, name string, data []byte) bool {
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.EqualFold(f[0], want) && strings.TrimPrefix(f[1], "*") == name {
			return true
		}
	}
	return false
}

// assetFor names the release archive for an OS and CPU.
func assetFor(goos, goarch string) (string, error) {
	switch {
	case goos == "linux" && (goarch == "amd64" || goarch == "arm64"):
		return "omarchy-remote-linux-" + goarch + ".tar.gz", nil
	case goos == "windows" && goarch == "amd64":
		return "pc-remote-windows-amd64.zip", nil
	}
	return "", fmt.Errorf("there's no release for %s/%s", goos, goarch)
}

// releaseAsset names the release archive for this PC.
func releaseAsset() (string, error) { return assetFor(runtime.GOOS, runtime.GOARCH) }

// archiveFolder is the folder inside an archive that holds the release.
func archiveFolder(asset string) string {
	return strings.TrimSuffix(strings.TrimSuffix(asset, ".tar.gz"), ".zip")
}

// serverFile is the server's file name in a release.
func serverFile() string {
	if runtime.GOOS == "windows" {
		return "pc-remote-web.exe"
	}
	return "pc-remote-web"
}

// releaseFiles downloads a release for this PC, checks it against the
// release's checksums, and returns the files in it by name.
func releaseFiles(tag string) (map[string][]byte, error) {
	name, err := releaseAsset()
	if err != nil {
		return nil, err
	}
	archive, err := download(releases + "/download/" + tag + "/" + name)
	if err != nil {
		return nil, err
	}
	sums, err := download(releases + "/download/" + tag + "/checksums.txt")
	if err != nil {
		return nil, err
	}
	if !checksumMatches(string(sums), name, archive) {
		return nil, errors.New("the download doesn't match the release's checksum")
	}
	files, err := unpack(name, archive)
	if err != nil {
		return nil, err
	}
	if files[serverFile()] == nil {
		return nil, errors.New("the release has no " + serverFile())
	}
	return files, nil
}

// unpack returns the regular files in the archive's release folder by name.
// Anything else, like a path that climbs out of the folder, is skipped.
func unpack(asset string, archive []byte) (map[string][]byte, error) {
	folder := archiveFolder(asset)
	files := map[string][]byte{}
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if !f.Mode().IsRegular() || path.Dir(f.Name) != folder {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := readAll(rc, maxFileSize)
			rc.Close()
			if err != nil {
				return nil, err
			}
			files[path.Base(f.Name)] = data
		}
		return files, nil
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	r := tar.NewReader(gz)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || path.Dir(h.Name) != folder {
			continue
		}
		if files[path.Base(h.Name)], err = readAll(r, maxFileSize); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// Executable is this program's file; tests point it elsewhere.
var Executable = func() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	return exe, err
}

// writable reports whether files can be added to dir.
func writable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".pc-remote-update-*")
	if err != nil {
		return false
	}
	probe.Close()
	os.Remove(probe.Name())
	return true
}

// runUpdate is `pc-remote update`.
func runUpdate(args []string, out io.Writer) int {
	st := newStyles(out)
	check := len(args) == 1 && args[0] == "--check"
	if len(args) > 0 && !check {
		fmt.Fprintln(out, "Usage: pc-remote update [--check]")
		return 2
	}

	current := VersionString()
	latest, err := LatestVersion(10 * time.Second)
	if err != nil {
		fmt.Fprintln(out, "Couldn't check for updates:", err)
		return 1
	}
	if _, ok := ParseVersion(current); !ok {
		fmt.Fprintf(out, "This is %s, built from source. The latest release is %s.\n", current, latest)
		fmt.Fprintln(out, "Update it the way you built it: "+st.code.Render("git pull && ./install.sh"))
		return 1
	}
	if !NewerVersion(latest, current) {
		fmt.Fprintf(out, "You have the latest version, %s.\n", current)
		return 0
	}
	if check {
		fmt.Fprintf(out, "%s is out; you have %s. Run %s to get it.\n", latest, current, st.code.Render("pc-remote update"))
		return 0
	}

	exe, err := Executable()
	if err != nil {
		fmt.Fprintln(out, "Can't find this program:", err)
		return 1
	}
	dir := filepath.Dir(exe)
	if hint := packageManaged(exe); hint != "" {
		fmt.Fprintln(out, hint)
		return 1
	}
	if !writable(dir) {
		fmt.Fprintf(out, "Can't write to %s, so this copy was likely installed by a package manager. Update it with that.\n", dir)
		return 1
	}

	fmt.Fprintf(out, "Downloading %s\n", latest)
	files, err := releaseFiles(latest)
	if err != nil {
		fmt.Fprintln(out, st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	return installUpdate(st, dir, current, latest, files, out)
}
