package switchboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeRegistry struct {
	provider Provider
}

func (r fakeRegistry) Providers() []string {
	return []string{"mock"}
}

func (r fakeRegistry) Resolve(name string) (Provider, error) {
	return r.provider, nil
}

type fakeProvider struct {
	contexts  []context.Context
	endpoints map[string]Endpoint
	sessions  map[string]Session
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		endpoints: map[string]Endpoint{},
		sessions:  map[string]Session{},
	}
}

func (p *fakeProvider) Name() string { return "mock" }

func (p *fakeProvider) Capabilities() Capabilities {
	return Capabilities{StableHostname: true, HTTPForwarding: true, HTTPSForwarding: true, OAuthSuitable: true}
}

func (p *fakeProvider) Init(ctx context.Context, _ ProviderConfig) error {
	p.contexts = append(p.contexts, ctx)
	return nil
}

func (p *fakeProvider) EnsureEndpoint(ctx context.Context, req EndpointRequest) (Endpoint, error) {
	p.contexts = append(p.contexts, ctx)
	ep := Endpoint{ID: req.PublicHost, Provider: "mock", Name: req.Name, Host: req.PublicHost, Metadata: req.Metadata}
	p.endpoints[ep.ID] = ep
	return ep, nil
}

func (p *fakeProvider) Start(ctx context.Context, req StartRequest) (Session, error) {
	p.contexts = append(p.contexts, ctx)
	session := Session{
		ID:         req.Endpoint.ID + "-session",
		Provider:   "mock",
		EndpointID: req.Endpoint.ID,
		PID:        1234,
		StartedAt:  time.Date(2026, 3, 26, 15, 30, 0, 0, time.UTC),
	}
	p.sessions[session.ID] = session
	return session, nil
}

func (p *fakeProvider) Stop(ctx context.Context, sessionID string) error {
	p.contexts = append(p.contexts, ctx)
	delete(p.sessions, sessionID)
	return nil
}

func (p *fakeProvider) RemoveEndpoint(ctx context.Context, endpointID string) error {
	p.contexts = append(p.contexts, ctx)
	delete(p.endpoints, endpointID)
	return nil
}

func (p *fakeProvider) Status(ctx context.Context, endpointID string) (EndpointStatus, error) {
	p.contexts = append(p.contexts, ctx)
	for _, session := range p.sessions {
		if session.EndpointID == endpointID {
			return EndpointStatus{Ready: true, Endpoint: p.endpoints[endpointID], SessionID: session.ID, Message: "active"}, nil
		}
	}
	return EndpointStatus{Ready: false, Endpoint: p.endpoints[endpointID], Message: "inactive"}, nil
}

func TestClientUsesExplicitConfigPathAndProviderInjection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := New(Options{
		ConfigPath: cfgPath,
		ProviderRegistry: fakeRegistry{
			provider: newFakeProvider(),
		},
		ApplyFunc: func(string, Config) error { return nil },
	})

	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatalf("LoadOrCreateDefaultConfig returned error: %v", err)
	}
	cfg.Caddy.TLS.Enabled = false
	cfg.Tunnel.DefaultProvider = "mock"
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}

	if err := client.CreateApp("demo", 3000, nil); err != nil {
		t.Fatalf("CreateApp returned error: %v", err)
	}
	if err := client.ExposeApp("demo", "mock", "demo.example.com"); err != nil {
		t.Fatalf("ExposeApp returned error: %v", err)
	}
	if err := client.AppUp("demo"); err != nil {
		t.Fatalf("AppUp returned error: %v", err)
	}

	apps, err := client.ListApps()
	if err != nil {
		t.Fatalf("ListApps returned error: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected one app, got %d", len(apps))
	}
	if apps[0].PublicEndpoint.ActiveSessionID == "" {
		t.Fatalf("expected active session, got %#v", apps[0].PublicEndpoint)
	}

	if err := client.AppDown("demo"); err != nil {
		t.Fatalf("AppDown returned error: %v", err)
	}
	apps, err = client.ListApps()
	if err != nil {
		t.Fatalf("ListApps after down returned error: %v", err)
	}
	if apps[0].PublicEndpoint.ActiveSessionID != "" {
		t.Fatalf("expected cleared session, got %#v", apps[0].PublicEndpoint)
	}
}

func TestClientOwnsIngressLifecycleEndToEnd(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "ingress"), time.Minute)
	defer cancel()
	dir := t.TempDir()
	provider := newFakeProvider()
	client := New(Options{ConfigPath: filepath.Join(dir, "config.yaml"), ProviderRegistry: fakeRegistry{provider: provider}, ApplyFunc: func(string, Config) error { return nil }})
	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tunnel.DefaultProvider = "mock"
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	owner := IngressOwnership{System: "ctx", ScopeID: "workspace-1"}
	spec := IngressSpec{Name: "tracker-hooks", LocalPort: 8080, Provider: "mock", PublicHost: "hooks.example.com", CallbackPath: "/webhooks/trackers", Owner: owner, Metadata: map[string]string{"purpose": "tracker"}}
	created, err := client.EnsureIngress(ctx, spec)
	if err != nil {
		t.Fatalf("EnsureIngress: %v", err)
	}
	if created.CallbackURL != "https://hooks.example.com/webhooks/trackers" || created.Revision != 1 {
		t.Fatalf("created ingress = %#v", created)
	}
	repeated, err := client.EnsureIngress(ctx, spec)
	if err != nil || repeated.Revision != created.Revision {
		t.Fatalf("idempotent ensure = %#v, err = %v", repeated, err)
	}

	wrongOwner := spec
	wrongOwner.Owner.ScopeID = "workspace-2"
	if _, err := client.EnsureIngress(ctx, wrongOwner); err == nil {
		t.Fatal("ownership conflict was accepted")
	}

	started, err := client.StartIngress(ctx, IngressRef{Name: created.Name, Owner: owner, ExpectedRevision: &created.Revision})
	if err != nil || started.State != IngressStateRunning || started.SessionID == "" {
		t.Fatalf("StartIngress = %#v, err = %v", started, err)
	}
	status, err := client.StatusIngress(ctx, IngressRef{Name: created.Name, Owner: owner})
	if err != nil || status.State != IngressStateRunning {
		t.Fatalf("StatusIngress = %#v, err = %v", status, err)
	}
	stopped, err := client.StopIngress(ctx, IngressRef{Name: created.Name, Owner: owner, ExpectedRevision: &started.Revision})
	if err != nil || stopped.State != IngressStateStopped || stopped.SessionID != "" {
		t.Fatalf("StopIngress = %#v, err = %v", stopped, err)
	}
	if err := client.ReleaseIngress(ctx, IngressRef{Name: created.Name, Owner: owner, ExpectedRevision: &stopped.Revision}); err != nil {
		t.Fatalf("ReleaseIngress: %v", err)
	}
	if len(provider.endpoints) != 0 {
		t.Fatalf("remote endpoints remain: %#v", provider.endpoints)
	}
	items, err := client.ListIngress(ctx, &owner)
	if err != nil || len(items) != 0 {
		t.Fatalf("ListIngress = %#v, err = %v", items, err)
	}
	deadline, _ := ctx.Deadline()
	for _, providerCtx := range provider.contexts {
		providerDeadline, ok := providerCtx.Deadline()
		if providerCtx.Value(contextKey{}) != "ingress" || !ok || providerDeadline.After(deadline) {
			t.Fatal("provider operation lost the ingress caller context or deadline")
		}
	}
}

func TestClientRejectsUnsafeIngressCallback(t *testing.T) {
	client := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), ProviderRegistry: fakeRegistry{provider: newFakeProvider()}, ApplyFunc: func(string, Config) error { return nil }})
	_, err := client.EnsureIngress(context.Background(), IngressSpec{Name: "hooks", LocalPort: 8080, Provider: "mock", PublicHost: "hooks.example.com", CallbackPath: "https://attacker.example/hook", Owner: IngressOwnership{System: "ctx", ScopeID: "workspace-1"}})
	if err == nil {
		t.Fatal("absolute callback URL was accepted")
	}
}

func TestClientStackFacade(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	stackPath := filepath.Join(dir, "stack.yaml")
	client := New(Options{
		ConfigPath: cfgPath,
		ProviderRegistry: fakeRegistry{
			provider: newFakeProvider(),
		},
		ApplyFunc: func(string, Config) error { return nil },
	})

	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatalf("LoadOrCreateDefaultConfig returned error: %v", err)
	}
	cfg.TLD = "test"
	cfg.Caddy.TLS.Enabled = false
	cfg.Tunnel.DefaultProvider = "mock"
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
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
outputs:
  APP_HTTP__BASE_URL: "https://{{ service \"app\" \"public_host\" }}"
`
	if err := os.WriteFile(stackPath, []byte(stackYAML), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	report, err := client.StackPlan(stackPath)
	if err != nil {
		t.Fatalf("StackPlan returned error: %v", err)
	}
	if len(report.Services) != 1 || report.Services[0].Actions[0].Type != "create_app" {
		t.Fatalf("unexpected plan report: %#v", report)
	}

	lines, err := client.RenderStackEnv(stackPath)
	if err != nil {
		t.Fatalf("RenderStackEnv returned error: %v", err)
	}
	if len(lines) != 1 || lines[0] != "APP_HTTP__BASE_URL=https://app.carina.getctx.com" {
		t.Fatalf("unexpected env lines: %#v", lines)
	}
}
