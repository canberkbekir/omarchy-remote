package cli

import (
	"strings"

	"github.com/canberkbekir/omarchy-remote/internal/config"
	"github.com/canberkbekir/omarchy-remote/internal/network"
	"github.com/canberkbekir/omarchy-remote/internal/qrtext"
)

// ConnectChoice is one way to open the app, with its QR code.
type ConnectChoice struct {
	Label string   `json:"label"`
	URL   string   `json:"url"`
	QR    []string `json:"qr"`
}

// ConnectChoices are the addresses the bar card and the tray page offer, the
// first one shown by default: with access "both", Tailscale and the home
// network. A Tailscale address whose name isn't known yet is left out while
// another address works.
func ConnectChoices(dir string, c config.Config) []ConnectChoice {
	s := host{Dir: dir, Existing: c, LANAddress: network.LANAddress()}
	if config.ViaTailscale(c.Access) {
		tool, _ := tailscaleTool()
		s.TailscaleName, _ = tailscaleName(tool)
	}
	return connectChoices(appAddresses(s, answers{Access: c.Access}))
}

func connectChoices(addresses []Address) []ConnectChoice {
	var choices []ConnectChoice
	for _, a := range addresses {
		if !strings.Contains(a.URL, "<") {
			choices = append(choices, ConnectChoice{Label: a.Label, URL: a.URL, QR: qrtext.Rows(a.URL)})
		}
	}
	if len(choices) == 0 && len(addresses) > 0 {
		a := addresses[0]
		choices = append(choices, ConnectChoice{Label: a.Label, URL: a.URL, QR: qrtext.Rows(a.URL)})
	}
	return choices
}
