#!/bin/bash
# Installs Omarchy Remote into ~/.local/bin, then runs `pc-remote setup`.
# In a clone it builds from source. Piped from curl, it downloads the latest release:
#   curl -fsSL https://github.com/canberkbekir/omarchy-remote/releases/latest/download/install.sh | bash
set -euo pipefail

repo="canberkbekir/omarchy-remote"
bin="$HOME/.local/bin"
units="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
src="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [[ -f "$src/go.mod" && -f "$src/main.go" ]]; then
  if ! command -v go >/dev/null; then
    echo "Go is needed to build from source. Install it with: mise use -g go@latest" >&2
    echo "Or install the latest release instead: curl -fsSL https://github.com/$repo/releases/latest/download/install.sh | bash" >&2
    exit 1
  fi
  echo "Building from $src"
  files="$tmp/files"
  mkdir -p "$files"
  (cd "$src" && go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o "$files/pc-remote-web" .)
  cp "$src/scripts/pc-remote" "$src/systemd/pc-remote-web.service" "$files/"
else
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "There's no release for $(uname -m). Build from source instead: see the README." >&2; exit 1 ;;
  esac
  name="omarchy-remote-linux-$arch"
  echo "Downloading the latest release"
  curl -fsSL "https://github.com/$repo/releases/latest/download/$name.tar.gz" -o "$tmp/$name.tar.gz"
  curl -fsSL "https://github.com/$repo/releases/latest/download/checksums.txt" -o "$tmp/checksums.txt"
  (cd "$tmp" && sha256sum --check --ignore-missing --quiet checksums.txt)
  tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
  files="$tmp/$name"
fi

mkdir -p "$bin" "$units"
install -m 755 "$files/pc-remote-web" "$bin/pc-remote-web.new"
mv "$bin/pc-remote-web.new" "$bin/pc-remote-web"
install -m 755 "$files/pc-remote" "$bin/pc-remote"
install -m 644 "$files/pc-remote-web.service" "$units/pc-remote-web.service"
systemctl --user daemon-reload
echo "Installed $("$bin/pc-remote-web" version) into $bin"

# Setup checks the PC, asks a few questions, starts the service and shows the passcode.
# Piped from curl, stdin is the script, so the questions read from the terminal.
if [[ -t 1 ]] && (: </dev/tty) 2>/dev/null; then
  exec "$bin/pc-remote-web" setup </dev/tty
else
  exec "$bin/pc-remote-web" setup --yes
fi
