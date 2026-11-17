package tailscale

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

func TestInitFailsWhenBinaryMissing(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{}); err == nil {
		t.Fatal("expected binary missing error")
	}
}

func TestInitFailsWhenNeedsLogin(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/tailscale", nil }),
		WithCommandRunner(func(context.Context, string, ...string) (string, error) {
			return `{"BackendState":"NeedsLogin"}`, nil
		}),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{}); err == nil {
		t.Fatal("expected needs-login error")
	}
}

func TestEnsureEndpointFromPublicHost(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/tailscale", nil }),
		WithCommandRunner(func(_ context.Context, _ string, args ...string) (string, error) {
			if strings.HasPrefix(strings.Join(args, " "), "status --json") {
				return `{"BackendState":"Running","Self":{"DNSName":"machine.ts.net."}}`, nil
			}
			return "", nil
		}),
	)
	ep, err := p.EnsureEndpoint(context.Background(), tunnel.EndpointRequest{
		Name:       "esign",
		PublicHost: "my-mac.ts.net",
	})
	if err != nil {
		t.Fatalf("EnsureEndpoint returned error: %v", err)
	}
	if ep.ID != "my-mac.ts.net" {
		t.Fatalf("unexpected endpoint id: %q", ep.ID)
	}
}

func TestLegacyLifecycleRefusesUnownedMutation(t *testing.T) {
	calls := 0
	p := New(WithCommandRunner(func(context.Context, string, ...string) (string, error) { calls++; return "", nil }))
	if _, err := p.Start(context.Background(), tunnel.StartRequest{}); err == nil {
		t.Fatal("legacy start accepted")
	}
	if err := p.Stop(context.Background(), "unknown-session"); err == nil {
		t.Fatal("legacy cleanup accepted")
	}
	if calls != 0 {
		t.Fatal("unowned lifecycle invoked CLI")
	}
}

func TestExtractLocalPort(t *testing.T) {
	p, err := extractLocalPort("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("extractLocalPort returned error: %v", err)
	}
	if p != 3000 {
		t.Fatalf("unexpected port: %d", p)
	}
}
