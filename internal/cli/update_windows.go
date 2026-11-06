package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canberkbekir/omarchy-remote/internal/config"
)

// wingetID is the package's name in winget; see packaging/winget.
const wingetID = "canberkbekir.OmarchyRemote"

// updatedFiles are the release files that live next to the server. The rest
// of the release, like the README, isn't installed.
var updatedFiles = []string{"pc-remote-web.exe", "ViGEmClient.dll"}

// updateConfig reads the settings, to find the running server; tests fake it.
var updateConfig = func() (config.Config, error) {
	dir, err := config.Dir()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(dir)
}

// packageManaged tells winget installs to update with winget, which tracks
// the version it installed. Per-user installs live under
// %LOCALAPPDATA%\Microsoft\WinGet\Packages, machine-wide ones under
// %ProgramFiles%\WinGet\Packages.
func packageManaged(exe string) string {
	if strings.Contains(strings.ToLower(exe), `\winget\packages\`) {
		return "This copy was installed with winget. Update it with: winget upgrade " + wingetID
	}
	return ""
}

// installUpdate stops the running server, swaps in the release's files, and
// starts the Task Scheduler task again. The server has to stop first: its
// files can be renamed while it runs but not replaced, and stopServers finds
// it by its program's path, which the swap changes.
func installUpdate(st styles, dir, current, latest string, files map[string][]byte, out io.Writer) int {
	startHint := st.code.Render(`schtasks /Run /TN "` + taskName + `"`)
	c, configErr := updateConfig()
	running := configErr == nil && serviceRunning(c)
	if running {
		if err := stopTask(c.ListenAddr()); err != nil {
			fmt.Fprintln(out, st.bad.Render("✗")+" "+err.Error())
			startTask() // stopping may have ended it; bring the old version back
			return 1
		}
	}
	if err := installRelease(dir, files); err != nil {
		fmt.Fprintln(out, st.bad.Render("✗")+" "+err.Error())
		if running {
			if startTask() != nil {
				fmt.Fprintln(out, "Start the old version again with: "+startHint)
			}
		}
		return 1
	}
	fmt.Fprintf(out, "%s Updated %s → %s in %s\n", st.good.Render("✓"), current, latest, tildePath(dir))
	if configErr != nil {
		fmt.Fprintf(out, "Couldn't read the settings (%v), so a running server wasn't restarted. Restart it to use the update: %s\n", configErr, startHint)
		return 0
	}
	if !running {
		return 0
	}
	if err := startTask(); err != nil {
		fmt.Fprintln(out, "Start it to use it: "+startHint)
		return 1
	}
	fmt.Fprintln(out, "Restarted Omarchy Remote. Phones reconnect on their own.")
	return 0
}

// swap is a file installRelease replaced, and whether an older one was there.
type swap struct {
	path   string
	hadOld bool
}

// installRelease swaps the release's files into dir. When one fails, the
// ones already swapped go back, so the old version stays whole.
func installRelease(dir string, files map[string][]byte) error {
	var done []swap
	for _, name := range updatedFiles {
		data := files[name]
		if data == nil {
			continue
		}
		path := filepath.Join(dir, name)
		hadOld, err := swapFile(path, data)
		if err != nil {
			for i := len(done) - 1; i >= 0; i-- {
				var undo error
				if done[i].hadOld {
					undo = renameRetry(done[i].path+".old", done[i].path)
				} else {
					undo = os.Remove(done[i].path)
				}
				if undo != nil {
					err = fmt.Errorf("%w; putting %s back failed too, so reinstall Omarchy Remote: %v", err, filepath.Base(done[i].path), undo)
				}
			}
			return err
		}
		done = append(done, swap{path, hadOld})
	}
	return nil
}

// swapFile moves the current file to path.old, which Windows allows even
// while the file runs, and puts data in its place. It reports whether there
// was a current file.
func swapFile(path string, data []byte) (bool, error) {
	tmp, old := path+".new", path+".old"
	if err := WriteNew(tmp, data); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.Remove(tmp)
		return false, fmt.Errorf("can't remove %s, which an older Omarchy Remote may still be running from. Close it or restart Windows, then update again: %w", filepath.Base(old), err)
	}
	hadOld := true
	if err := os.Rename(path, old); errors.Is(err, fs.ErrNotExist) {
		hadOld = false
	} else if err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := renameRetry(tmp, path); err != nil {
		if hadOld {
			renameRetry(old, path)
		}
		os.Remove(tmp)
		return false, err
	}
	return hadOld, nil
}

// WriteNew writes a file that must not exist yet, so it never writes through
// a link someone left at that path.
func WriteNew(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// renameRetry renames, retrying for a second: antivirus scanners often hold a
// freshly written exe open for a moment.
func renameRetry(from, to string) error {
	var err error
	for range 5 {
		if err = os.Rename(from, to); err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// RemoveOldFiles deletes what the last update moved aside. The server calls
// it when it starts, once the old version has stopped.
func RemoveOldFiles(exe string) {
	dir := filepath.Dir(exe)
	for _, name := range updatedFiles {
		os.Remove(filepath.Join(dir, name+".old"))
	}
}
