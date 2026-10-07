// systemd-compose: docker-compose verbs over systemd user units. The
// command line is internal/cli.
package main

import (
	"os"

	"github.com/xflash96/systemd-compose/internal/cli"
)

// version is set by a release build: -ldflags "-X main.version=v0.1.0".
var version string

func main() {
	os.Exit(cli.Main(os.Args[1:], version))
}
