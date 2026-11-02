#!/bin/bash
# Builds PC Remote, installs it into ~/.local/bin, then runs `pc-remote setup`.
set -euo pipefail

cd "$(dirname "$0")"
bin="$HOME/.local/bin"

if ! command -v go >/dev/null; then
  echo "Go is needed to build PC Remote. Install it with: mise use -g go@latest" >&2
  exit 1
fi

mkdir -p "$bin" "$HOME/.config/systemd/user"
go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o "$bin/pc-remote-web" .
install -m 755 scripts/pc-remote "$bin/pc-remote"
install -m 644 systemd/pc-remote-web.service "$HOME/.config/systemd/user/pc-remote-web.service"
systemctl --user daemon-reload

# Setup checks the PC, asks a few questions, starts the service and shows the passcode.
if [[ -t 0 && -t 1 ]]; then
  exec "$bin/pc-remote-web" setup
else
  exec "$bin/pc-remote-web" setup --yes
fi
