package switchboard

import (
	"context"
	"maps"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

const IngressModeTunnelOnly = "tunnel-only"

// ErrIngressSafetyUnavailable is returned before isolated mutations while the
// coordinated store and owned provider lifecycle integration is unavailable.
var ErrIngressSafetyUnavailable = app.ErrIngressSafetyUnavailable

var ErrIngressProviderFailure = app.ErrIngressProviderFailure

// CredentialResolver reads an opaque reference from the host's private store.
// Returned bytes belong to the caller. Implementations must not log secrets.
type CredentialResolver interface {
	ResolveCredential(context.Context, string) ([]byte, error)
}

// IngressOptions selects explicit isolated library configuration. Mutations
// use the default coordinated store (or an injected compatible store) and an
// OwnedIngressProvider. Tailscale requires explicit LocalAPI settings; the
// built-in Cloudflare adapter requires dedicated single-writer ownership and
// the explicit per-session connector supervisor. Real-provider adoption remains
// subject to validation on the selected immutable candidate.
type IngressOptions struct {
	Helpers        map[string]string
	HelperResolver ingress.HelperResolver `json:"-" yaml:"-"`
	Mode           string
	StateDir       string
	RuntimeDir     string
	Credentials    CredentialResolver `json:"-" yaml:"-"`
}

func toInternalIngressOptions(opts *IngressOptions) *app.IngressOptions {
	if opts == nil {
		return nil
	}
	return &app.IngressOptions{Helpers: maps.Clone(opts.Helpers), HelperResolver: opts.HelperResolver, Mode: opts.Mode, StateDir: opts.StateDir, RuntimeDir: opts.RuntimeDir, Credentials: opts.Credentials}
}
