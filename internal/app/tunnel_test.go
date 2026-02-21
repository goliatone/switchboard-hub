package app

import (
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
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
