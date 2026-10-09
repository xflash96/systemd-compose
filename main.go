// systemd-compose: docker-compose verbs over systemd user units. The
// command line is internal/cli.
package main

import (
	_ "embed"
	"os"

	"github.com/xflash96/systemd-compose/internal/cli"
)

// version is set by a release build: -ldflags "-X main.version=v0.1.0".
var version string

// The manual and the key reference, for help: go install brings no man
// page, and help works offline.
var (
	//go:embed docs/systemd-compose.1
	manPage string
	//go:embed docs/config.example.yaml
	keys string
)

func main() {
	os.Exit(cli.Main(os.Args[1:], version, cli.Docs{Man: manPage, Keys: keys}))
}
