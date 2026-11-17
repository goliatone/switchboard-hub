package tracker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/pkg/switchboard"
)

// Copied only into an isolated ctx checkout for candidate compatibility checks.
// No Switchboard binary, provider executable, network, or live configuration.
var _ SwitchboardClient = (*switchboard.Client)(nil)

type candidateRegistry struct{ p *candidateProvider }

func (r candidateRegistry) Providers() []string                          { return []string{"cloudflare"} }
func (r candidateRegistry) Resolve(string) (switchboard.Provider, error) { return r.p, nil }

type candidateProvider struct {
	calls     int
	endpoints map[string]switchboard.Endpoint
	sessions  map[string]switchboard.Session
}

func (p *candidateProvider) Name() string { return "cloudflare" }
func (p *candidateProvider) Capabilities() switchboard.Capabilities {
	return switchboard.Capabilities{HTTPForwarding: true, HTTPSForwarding: true, StableHostname: true, OAuthSuitable: true}
}
func (p *candidateProvider) Init(context.Context, switchboard.ProviderConfig) error {
	p.calls++
	return nil
}
func (p *candidateProvider) EnsureEndpoint(_ context.Context, r switchboard.EndpointRequest) (switchboard.Endpoint, error) {
	p.calls++
	ep := switchboard.Endpoint{ID: r.Name, Name: r.Name, Host: r.PublicHost, Provider: "cloudflare"}
	p.endpoints[ep.ID] = ep
	return ep, nil
}
func (p *candidateProvider) Start(_ context.Context, r switchboard.StartRequest) (switchboard.Session, error) {
	p.calls++
	s := switchboard.Session{ID: r.Endpoint.ID + "-session", Provider: "cloudflare", EndpointID: r.Endpoint.ID, StartedAt: time.Now()}
	p.sessions[s.ID] = s
	return s, nil
}
func (p *candidateProvider) Stop(_ context.Context, id string) error {
	p.calls++
	delete(p.sessions, id)
	return nil
}
func (p *candidateProvider) RemoveEndpoint(_ context.Context, id string) error {
	p.calls++
	delete(p.endpoints, id)
	return nil
}
func (p *candidateProvider) Status(_ context.Context, id string) (switchboard.EndpointStatus, error) {
	p.calls++
	st := switchboard.EndpointStatus{Endpoint: p.endpoints[id]}
	for _, s := range p.sessions {
		if s.EndpointID == id {
			st.Ready = true
			st.SessionID = s.ID
		}
	}
	return st, nil
}
func candidateClient(t *testing.T) (*switchboard.Client, *candidateProvider) {
	t.Helper()
	p := &candidateProvider{endpoints: map[string]switchboard.Endpoint{}, sessions: map[string]switchboard.Session{}}
	c := switchboard.New(switchboard.Options{ConfigPath: filepath.Join(t.TempDir(), "switchboard.yaml"), ProviderRegistry: candidateRegistry{p}, ApplyFunc: func(string, switchboard.Config) error { return nil }})
	return c, p
}

func TestSwitchboardCandidateManagedLifecycleRepairAndTargetDrift(t *testing.T) {
	ctx := context.Background()
	repository := newMemoryRepository()
	connection := ingressConnectionFixture()
	connection.Scopes = []string{"acme/one", "acme/two"}
	connection, _, _ = repository.CreateConnection(ctx, connection)
	_, _ = repository.PutHealth(ctx, connection.ID, ConnectionHealth{Polling: healthOutcome(SurfaceFixtureTime, nil, "polling healthy")}, "")
	client, provider := candidateClient(t)
	registrar := &recordingWebhookRegistrar{failScope: "acme/two"}
	service, err := NewIngressService(repository, client, registrar, nil, IngressConfig{Provider: "cloudflare", PublicHost: "hooks.example.test", LocalPort: 8042}, fixedTrackerNow())
	if err != nil {
		t.Fatal(err)
	}
	partial, err := service.Execute(ctx, CommandIngressEnsure, connection.ID, connection.Revision, "partial")
	if err == nil || len(partial.Ownership.WebhookRegistrations) != 1 || partial.Health.Polling.State != "healthy" {
		t.Fatalf("partial registration/polling: %#v %v", partial, err)
	}
	registrar.failScope = ""
	repaired, err := service.Execute(ctx, CommandIngressEnsure, connection.ID, partial.Ownership.Revision, "repair")
	if err != nil || len(repaired.Ownership.WebhookRegistrations) != 2 || len(registrar.ensured) != 3 {
		t.Fatalf("partial repair: %#v %v", repaired, err)
	}
	if err := service.SetLocalPort(8123); err != nil {
		t.Fatal(err)
	}
	drifted, err := service.Execute(ctx, CommandIngressEnsure, connection.ID, repaired.Ownership.Revision, "target-drift")
	if err != nil {
		t.Fatal(err)
	}
	items, err := client.ListIngress(ctx, nil)
	if err != nil || len(items) != 1 || items[0].LocalPort != 8123 || items[0].Owner.System != "ctx" {
		t.Fatalf("target/owner drift: %#v %v", items, err)
	}
	if !strings.HasPrefix(items[0].CallbackURL, "https://hooks.example.test/workbench/hooks/tracker/") {
		t.Fatal(items[0].CallbackURL)
	}
	started, err := service.Execute(ctx, CommandIngressStart, connection.ID, drifted.Ownership.Revision, "start")
	if err != nil || started.Health.Tunnel.State != "healthy" {
		t.Fatalf("start: %#v %v", started, err)
	}
	stopped, err := service.Execute(ctx, CommandIngressStop, connection.ID, started.Ownership.Revision, "stop")
	if err != nil || len(provider.sessions) != 0 || len(provider.endpoints) != 1 {
		t.Fatalf("stop: %#v %v", stopped, err)
	}
	restarted, err := service.Execute(ctx, CommandIngressStart, connection.ID, stopped.Ownership.Revision, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(ctx, CommandIngressRelease, connection.ID, restarted.Ownership.Revision, "release"); err != nil {
		t.Fatal(err)
	}
	items, err = client.ListIngress(ctx, nil)
	if err != nil || len(items) != 0 || len(provider.endpoints) != 0 || len(provider.sessions) != 0 {
		t.Fatalf("cleanup: %#v %v", items, err)
	}
}

func TestSwitchboardCandidateOwnershipAndExternalOrigin(t *testing.T) {
	ctx := context.Background()
	repository := newMemoryRepository()
	connection, _, _ := repository.CreateConnection(ctx, ingressConnectionFixture())
	_, _ = repository.PutHealth(ctx, connection.ID, ConnectionHealth{Polling: healthOutcome(SurfaceFixtureTime, nil, "polling healthy")}, "")
	client, provider := candidateClient(t)
	_, err := client.EnsureIngress(ctx, switchboard.IngressSpec{Name: ingressAppName(connection), LocalPort: 8042, DialHost: "127.0.0.1", Provider: "cloudflare", PublicHost: "hooks.example.test", CallbackPath: "/hooks", Owner: switchboard.IngressOwnership{System: "other", ScopeID: "foreign"}})
	if err != nil {
		t.Fatal(err)
	}
	before := provider.calls
	service, _ := NewIngressService(repository, client, &recordingWebhookRegistrar{}, nil, IngressConfig{Provider: "cloudflare", PublicHost: "hooks.example.test", LocalPort: 8042}, fixedTrackerNow())
	denied, err := service.Execute(ctx, CommandIngressEnsure, connection.ID, connection.Revision, "owner-denied")
	if err == nil || denied.Health.Polling.State != "healthy" || provider.calls != before {
		t.Fatalf("owner conflict caused I/O or polling loss: %#v %v", denied, err)
	}
	repository = newMemoryRepository()
	connection, _, _ = repository.CreateConnection(ctx, ingressConnectionFixture())
	registrar := &recordingWebhookRegistrar{ensureErr: errors.New("temporary webhook failure")}
	external, _ := NewIngressService(repository, client, registrar, nil, IngressConfig{PublicBaseURL: "https://external.example.test"}, fixedTrackerNow())
	failed, err := external.Execute(ctx, CommandIngressEnsure, connection.ID, connection.Revision, "external-fail")
	if err == nil {
		t.Fatal("webhook failure ignored")
	}
	registrar.ensureErr = nil
	ready, err := external.Execute(ctx, CommandIngressEnsure, connection.ID, failed.Ownership.Revision, "external-repair")
	if err != nil || ready.Ownership.SwitchboardRevision != 0 {
		t.Fatalf("external repair: %#v %v", ready, err)
	}
	paused, err := external.Execute(ctx, CommandIngressStop, connection.ID, ready.Ownership.Revision, "external-pause")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := external.Execute(ctx, CommandIngressStart, connection.ID, paused.Ownership.Revision, "external-resume")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := external.Execute(ctx, CommandIngressRelease, connection.ID, resumed.Ownership.Revision, "external-release"); err != nil {
		t.Fatal(err)
	}
	if provider.calls != before {
		t.Fatal("external-origin lifecycle mutated Switchboard")
	}
}
