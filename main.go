// Small web app for controlling an Omarchy PC from a phone.
package main

import (
	"os"

	"github.com/canberkbekir/omarchy-remote/internal/cli"
	"github.com/canberkbekir/omarchy-remote/internal/server"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cli.Version = version
	if code, handled := cli.Run(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}
	server.Run()
}
