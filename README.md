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
- **One passcode** that is created on first run, with a one-minute lockout after five wrong tries.
- **Single binary** with the web app embedded. It needs no root access: keyboard and mouse input go through `/dev/uinput`, which systemd gives to the user signed in at the computer.

## Requirements

- Omarchy (Hyprland, PipeWire)
- Go 1.27 or newer to build it (`mise use -g go@latest` works)
- `playerctl` (media controls), `wtype` and `wl-clipboard` (typing and clipboard). Omarchy includes these.

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

  Then open `https://<your-pc>.<your-tailnet>.ts.net` on your phone. To stop listening on the local network, set `PC_REMOTE_ADDR=127.0.0.1:8377` in the service file (`systemctl --user edit pc-remote-web`) so only Tailscale can reach the app.

## Configuration

| Setting | Default | Purpose |
| --- | --- | --- |
| `PC_REMOTE_ADDR` | `0.0.0.0:8377` | Address and port the server listens on |
| `~/.config/pc-remote/passcode` | random, 10 characters | Delete the file and restart the service to get a new one |

## The `pc-remote` helper

`scripts/pc-remote` runs a command inside your running Hyprland session, for example `pc-remote omarchy-system-lock`. The server uses it for session commands. It also works over SSH, so you can use it from an iPhone Shortcuts **Run Script Over SSH** action as well.

## Development

```bash
go test ./...
go build -o pc-remote-web . && ./pc-remote-web
```

The web app is a single file, `web/index.html`, embedded into the binary at build time.

## License

[MIT](LICENSE)
