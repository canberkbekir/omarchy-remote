#!/bin/bash
# Builds PC Remote and runs it as a systemd user service.
set -euo pipefail

cd "$(dirname "$0")"
bin="$HOME/.local/bin"
mkdir -p "$bin" "$HOME/.config/systemd/user"

go build -o "$bin/pc-remote-web" .
install -m 755 scripts/pc-remote "$bin/pc-remote"
install -m 644 systemd/pc-remote-web.service "$HOME/.config/systemd/user/pc-remote-web.service"

systemctl --user daemon-reload
systemctl --user enable --now pc-remote-web.service
systemctl --user restart pc-remote-web.service

# The passcode is created on first start.
for _ in 1 2 3 4 5; do
  [[ -s "$HOME/.config/pc-remote/passcode" ]] && break
  sleep 1
done

echo
echo "PC Remote is running. Settings: ~/.config/pc-remote/config.toml (see the README)."
echo "Passcode: $(cat "$HOME/.config/pc-remote/passcode" 2>/dev/null || echo 'see ~/.config/pc-remote/passcode')"
