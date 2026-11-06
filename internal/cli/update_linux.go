package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/canberkbekir/omarchy-remote/internal/run"
)

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

// packageManaged is a hint for copies a package manager updates; on Linux the
// writable check covers it.
func packageManaged(string) string { return "" }

// installUpdate puts the release in dir and restarts the service if it runs.
func installUpdate(st styles, dir, current, latest string, files map[string][]byte, out io.Writer) int {
	if err := installRelease(dir, files); err != nil {
		fmt.Fprintln(out, st.bad.Render("✗")+" "+err.Error())
		return 1
	}
	fmt.Fprintf(out, "%s Updated %s → %s in %s\n", st.good.Render("✓"), current, latest, tildePath(dir))
	if _, err := run.Command("systemctl", "--user", "is-active", "--quiet", "pc-remote-web.service"); err != nil {
		return 0
	}
	if _, err := run.Command("systemctl", "--user", "restart", "pc-remote-web.service"); err != nil {
		fmt.Fprintln(out, "Restart the service to use it: systemctl --user restart pc-remote-web")
		return 1
	}
	fmt.Fprintln(out, "Restarted the service. Phones reconnect on their own.")
	return 0
}

// RemoveOldFiles cleans up after an update; on Linux the rename leaves nothing.
func RemoveOldFiles(string) {}
