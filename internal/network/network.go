// Package network finds this PC's addresses.
package network

import (
	"net"
)

// LANAddress is this PC's address on the home network: the source address the
// system would use for a public address. Dialing UDP sends nothing.
func LANAddress() string {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// Tailscale's IPv4 range; its IPv6 range is already private.
var _, TailnetRange, _ = net.ParseCIDR("100.64.0.0/10")
