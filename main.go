// systemd-compose — docker-compose verbs over systemctl, with a
// project-local systemd-compose.yaml. See cli.go for the verbs.
package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	var ee exitError
	if errors.As(err, &ee) {
		os.Exit(ee.code)
	}
	fmt.Fprintln(os.Stderr, "systemd-compose:", err)
	os.Exit(1)
}
