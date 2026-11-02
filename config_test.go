package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("PC_REMOTE_ADDR", "")
	c, err := loadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Theme != "default" || c.Access != "lan" || c.Name == "" {
		t.Errorf("config = %+v", c)
	}
	if got := c.listenAddr(); got != "0.0.0.0:8377" {
		t.Errorf("listenAddr = %q", got)
	}
}

func TestLoadConfigListen(t *testing.T) {
	tests := []struct{ text, env, want string }{
		{"access = \"tailscale\"", "", "127.0.0.1:8377"},
		{"access = \"tailscale\"\nport = 9000", "", "127.0.0.1:9000"},
		{"listen = \"100.64.0.1:8377\"\naccess = \"tailscale\"", "", "100.64.0.1:8377"},
		{"listen = \"100.64.0.1:8377\"", "127.0.0.1:1234", "127.0.0.1:1234"},
	}
	for _, test := range tests {
		t.Setenv("PC_REMOTE_ADDR", test.env)
		c, err := loadConfig(writeConfig(t, test.text))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.listenAddr(); got != test.want {
			t.Errorf("%q: listenAddr = %q, want %q", test.text, got, test.want)
		}
	}
}

func TestLoadConfigRejects(t *testing.T) {
	tests := map[string]string{
		"name = \"x\"\ntheem = \"omarchy\"": "unknown setting",
		"access = \"internet\"":             "access must be",
		"theme = \"../secrets\"":            "theme",
		"port = 70000":                      "port must be",
		"name = ":                           "config.toml",
	}
	for text, want := range tests {
		_, err := loadConfig(writeConfig(t, text))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to mention %q", text, err, want)
		}
	}
}
