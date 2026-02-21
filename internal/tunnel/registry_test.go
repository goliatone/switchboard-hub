package tunnel

import (
	"context"
	"testing"
)

type mockProvider struct {
	name string
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Capabilities() Capabilities {
	return Capabilities{
		StableHostname:     true,
		HTTPForwarding:     true,
		HTTPSForwarding:    true,
		OAuthSuitable:      true,
		SupportsSSE:        true,
		SupportsWebSockets: true,
	}
}

func (m *mockProvider) Init(context.Context, ProviderConfig) error { return nil }
func (m *mockProvider) EnsureEndpoint(context.Context, EndpointRequest) (Endpoint, error) {
	return Endpoint{}, nil
}
func (m *mockProvider) Start(context.Context, StartRequest) (Session, error) { return Session{}, nil }
func (m *mockProvider) Stop(context.Context, string) error                   { return nil }
func (m *mockProvider) RemoveEndpoint(context.Context, string) error         { return nil }
func (m *mockProvider) Status(context.Context, string) (EndpointStatus, error) {
	return EndpointStatus{}, nil
}

var _ Provider = (*mockProvider)(nil)

func TestRegistryRegisterAndResolve(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("cloudflare", func() Provider {
		return &mockProvider{name: "cloudflare"}
	}); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	got, err := r.Resolve("CLOUDFLARE")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got.Name() != "cloudflare" {
		t.Fatalf("unexpected provider name: %q", got.Name())
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("cloudflare", func() Provider { return &mockProvider{name: "cloudflare"} }); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if err := r.Register("cloudflare", func() Provider { return &mockProvider{name: "cloudflare"} }); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistryProvidersSorted(t *testing.T) {
	r := NewRegistry()
	_ = r.Register("tailscale", func() Provider { return &mockProvider{name: "tailscale"} })
	_ = r.Register("cloudflare", func() Provider { return &mockProvider{name: "cloudflare"} })

	got := r.Providers()
	if len(got) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(got))
	}
	if got[0] != "cloudflare" || got[1] != "tailscale" {
		t.Fatalf("unexpected provider order: %#v", got)
	}
}
