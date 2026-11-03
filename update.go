package main

import (
	"archive/tar"
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

// parseVersion reads a release version like v1.2.3. Builds from source, like
// v1.2.3-4-gabcdef or dev, aren't releases.
func parseVersion(v string) ([3]int, bool) {
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

// newerVersion reports whether release a is newer than release b.
func newerVersion(a, b string) bool {
	x, okA := parseVersion(a)
	y, okB := parseVersion(b)
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

// latestVersion asks GitHub for the newest release. releases/latest redirects
// to the release's page, which ends in its tag.
func latestVersion(timeout time.Duration) (string, error) {
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
	if _, ok := parseVersion(tag); !ok || resp.StatusCode/100 != 3 {
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
	return io.ReadAll(io.LimitReader(resp.Body, 128<<20))
}

// releaseFiles downloads a release for this PC, checks it against the
// release's checksums, and returns the files in it by name.
func releaseFiles(tag string) (map[string][]byte, error) {
	name := "omarchy-remote-linux-" + runtime.GOARCH
	archive, err := download(releases + "/download/" + tag + "/" + name + ".tar.gz")
	if err != nil {
		return nil, err
	}
	sums, err := download(releases + "/download/" + tag + "/checksums.txt")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if !strings.Contains(string(sums), hex.EncodeToString(sum[:])+"  "+name+".tar.gz") {
		return nil, errors.New("the download doesn't match the release's checksum")
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	r := tar.NewReader(gz)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || path.Dir(h.Name) != name {
			continue
		}
		if files[path.Base(h.Name)], err = io.ReadAll(io.LimitReader(r, 64<<20)); err != nil {
			return nil, err
		}
	}
	if files["pc-remote-web"] == nil {
		return nil, errors.New("the release has no pc-remote-web")
	}
	return files, nil
}

// replaceFile swaps in a new version of an executable. The running server
// keeps the old one until it restarts.
func replaceFile(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// installRelease puts the release's programs in dir, next to the current ones.
func installRelease(dir string, files map[string][]byte) error {
	if err := replaceFile(filepath.Join(dir, "pc-remote-web"), files["pc-remote-web"]); err != nil {
		return err
	}
	if helper := files["pc-remote"]; helper != nil {
		return replaceFile(filepath.Join(dir, "pc-remote"), helper)
	}
	return nil
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

	current := versionString()
	latest, err := latestVersion(10 * time.Second)
	if err != nil {
		fmt.Fprintln(out, "Couldn't check for updates:", err)
		return 1
	}
	if _, ok := parseVersion(current); !ok {
		fmt.Fprintf(out, "This is %s, built from source. The latest release is %s.\n", current, latest)
		fmt.Fprintln(out, "Update it the way you built it: "+st.code.Render("git pull && ./install.sh"))
		return 1
	}
	if !newerVersion(latest, current) {
		fmt.Fprintf(out, "You have the latest version, %s.\n", current)
		return 0
	}
	if check {
		fmt.Fprintf(out, "%s is out; you have %s. Run %s to get it.\n", latest, current, st.code.Render("pc-remote update"))
		return 0
	}

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(out, "Can't find this program:", err)
		return 1
	}
	dir := filepath.Dir(exe)
	if !writable(dir) {
		fmt.Fprintf(out, "Can't write to %s, so this copy was likely installed by a package manager. Update it with that.\n", dir)
		return 1
	}

	fmt.Fprintf(out, "Downloading %s\n", latest)
	files, err := releaseFiles(latest)
	if err == nil {
		err = installRelease(dir, files)
	}
	if err != nil {
		fmt.Fprintln(out, st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	fmt.Fprintf(out, "%s Updated %s → %s in %s\n", st.good.Render("✓"), current, latest, tildePath(dir))
	if _, err := runCommand("systemctl", "--user", "is-active", "--quiet", "pc-remote-web.service"); err != nil {
		return 0
	}
	if _, err := runCommand("systemctl", "--user", "restart", "pc-remote-web.service"); err != nil {
		fmt.Fprintln(out, "Restart the service to use it: systemctl --user restart pc-remote-web")
		return 1
	}
	fmt.Fprintln(out, "Restarted the service. Phones reconnect on their own.")
	return 0
}
