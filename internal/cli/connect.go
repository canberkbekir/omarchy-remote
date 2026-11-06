package cli

import (
	"github.com/canberkbekir/omarchy-remote/internal/config"
	"github.com/canberkbekir/omarchy-remote/internal/network"
)

// ConnectAddress is the first address setup gives for the app: the one the
// bar card and the tray page show as a QR code.
func ConnectAddress(dir string, c config.Config) Address {
	s := host{Dir: dir, Existing: c, LANAddress: network.LANAddress()}
	if config.ViaTailscale(c.Access) {
		tool, _ := tailscaleTool()
		s.TailscaleName, _ = tailscaleName(tool)
	}
	return appAddresses(s, answers{Access: c.Access})[0]
}
