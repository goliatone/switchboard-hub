package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

func TestNormalizePublicHost(t *testing.T) {
	got, err := normalizePublicHost("https://esign-oauth.dev.example.com/")
	if err != nil {
		t.Fatalf("normalizePublicHost returned error: %v", err)
	}
	if got != "esign-oauth.dev.example.com" {
		t.Fatalf("unexpected normalized host: %q", got)
	}
}

func TestResolveProviderNameFallsBackToDefault(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	c.Tunnel.DefaultProvider = "cloudflare"
	got, err := resolveProviderName(c, "")
	if err != nil {
		t.Fatalf("resolveProviderName returned error: %v", err)
	}
	if got != "cloudflare" {
		t.Fatalf("unexpected provider name: %q", got)
	}
}

func TestTunnelProviderConfigIncludesTopLevelFields(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	c.Tunnel.Providers["cloudflare"] = config.TunnelProviderCfg{
		Enabled:   true,
		AccountID: "acct-1",
		Zone:      "dev.example.com",
		Values: map[string]string{
			"team": "dev",
		},
	}
	got := tunnelProviderConfig(c, "cloudflare")
	if got.Values["account_id"] != "acct-1" {
		t.Fatalf("expected account_id in config values: %#v", got.Values)
	}
	if got.Values["zone"] != "dev.example.com" {
		t.Fatalf("expected zone in config values: %#v", got.Values)
	}
}

type mockProvider struct{}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Capabilities() tunnel.Capabilities {
	return tunnel.Capabilities{
		StableHostname:  true,
		HTTPForwarding:  true,
		HTTPSForwarding: true,
		OAuthSuitable:   true,
	}
}
func (m *mockProvider) Init(context.Context, tunnel.ProviderConfig) error { return nil }
func (m *mockProvider) EnsureEndpoint(context.Context, tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	return tunnel.Endpoint{}, nil
}
func (m *mockProvider) Start(context.Context, tunnel.StartRequest) (tunnel.Session, error) {
	return tunnel.Session{ID: "s-1", StartedAt: time.Now()}, nil
}
func (m *mockProvider) Stop(context.Context, string) error { return nil }
func (m *mockProvider) RemoveEndpoint(context.Context, string) error {
	return nil
}
func (m *mockProvider) Status(context.Context, string) (tunnel.EndpointStatus, error) {
	return tunnel.EndpointStatus{Ready: true}, nil
}

func TestResolveProviderNameUnknownProvider(t *testing.T) {
	orig := providerRegistryFactory
	defer func() { providerRegistryFactory = orig }()
	providerRegistryFactory = func() *tunnel.Registry {
		r := tunnel.NewRegistry()
		_ = r.Register("mock", func() tunnel.Provider { return &mockProvider{} })
		return r
	}

	c := config.Default("test", "10.0.0.1")
	c.Tunnel.DefaultProvider = "unknown"
	if _, err := resolveProviderName(c, ""); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestIsIdempotentStopError(t *testing.T) {
	if !isIdempotentStopError(assertErr("session not found")) {
		t.Fatal("expected idempotent stop error")
	}
	if isIdempotentStopError(assertErr("hard failure")) {
		t.Fatal("did not expect idempotent classification")
	}
}

func TestApplyTunnelInitOptionsSetsCloudflareOriginCert(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := applyTunnelInitOptions(c, configPath, "cloudflare", TunnelInitOptions{
		OriginCert: "certs/cert.pem",
	})
	if err != nil {
		t.Fatalf("applyTunnelInitOptions returned error: %v", err)
	}
	got := c.Tunnel.Providers["cloudflare"].Values["origincert"]
	want := filepath.Join(filepath.Dir(configPath), "certs/cert.pem")
	if got != want {
		t.Fatalf("unexpected origin cert path: got=%q want=%q", got, want)
	}
}

func TestApplyTunnelInitOptionsSetsAPIModeFields(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := applyTunnelInitOptions(c, configPath, "cloudflare", TunnelInitOptions{
		Mode:        "api",
		AccountID:   "acct-1",
		ZoneID:      "zone-1",
		BaseDomain:  "tnl.example.com",
		APITokenEnv: "CF_TOKEN",
	})
	if err != nil {
		t.Fatalf("applyTunnelInitOptions returned error: %v", err)
	}
	cfg := c.Tunnel.Providers["cloudflare"]
	if cfg.Values["mode"] != "api" {
		t.Fatalf("expected mode=api, got %q", cfg.Values["mode"])
	}
	if cfg.Values["account_id"] != "acct-1" || cfg.AccountID != "acct-1" {
		t.Fatalf("expected account id set, got cfg=%#v", cfg)
	}
	if cfg.Values["zone_id"] != "zone-1" || cfg.Zone != "zone-1" {
		t.Fatalf("expected zone id set, got cfg=%#v", cfg)
	}
	if cfg.Values["base_domain"] != "tnl.example.com" {
		t.Fatalf("expected base_domain set, got cfg=%#v", cfg)
	}
	if cfg.Values["api_token_env"] != "CF_TOKEN" {
		t.Fatalf("expected api_token_env set, got cfg=%#v", cfg)
	}
}

func TestShouldAttemptCloudflareSetupForOriginCertMissing(t *testing.T) {
	err := &fakeActionableErr{
		details: tunnel.ActionableDetails{Code: "CF_ORIGIN_CERT_MISSING"},
	}
	if !shouldAttemptCloudflareSetup(nil, "cloudflare", TunnelInitOptions{Setup: true}, err) {
		t.Fatal("expected setup attempt")
	}
	if shouldAttemptCloudflareSetup(nil, "tailscale", TunnelInitOptions{Setup: true}, err) {
		t.Fatal("did not expect setup attempt for non-cloudflare provider")
	}
}

func TestResolveExposePublicHostFromBaseDomain(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	cfg := c.Tunnel.Providers["cloudflare"]
	if cfg.Values == nil {
		cfg.Values = map[string]string{}
	}
	cfg.Values["base_domain"] = "tnl.example.com"
	c.Tunnel.Providers["cloudflare"] = cfg

	host, err := resolveExposePublicHost(c, "cloudflare", "esign", "")
	if err != nil {
		t.Fatalf("resolveExposePublicHost returned error: %v", err)
	}
	if host != "esign.tnl.example.com" {
		t.Fatalf("unexpected host: %q", host)
	}
}

func assertErr(msg string) error { return &staticErr{msg: msg} }

type staticErr struct{ msg string }

func (e *staticErr) Error() string { return e.msg }

type fakeActionableErr struct {
	details tunnel.ActionableDetails
}

func (e *fakeActionableErr) Error() string { return "fake actionable error" }
func (e *fakeActionableErr) Actionable() tunnel.ActionableDetails {
	return e.details
}
