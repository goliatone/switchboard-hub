//go:build integration

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type integrationProvider struct {
	mu        sync.Mutex
	endpoints map[string]tunnel.Endpoint
	sessions  map[string]tunnel.Session
}

func newIntegrationProvider() *integrationProvider {
	return &integrationProvider{
		endpoints: map[string]tunnel.Endpoint{},
		sessions:  map[string]tunnel.Session{},
	}
}

func (p *integrationProvider) Name() string { return "mock" }

func (p *integrationProvider) Capabilities() tunnel.Capabilities {
	return tunnel.Capabilities{
		StableHostname:  true,
		HTTPForwarding:  true,
		HTTPSForwarding: true,
		OAuthSuitable:   true,
	}
}

func (p *integrationProvider) Init(context.Context, tunnel.ProviderConfig) error { return nil }

func (p *integrationProvider) EnsureEndpoint(_ context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	ep := tunnel.Endpoint{
		ID:       req.PublicHost,
		Provider: "mock",
		Name:     req.Name,
		Host:     req.PublicHost,
		Metadata: req.Metadata,
	}
	p.mu.Lock()
	p.endpoints[ep.ID] = ep
	p.mu.Unlock()
	return ep, nil
}

func (p *integrationProvider) Start(_ context.Context, req tunnel.StartRequest) (tunnel.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := tunnel.Session{
		ID:         req.Endpoint.ID + "-session",
		Provider:   "mock",
		EndpointID: req.Endpoint.ID,
		PID:        4242,
		StartedAt:  time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC),
	}
	p.sessions[session.ID] = session
	return session, nil
}

func (p *integrationProvider) Stop(_ context.Context, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sessions[sessionID]; !ok {
		return errStatic("session not found")
	}
	delete(p.sessions, sessionID)
	return nil
}

func (p *integrationProvider) RemoveEndpoint(_ context.Context, endpointID string) error {
	p.mu.Lock()
	delete(p.endpoints, endpointID)
	p.mu.Unlock()
	return nil
}

func (p *integrationProvider) Status(_ context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		if s.EndpointID == endpointID {
			return tunnel.EndpointStatus{
				Ready:     true,
				Endpoint:  p.endpoints[endpointID],
				SessionID: s.ID,
				Message:   "mock session active",
			}, nil
		}
	}
	return tunnel.EndpointStatus{
		Ready:    false,
		Endpoint: p.endpoints[endpointID],
		Message:  "mock session inactive",
	}, nil
}

func TestIntegration_AppExposeOAuthUpDownWithMockProvider(t *testing.T) {
	restore := installMockIntegrationProvider(t)
	defer restore()

	cfgPath, caddyServer := setupIntegrationConfig(t)
	defer caddyServer.Close()

	if err := CreateApp("esign", 3000); err != nil {
		t.Fatalf("CreateApp returned error: %v", err)
	}
	if err := ExposeApp("esign", "mock", "esign-oauth.dev.example.com"); err != nil {
		t.Fatalf("ExposeApp returned error: %v", err)
	}
	if err := OAuthGoogleEnable("esign", "/admin/esign/integrations/google/callback"); err != nil {
		t.Fatalf("OAuthGoogleEnable returned error: %v", err)
	}
	if err := AppUp("esign"); err != nil {
		t.Fatalf("AppUp returned error: %v", err)
	}
	if err := AppUp("esign"); err != nil {
		t.Fatalf("AppUp second call should be idempotent, got error: %v", err)
	}
	if err := AppDown("esign"); err != nil {
		t.Fatalf("AppDown returned error: %v", err)
	}
	if err := AppDown("esign"); err != nil {
		t.Fatalf("AppDown second call should be idempotent, got error: %v", err)
	}

	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(c.Apps) != 1 {
		t.Fatalf("expected one app, got %d", len(c.Apps))
	}
	a := c.Apps[0]
	if a.OAuth.Google.RedirectURI != "https://esign-oauth.dev.example.com/admin/esign/integrations/google/callback" {
		t.Fatalf("unexpected redirect URI: %q", a.OAuth.Google.RedirectURI)
	}
	if a.PublicEndpoint.ActiveSessionID != "" || a.PublicEndpoint.ActiveSessionPID != 0 {
		t.Fatalf("expected cleared session metadata, got %#v", a.PublicEndpoint)
	}
}

func TestIntegration_BackwardCompatibilityRouteOnlyConfig(t *testing.T) {
	restore := installMockIntegrationProvider(t)
	defer restore()

	cfgPath, caddyServer := setupIntegrationConfig(t)
	defer caddyServer.Close()

	legacy := `tld: test
dns:
  ip: 10.0.0.1
caddy:
  admin: ` + caddyServer.URL + `
  listen:
    - :80
routes:
  - host: legacy.test
    dial: 127.0.0.1:3030
`
	if err := osWriteFile(cfgPath, legacy); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	apps, err := ListApps()
	if err != nil {
		t.Fatalf("ListApps returned error: %v", err)
	}
	if len(apps) == 0 {
		t.Fatal("expected migrated apps from legacy routes")
	}
	if err := AddRoute("new-legacy", 4040); err != nil {
		t.Fatalf("AddRoute returned error: %v", err)
	}
	if err := RemoveRoute("new-legacy.test"); err != nil {
		t.Fatalf("RemoveRoute returned error: %v", err)
	}
}

func setupIntegrationConfig(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case "/load":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`ok`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	c := config.Default("test", "10.0.0.1")
	c.Caddy.Admin = ts.URL
	c.Caddy.TLS.Enabled = false
	if err := config.Save(cfgPath, c); err != nil {
		t.Fatalf("seed config.Save: %v", err)
	}
	return cfgPath, ts
}

func installMockIntegrationProvider(t *testing.T) func() {
	t.Helper()
	orig := providerRegistryFactory
	mock := newIntegrationProvider()
	providerRegistryFactory = func() *tunnel.Registry {
		r := tunnel.NewRegistry()
		_ = r.Register("mock", func() tunnel.Provider { return mock })
		return r
	}
	return func() {
		providerRegistryFactory = orig
	}
}

type errStatic string

func (e errStatic) Error() string { return string(e) }

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
