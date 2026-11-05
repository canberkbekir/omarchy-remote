package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// desktopIntegration ties the server into the desktop: on Omarchy, the bar
// widget.
func desktopIntegration(*app, string) {
	syncBarPlugin()
}

// closeDesktopIntegration: the bar widget needs nothing at exit.
func closeDesktopIntegration() {}

// syncBarPlugin keeps an installed widget in step with this version, so an
// update reaches the bar too. The shell reloads changed plugin files itself.
func syncBarPlugin() {
	dir, err := barPluginDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return
	}
	if _, err := writeBarPlugin(dir, executable); err != nil {
		fmt.Fprintln(os.Stderr, "updating the bar widget:", err)
	}
}
