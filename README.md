# Omarchy Remote

Control your [Omarchy](https://omarchy.org) PC from your phone. Omarchy Remote is a small Go server with a web app that you add to your iPhone (or Android) Home Screen, where it opens like a regular app.

<p>
  <img src="docs/display.png" width="260" alt="Display tab with Screens off, Night light and per-monitor brightness">
  <img src="docs/input.png" width="260" alt="Input tab with the trackpad">
</p>

## Features

- **Control:** volume slider and mute, audio output picker, Now Playing with cover art and media keys, plus Lock, Reboot and Shut Down.
- **Display:** turn the screens off, toggle the night light, and set the brightness of all monitors or of each one.
- **Input:** hold the phone upright for a trackpad (tap to click, two-finger scroll, a scroll strip, left and right click) or turn it sideways for a full keyboard. You can also type text into the focused window and send or fetch the clipboard.
- **Themes:** follows your Omarchy theme, or use your own CSS. Changes show up on the phone without a restart.
- **One passcode** that is created on first run, with a one-minute lockout after five wrong tries.
- **Single binary** with the web app embedded. It needs no root access: keyboard and mouse input go through `/dev/uinput`, which systemd gives to the user signed in at the computer.

## Requirements

- Omarchy (Hyprland, PipeWire)
- Go 1.27 or newer to build it (`mise use -g go@latest` works)
- `playerctl` for the media controls. Omarchy doesn't include it: `sudo pacman -S playerctl`
- An iPhone on iOS 16.2 or newer, or a recent Android browser

## Install

```bash
git clone https://github.com/canberkbekir/omarchy-remote.git
cd omarchy-remote
./install.sh
```

The script builds the server into `~/.local/bin`, installs the `pc-remote` helper next to it, and starts the `pc-remote-web` systemd user service. It prints your passcode at the end. You can read the passcode again at any time from `~/.config/pc-remote/passcode`.

Then on your phone:

1. Open `http://<your-pc-ip>:8377` in Safari (or `https://` with Tailscale, as described below).
2. Tap **Share**, then **Add to Home Screen**.
3. Open the new icon and enter the passcode.

If you use a firewall, allow the port from your home network only, for example `sudo ufw allow from 192.168.0.0/24 to any port 8377 proto tcp`.

## Security

This app can type on your keyboard and shut down your computer, so treat access to it like access to the PC.

- **Never expose port 8377 to the internet.** Do not forward it on your router.
- On plain `http://`, your home network can see the passcode. The recommended setup is [Tailscale](https://tailscale.com), which encrypts the connection and also works away from home:

  ```bash
  sudo tailscale serve --bg 8377
  ```

  Then set `access = "tailscale"` in the config file (below) and restart the service. The app then listens only on this PC, so Tailscale is the only way in. Open `https://<your-pc>.<your-tailnet>.ts.net` on your phone.

## Configuration

Settings live in `~/.config/pc-remote/config.toml`. Every setting is optional, and without the file the app uses the defaults below.

```toml
name   = "Studio PC"   # title at the top of the app (default: the computer's name)
theme  = "omarchy"     # see Themes below (default: "default")
access = "tailscale"   # "lan" or "tailscale" (default: "lan")
port   = 8377
```

| `access` | Listens on | Use it when |
| --- | --- | --- |
| `lan` | every network interface | you open `http://<your-pc-ip>:8377` on home Wi-Fi |
| `tailscale` | this PC only (`127.0.0.1`) | you use `tailscale serve`, as described in Security |

Restart the service after changing the config: `systemctl --user restart pc-remote-web`. If a setting is misspelled, the service stops and `journalctl --user -u pc-remote-web` shows which one.

For advanced setups, `listen = "100.64.0.1:8377"` (or the `PC_REMOTE_ADDR` environment variable) sets the exact address and overrides `access` and `port`.

The passcode is kept separately in `~/.config/pc-remote/passcode`, so you can share your config without sharing the passcode. Delete that file and restart the service to get a new passcode.

## Themes

<img src="docs/theme-omarchy.png" width="260" align="right" alt="The app in the Omarchy theme, using Tokyo Night colors">

The app loads three stylesheets in order, and each one can override the one before:

1. **`base.css`**: layout and structure, built in.
2. **The theme** chosen with `theme = "..."`: colors and fonts.
3. **`~/.config/pc-remote/user.css`**: anything you like, up to a full restyle.

Built-in themes:

- `default`: dark with a lime accent.
- `omarchy`: follows your current Omarchy theme. Change the theme on the PC and the phone follows within a few seconds.

**To make your own theme**, copy [`themes/default.css`](themes/default.css) to `~/.config/pc-remote/themes/<name>.css`, change the colors, and set `theme = "<name>"`. A file in that folder with the same name as a built-in theme replaces it.

Edits to theme files and `user.css` reach an open app within a few seconds, with no restart. Only changing `theme` in `config.toml` needs a restart.

<br clear="right">

### Variables

| Variable | Used for |
| --- | --- |
| `--ground` | page background |
| `--panel` | buttons and cards |
| `--line` | borders |
| `--ink` | main text |
| `--soft` | secondary text |
| `--dim` | hints and unlit ticks |
| `--accent` | sliders, active buttons and the status dot |
| `--risk` | Shut Down and other warnings |
| `--font`, `--mono` | text and number fonts |

A few shades are mixed from these automatically: `--pad`, `--pressed`, `--edge`, `--risk-edge`, `--muted-tick` and `--scrim` (the backdrop behind sheets). Set them yourself to override them.

Every color in Omarchy's `colors.toml` is also available as `--omarchy-<name>`, with underscores turned into dashes (for example `--omarchy-bright-blue`), so your own themes can use Omarchy colors too.

### Stable names for `user.css`

These IDs and classes won't be renamed without a note in the release, so `user.css` can rely on them:

| Name | What it is |
| --- | --- |
| `#login`, `#control` | the sign-in screen and the main app |
| `#pc-name` | the title |
| `#view-control`, `#view-display`, `#view-input` | the three tabs' pages |
| `#tab-control`, `#tab-display`, `#tab-input`, `.tab` | the tab buttons |
| `#volume`, `#monitors` | the volume section and the brightness list |
| `#lock`, `#reboot`, `#shutdown` | the power buttons |
| `#pad`, `#keyboard` | the trackpad and the sideways keyboard |
| `.key`, `.primary`, `.sheet` | buttons, main actions and pop-up sheets |

Anything else in the page may change between versions.

## Uninstall

```bash
systemctl --user disable --now pc-remote-web
rm ~/.config/systemd/user/pc-remote-web.service ~/.local/bin/pc-remote-web ~/.local/bin/pc-remote
rm -r ~/.config/pc-remote   # your passcode, config and themes
```

## The `pc-remote` helper

`scripts/pc-remote` runs a command inside your running Hyprland session, for example `pc-remote omarchy-system-lock`. The server uses it for session commands. It also works over SSH, so you can use it from an iPhone Shortcuts **Run Script Over SSH** action as well.

## Development

```bash
go test ./...
go build -o pc-remote-web . && ./pc-remote-web
```

The web app is `web/index.html` and `web/base.css`. They are embedded into the binary at build time, along with the built-in themes in `themes/`.

New themes are welcome as pull requests: add `themes/<name>.css` with a screenshot.

## License

[MIT](LICENSE)
