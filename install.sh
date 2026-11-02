#!/bin/bash
# Builds PC Remote and runs it as a systemd user service.
set -euo pipefail

cd "$(dirname "$0")"
bin="$HOME/.local/bin"
config="${XDG_CONFIG_HOME:-$HOME/.config}/pc-remote"

if ! command -v go >/dev/null; then
  echo "Go is needed to build PC Remote. Install it with: mise use -g go@latest" >&2
  exit 1
fi
for tool in wpctl hyprctl playerctl wtype wl-copy; do
  command -v "$tool" >/dev/null || echo "Note: $tool is not installed, so some buttons won't work. See Requirements in the README."
done

mkdir -p "$bin" "$HOME/.config/systemd/user"
go build -o "$bin/pc-remote-web" .
install -m 755 scripts/pc-remote "$bin/pc-remote"
install -m 644 systemd/pc-remote-web.service "$HOME/.config/systemd/user/pc-remote-web.service"

systemctl --user daemon-reload
systemctl --user enable pc-remote-web.service
systemctl --user restart pc-remote-web.service

# The passcode is created on first start; a bad config.toml stops the service instead.
for _ in 1 2 3 4 5; do
  [[ -s "$config/passcode" ]] && break
  sleep 1
done
sleep 1
if ! systemctl --user is-active --quiet pc-remote-web.service; then
  echo "PC Remote did not start. The last log lines:" >&2
  journalctl --user -u pc-remote-web.service -n 5 --no-pager >&2
  exit 1
fi

echo
echo "PC Remote is running. Settings: $config/config.toml (see the README)."
echo "Passcode: $(cat "$config/passcode")"
