package app

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"strings"

	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

const IngressModeTunnelOnly = "tunnel-only"

// ErrIngressSafetyUnavailable keeps isolated clients off the legacy mutation
// path until coordinated storage and owned provider lifecycle support is wired.
var ErrIngressSafetyUnavailable = errors.New("isolated ingress requires coordinated storage and owned provider lifecycle support; keep tunnel operations disabled until that support is available")

// CredentialResolver resolves an opaque host-owned reference. Implementations
// must not log secrets. The caller owns the returned buffer and its lifetime.
type CredentialResolver interface {
	ResolveCredential(context.Context, string) ([]byte, error)
}

// IngressOptions opts into isolated library composition. Nil retains legacy
// configuration resolution. Paths are explicit and never derived from HOME.
type IngressOptions struct {
	Helpers        map[string]string
	HelperResolver ingress.HelperResolver `json:"-" yaml:"-"`
	Mode           string
	StateDir       string
	RuntimeDir     string
	Credentials    CredentialResolver `json:"-" yaml:"-"`
}

func validateIngressOptions(configPath string, opts *IngressOptions) error {
	if opts == nil {
		return nil
	}
	if opts.Mode != IngressModeTunnelOnly {
		return errors.New("ingress mode must be tunnel-only")
	}
	for _, item := range []struct{ name, value string }{
		{"config path", configPath}, {"state directory", opts.StateDir}, {"runtime directory", opts.RuntimeDir},
	} {
		if strings.TrimSpace(item.value) == "" || !filepath.IsAbs(item.value) || strings.ContainsRune(item.value, 0) {
			return errors.New("isolated ingress requires an explicit absolute " + item.name)
		}
	}
	state, runtime := filepath.Clean(opts.StateDir), filepath.Clean(opts.RuntimeDir)
	// Runtime cleanup must never encompass durable state or its config. Keep
	// both directory trees separate; filesystem ownership checks belong to the
	// coordinated store/runtime implementation, before it creates any artifacts.
	if pathsOverlap(state, runtime) || pathsOverlap(filepath.Clean(configPath), runtime) {
		return errors.New("isolated ingress runtime directory must be separate from durable state and config")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) || a == string(filepath.Separator) || b == string(filepath.Separator)
}

func (s *Service) requireIngressMutationSupport() error {
	if err := validateIngressOptions(s.configPath, s.ingress); err != nil {
		return err
	}
	if s.ingress != nil {
		return ErrIngressSafetyUnavailable
	}
	return nil
}

func cloneIngressOptions(opts *IngressOptions) IngressOptions {
	out := *opts
	out.Helpers = maps.Clone(opts.Helpers)
	return out
}
