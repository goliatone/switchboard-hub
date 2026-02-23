package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault_TLSIsEnabled(t *testing.T) {
	c := Default("test", "10.0.0.1")
	if !c.Caddy.TLS.Enabled {
		t.Fatal("expected TLS enabled in default config")
	}
	if c.Caddy.TLS.Mode != "internal" {
		t.Fatalf("expected TLS mode internal, got %q", c.Caddy.TLS.Mode)
	}
	if len(c.Caddy.TLS.Listen) != 1 || c.Caddy.TLS.Listen[0] != ":443" {
		t.Fatalf("unexpected TLS listen addresses: %#v", c.Caddy.TLS.Listen)
	}
}

func TestLoad_BackfillsTLSListenForLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `tld: test
dns:
  ip: 10.0.0.1
caddy:
  admin: http://127.0.0.1:2019
  listen:
    - :80
routes: []
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if c.Caddy.TLS.Enabled {
		t.Fatal("expected TLS disabled for legacy config without tls.enabled")
	}
	if c.Caddy.TLS.Mode != "internal" {
		t.Fatalf("expected TLS mode internal, got %q", c.Caddy.TLS.Mode)
	}
	if len(c.Caddy.TLS.Listen) != 1 || c.Caddy.TLS.Listen[0] != ":443" {
		t.Fatalf("unexpected TLS listen addresses: %#v", c.Caddy.TLS.Listen)
	}
}

func TestLoad_BackfillsTLSModeForTLSConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `tld: test
dns:
  ip: 10.0.0.1
caddy:
  admin: http://127.0.0.1:2019
  listen:
    - :80
  tls:
    enabled: true
    listen:
      - :443
routes: []
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if c.Caddy.TLS.Mode != "internal" {
		t.Fatalf("expected TLS mode internal, got %q", c.Caddy.TLS.Mode)
	}
}

func TestLoad_MigratesLegacyRoutesToApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `tld: test
dns:
  ip: 10.0.0.1
caddy:
  admin: http://127.0.0.1:2019
  listen:
    - :80
routes:
  - host: esign.test
    dial: 127.0.0.1:3000
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(c.Apps) != 1 {
		t.Fatalf("expected 1 migrated app, got %d", len(c.Apps))
	}
	if c.Apps[0].Name != "esign" {
		t.Fatalf("unexpected app name: %q", c.Apps[0].Name)
	}
	if c.Apps[0].LocalHost != "esign.test" {
		t.Fatalf("unexpected app local_host: %q", c.Apps[0].LocalHost)
	}
	if c.Apps[0].LocalPort != 3000 {
		t.Fatalf("unexpected app local_port: %d", c.Apps[0].LocalPort)
	}
	if c.Tunnel.DefaultProvider != "cloudflare" {
		t.Fatalf("unexpected default tunnel provider: %q", c.Tunnel.DefaultProvider)
	}
}

func TestSaveLoad_RoundTripMixedLegacyAndNewSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		TLD:   "test",
		DNS:   DNS{IP: "10.0.0.1"},
		Caddy: Default("test", "10.0.0.1").Caddy,
		Routes: []Route{
			{Host: "esign.test", Dial: "127.0.0.1:3000"},
		},
		Tunnel: Tunnels{
			DefaultProvider: "cloudflare",
			Providers: map[string]TunnelProviderCfg{
				"cloudflare": {
					Enabled: true,
					Zone:    "dev.example.com",
					Values: map[string]string{
						"team": "dev",
					},
				},
			},
		},
		Apps: []App{
			{
				Name:      "esign",
				LocalHost: "esign.test",
				LocalPort: 3000,
				PublicEndpoint: AppPublicEndpoint{
					Provider:             "cloudflare",
					Host:                 "esign-oauth.dev.example.com",
					EndpointID:           "tunnel-123",
					ActiveSessionID:      "session-1",
					ActiveSessionPID:     4242,
					ActiveSessionStarted: "2026-02-21T10:00:00Z",
				},
				OAuth: AppOAuth{
					Google: AppGoogleOAuth{
						Enabled:      true,
						CallbackPath: "/admin/esign/integrations/google/callback",
						RedirectURI:  "https://esign-oauth.dev.example.com/admin/esign/integrations/google/callback",
					},
				},
			},
		},
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got.Tunnel.Providers["cloudflare"].Zone != "dev.example.com" {
		t.Fatalf("unexpected cloudflare zone: %q", got.Tunnel.Providers["cloudflare"].Zone)
	}
	if len(got.Apps) != 1 || got.Apps[0].PublicEndpoint.Host != "esign-oauth.dev.example.com" {
		t.Fatalf("unexpected apps after roundtrip: %#v", got.Apps)
	}
	if got.Apps[0].PublicEndpoint.ActiveSessionPID != 4242 {
		t.Fatalf("unexpected active_session_pid: %d", got.Apps[0].PublicEndpoint.ActiveSessionPID)
	}
	if got.Apps[0].PublicEndpoint.ActiveSessionStarted != "2026-02-21T10:00:00Z" {
		t.Fatalf("unexpected active_session_started_at: %q", got.Apps[0].PublicEndpoint.ActiveSessionStarted)
	}
	if got.Apps[0].OAuth.Google.CallbackPath != "/admin/esign/integrations/google/callback" {
		t.Fatalf("unexpected callback_path: %q", got.Apps[0].OAuth.Google.CallbackPath)
	}
}
