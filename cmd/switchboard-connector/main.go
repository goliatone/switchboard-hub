// switchboard-connector supervises one private cloudflared child. It is not a
// daemon and has no provider API credentials or remote provisioning authority.
package main

import (
	"github.com/goliatone/switchboard-hub/internal/tunnel/providers/cloudflare"
	"os"
)

func main() { os.Exit(cloudflare.ConnectorSupervisorMain(os.Args[1:])) }
