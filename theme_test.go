package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThemeBuiltinAndOverride(t *testing.T) {
	dir := t.TempDir()
	look, err := newThemes(dir, "default")
	if err != nil {
		t.Fatal(err)
	}
	look.colorFiles = nil
	if !strings.Contains(string(look.themeCSS()), "--accent: #C8F24A") {
		t.Error("built-in default theme not served")
	}

	writeFile(t, filepath.Join(dir, "themes", "default.css"), ":root { --accent: red; }")
	if got := string(look.themeCSS()); got != ":root { --accent: red; }" {
		t.Errorf("own theme should replace the built-in one, got %q", got)
	}
}

func TestThemeMissing(t *testing.T) {
	if _, err := newThemes(t.TempDir(), "nope"); err == nil {
		t.Error("expected an error for a missing theme")
	}
}

func TestOmarchyColors(t *testing.T) {
	dir := t.TempDir()
	colors := filepath.Join(dir, "colors.toml")
	writeFile(t, colors, `mode = "light"
accent = "#7aa2f7"
dark_background = "#13141c"
red = "red; } body { display: none"
"bad key;" = "#ffffff"
size = 3
`)
	look, err := newThemes(dir, "omarchy")
	if err != nil {
		t.Fatal(err)
	}
	look.colorFiles = []string{filepath.Join(dir, "missing.toml"), colors}
	css := string(look.themeCSS())
	for _, want := range []string{"--omarchy-mode: light;", "--omarchy-accent: #7aa2f7;", "--omarchy-dark-background: #13141c;", "var(--omarchy-accent"} {
		if !strings.Contains(css, want) {
			t.Errorf("theme.css is missing %q", want)
		}
	}
	for _, unwanted := range []string{"display: none", "bad key", "size"} {
		if strings.Contains(css, unwanted) {
			t.Errorf("theme.css should not contain %q", unwanted)
		}
	}
}

func TestThemeVersion(t *testing.T) {
	dir := t.TempDir()
	look, err := newThemes(dir, "default")
	if err != nil {
		t.Fatal(err)
	}
	look.colorFiles = nil
	before := look.version()
	if look.version() != before {
		t.Error("version should be stable")
	}
	writeFile(t, filepath.Join(dir, "user.css"), "h1 { color: red; }")
	if look.version() == before {
		t.Error("version should change when user.css changes")
	}
}
