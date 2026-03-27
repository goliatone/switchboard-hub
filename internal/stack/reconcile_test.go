package stack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type fakeProvider struct {
	endpoints map[string]tunnel.Endpoint
	sessions  map[string]tunnel.Session
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		endpoints: map[string]tunnel.Endpoint{},
		sessions:  map[string]tunnel.Session{},
	}
}

func (p *fakeProvider) Name() string { return "mock" }

func (p *fakeProvider) Capabilities() tunnel.Capabilities {
	return tunnel.Capabilities{StableHostname: true, HTTPForwarding: true, HTTPSForwarding: true, OAuthSuitable: true}
}

func (p *fakeProvider) Init(context.Context, tunnel.ProviderConfig) error { return nil }

func (p *fakeProvider) EnsureEndpoint(_ context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	ep := tunnel.Endpoint{
		ID:       req.PublicHost,
		Provider: "mock",
		Name:     req.Name,
		Host:     req.PublicHost,
		Metadata: req.Metadata,
	}
	p.endpoints[ep.ID] = ep
	return ep, nil
}

func (p *fakeProvider) Start(_ context.Context, req tunnel.StartRequest) (tunnel.Session, error) {
	session := tunnel.Session{
		ID:         req.Endpoint.ID + "-session",
		Provider:   "mock",
		EndpointID: req.Endpoint.ID,
		PID:        4242,
		StartedAt:  time.Date(2026, 3, 26, 15, 0, 0, 0, time.UTC),
	}
	p.sessions[session.ID] = session
	return session, nil
}

func (p *fakeProvider) Stop(_ context.Context, sessionID string) error {
	delete(p.sessions, sessionID)
	return nil
}

func (p *fakeProvider) RemoveEndpoint(_ context.Context, endpointID string) error {
	delete(p.endpoints, endpointID)
	return nil
}

func (p *fakeProvider) Status(_ context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	for _, session := range p.sessions {
		if session.EndpointID == endpointID {
			return tunnel.EndpointStatus{Ready: true, Endpoint: p.endpoints[endpointID], SessionID: session.ID, Message: "active"}, nil
		}
	}
	return tunnel.EndpointStatus{Ready: false, Endpoint: p.endpoints[endpointID], Message: "inactive"}, nil
}

func TestPlanShowsCreateActions(t *testing.T) {
	service, stackPath, _ := setupStackService(t)

	report, err := Plan(service, stackPath)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if report.HasUnsafe {
		t.Fatalf("expected safe plan, got %#v", report)
	}
	if len(report.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(report.Services))
	}
	if got := report.Services[0].Actions[0].Type; got != "create_app" {
		t.Fatalf("unexpected first action: %q", got)
	}
}

func TestUpAndDownReconcileManagedApps(t *testing.T) {
	service, stackPath, cfgPath := setupStackService(t)

	report, err := Up(service, stackPath)
	if err != nil {
		t.Fatalf("Up returned error: %v", err)
	}
	if len(report.Services) != 2 {
		t.Fatalf("unexpected up report: %#v", report)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("expected 2 managed apps, got %d", len(cfg.Apps))
	}
	for _, appCfg := range cfg.Apps {
		if appCfg.Metadata["managed_by"] != "stack" {
			t.Fatalf("expected stack metadata, got %#v", appCfg.Metadata)
		}
		if appCfg.PublicEndpoint.EndpointID == "" {
			t.Fatalf("expected endpoint id for %#v", appCfg)
		}
		if appCfg.PublicEndpoint.ActiveSessionID == "" {
			t.Fatalf("expected active session for %#v", appCfg)
		}
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(cfg.Routes))
	}

	downReport, err := Down(service, stackPath)
	if err != nil {
		t.Fatalf("Down returned error: %v", err)
	}
	if len(downReport.Services) != 2 {
		t.Fatalf("unexpected down report: %#v", downReport)
	}

	cfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after down returned error: %v", err)
	}
	for _, appCfg := range cfg.Apps {
		if appCfg.PublicEndpoint.ActiveSessionID != "" {
			t.Fatalf("expected cleared session after down, got %#v", appCfg.PublicEndpoint)
		}
		if appCfg.PublicEndpoint.EndpointID == "" {
			t.Fatalf("expected endpoint to remain after down, got %#v", appCfg.PublicEndpoint)
		}
	}
}

func TestUpFailsOnUnmanagedCollision(t *testing.T) {
	service, stackPath, cfgPath := setupStackService(t)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	cfg.Apps = append(cfg.Apps, config.App{
		Name:      "carina-app",
		LocalHost: "legacy.test",
		LocalPort: 3000,
		Metadata:  map[string]string{},
	})
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	report, err := Up(service, stackPath)
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !report.HasUnsafe {
		t.Fatalf("expected unsafe report, got %#v", report)
	}
}

func TestDownStopsManagedSessionsDespiteDesiredCollision(t *testing.T) {
	service, stackPath, cfgPath := setupStackService(t)

	if _, err := Up(service, stackPath); err != nil {
		t.Fatalf("Up returned error: %v", err)
	}

	collidingStack := `
version: 1
name: carina
defaults:
  provider: mock
  expose: true
  up: true
services:
  - name: app
    local_port: 8383
    local_host: legacy.test
    public_host: app.carina.getctx.com
  - name: simulator
    local_port: 8090
    public_host: carina.getctx.com
`
	if err := os.WriteFile(stackPath, []byte(collidingStack), 0o644); err != nil {
		t.Fatalf("write colliding stack: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	cfg.Apps = append(cfg.Apps, config.App{
		Name:      "legacy",
		LocalHost: "legacy.test",
		LocalPort: 3000,
		Metadata:  map[string]string{},
	})
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	report, err := Down(service, stackPath)
	if err != nil {
		t.Fatalf("Down returned error: %v", err)
	}
	if !report.HasUnsafe {
		t.Fatalf("expected collision to still be reported, got %#v", report)
	}

	cfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after down returned error: %v", err)
	}
	for _, appCfg := range cfg.Apps {
		if appCfg.Metadata["managed_by"] != "stack" {
			continue
		}
		if appCfg.PublicEndpoint.ActiveSessionID != "" {
			t.Fatalf("expected managed session to be stopped, got %#v", appCfg.PublicEndpoint)
		}
	}
}

func TestUpRemovesStaleRouteWhenManagedLocalHostChanges(t *testing.T) {
	service, stackPath, cfgPath := setupStackService(t)

	if _, err := Up(service, stackPath); err != nil {
		t.Fatalf("first Up returned error: %v", err)
	}

	updatedStack := `
version: 1
name: carina
defaults:
  provider: mock
  expose: true
  up: true
services:
  - name: app
    local_port: 8383
    local_host: web.test
    public_host: app.carina.getctx.com
  - name: simulator
    local_port: 8090
    public_host: carina.getctx.com
`
	if err := os.WriteFile(stackPath, []byte(updatedStack), 0o644); err != nil {
		t.Fatalf("write updated stack: %v", err)
	}

	if _, err := Up(service, stackPath); err != nil {
		t.Fatalf("second Up returned error: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if routeExists(cfg, "carina-app.test") {
		t.Fatalf("expected stale route to be removed: %#v", cfg.Routes)
	}
	if !routeExists(cfg, "web.test") {
		t.Fatalf("expected updated route to exist: %#v", cfg.Routes)
	}
}

func TestPlanDoesNotStartTunnelWhenExposeFalse(t *testing.T) {
	service, stackPath, cfgPath := setupStackService(t)

	stackYAML := `
version: 1
name: carina
defaults:
  expose: false
  up: true
services:
  - name: app
    local_port: 8383
`
	if err := os.WriteFile(stackPath, []byte(stackYAML), 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	report, err := Plan(service, stackPath)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	for _, action := range report.Services[0].Actions {
		if action.Type == "start_tunnel" || action.Type == "expose_endpoint" {
			t.Fatalf("unexpected tunnel action in plan: %#v", report.Services[0].Actions)
		}
	}

	if _, err := Up(service, stackPath); err != nil {
		t.Fatalf("Up returned error: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("expected one app, got %d", len(cfg.Apps))
	}
	if cfg.Apps[0].PublicEndpoint.EndpointID != "" || cfg.Apps[0].PublicEndpoint.ActiveSessionID != "" {
		t.Fatalf("expected no endpoint/session when expose=false, got %#v", cfg.Apps[0].PublicEndpoint)
	}
}

func setupStackService(t *testing.T) (*app.Service, string, string) {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	stackPath := filepath.Join(dir, "stack.yaml")

	caddyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(caddyServer.Close)

	cfg := config.Default("test", "10.0.0.1")
	cfg.Caddy.Admin = caddyServer.URL
	cfg.Caddy.TLS.Enabled = false
	cfg.Tunnel.DefaultProvider = "mock"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	stackYAML := `
version: 1
name: carina
defaults:
  provider: mock
  expose: true
  up: true
services:
  - name: app
    local_port: 8383
    public_host: app.carina.getctx.com
  - name: simulator
    local_port: 8090
    public_host: carina.getctx.com
outputs:
  APP_HTTP__BASE_URL: "https://{{ service \"app\" \"public_host\" }}"
`
	if err := os.WriteFile(stackPath, []byte(stackYAML), 0o644); err != nil {
		t.Fatalf("write stack file: %v", err)
	}

	provider := newFakeProvider()
	registry := tunnel.NewRegistry()
	if err := registry.Register("mock", func() tunnel.Provider { return provider }); err != nil {
		t.Fatalf("register mock provider: %v", err)
	}

	service := app.NewService(app.ServiceOptions{
		ConfigPath:       cfgPath,
		ProviderRegistry: registry,
		ApplyConfig: func(string, *config.Config) error {
			return nil
		},
	})
	return service, stackPath, cfgPath
}

func routeExists(cfg *config.Config, host string) bool {
	for _, route := range cfg.Routes {
		if route.Host == host {
			return true
		}
	}
	return false
}
