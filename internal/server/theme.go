package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	builtin "github.com/canberkbekir/omarchy-remote/themes"
)

var (
	colorKeyPattern   = regexp.MustCompile(`^[a-z0-9_]+$`)
	colorValuePattern = regexp.MustCompile(`^#[0-9A-Fa-f]{3,8}$`)
)

// themes serves the stylesheets loaded after web/base.css: the chosen theme as
// /theme.css, then the owner's own user.css. Files are read on every request, so
// an edit shows up on the next page load without restarting the server.
type themes struct {
	dir        string   // ~/.config/pc-remote
	name       string   // from config.toml
	colorFiles []string // where Omarchy may keep the current theme's colors.toml
}

func newThemes(dir, name string) (*themes, error) {
	t := &themes{dir: dir, name: name}
	if home, err := os.UserHomeDir(); err == nil {
		stateDir := os.Getenv("XDG_STATE_HOME")
		if stateDir == "" {
			stateDir = filepath.Join(home, ".local", "state")
		}
		t.colorFiles = []string{
			filepath.Join(stateDir, "omarchy", "current", "theme", "colors.toml"),
			filepath.Join(home, ".config", "omarchy", "current", "theme", "colors.toml"),
		}
	}
	if _, err := t.themeFile(); err != nil {
		return nil, err
	}
	return t, nil
}

// themeFile prefers the owner's themes folder, so a built-in theme can be replaced.
func (t *themes) themeFile() ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(t.dir, "themes", t.name+".css"))
	if !errors.Is(err, os.ErrNotExist) {
		return data, err
	}
	data, err = builtin.Files.ReadFile(t.name + ".css")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("theme %q not found in %s or the built-in themes", t.name, filepath.Join(t.dir, "themes"))
	}
	return data, err
}

// omarchyColors turns Omarchy's colors.toml into --omarchy-* variables, which any
// theme can use. Values must look like colors, so the file can't inject other CSS.
func (t *themes) omarchyColors() []byte {
	for _, path := range t.colorFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var colors map[string]any
		if _, err := toml.Decode(string(data), &colors); err != nil {
			log.Printf("reading %s: %v", path, err)
			return nil
		}
		var b bytes.Buffer
		b.WriteString(":root {\n")
		for _, key := range slices.Sorted(maps.Keys(colors)) {
			value, ok := colors[key].(string)
			if !ok || !colorKeyPattern.MatchString(key) {
				continue
			}
			name := "--omarchy-" + strings.ReplaceAll(key, "_", "-")
			switch {
			case key == "mode" && (value == "light" || value == "dark"):
				fmt.Fprintf(&b, "  %s: %s;\n", name, value)
			case colorValuePattern.MatchString(value):
				fmt.Fprintf(&b, "  %s: %s;\n", name, value)
			}
		}
		b.WriteString("}\n\n")
		return b.Bytes()
	}
	return nil
}

func (t *themes) themeCSS() []byte {
	data, err := t.themeFile()
	if err != nil {
		// The file was removed after startup; base.css still has the default look.
		log.Print(err)
	}
	return append(t.omarchyColors(), data...)
}

func (t *themes) userCSS() []byte {
	data, err := os.ReadFile(filepath.Join(t.dir, "user.css"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("reading user.css: %v", err)
	}
	return data
}

// version changes whenever either stylesheet does, so the page knows to reload them.
func (t *themes) version() string {
	sum := sha256.New()
	sum.Write(t.themeCSS())
	sum.Write([]byte{0})
	sum.Write(t.userCSS())
	return hex.EncodeToString(sum.Sum(nil)[:6])
}

func (t *themes) handleTheme(w http.ResponseWriter, r *http.Request) {
	writeCSS(w, t.themeCSS())
}

func (t *themes) handleUser(w http.ResponseWriter, r *http.Request) {
	writeCSS(w, t.userCSS())
}

func writeCSS(w http.ResponseWriter, css []byte) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(css)
}
