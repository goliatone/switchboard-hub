package providers

import (
	"fmt"

	"github.com/goliatone/switchboard-hub/internal/tunnel"
	"github.com/goliatone/switchboard-hub/internal/tunnel/providers/cloudflare"
	"github.com/goliatone/switchboard-hub/internal/tunnel/providers/tailscale"
)

func Registry() *tunnel.Registry {
	r := tunnel.NewRegistry()
	mustRegister(r, "cloudflare", func() tunnel.Provider { return cloudflare.New() })
	mustRegister(r, "tailscale", func() tunnel.Provider { return tailscale.New() })
	return r
}

func mustRegister(r *tunnel.Registry, name string, factory tunnel.Factory) {
	if err := r.Register(name, factory); err != nil {
		panic(fmt.Sprintf("register provider %q: %v", name, err))
	}
}
