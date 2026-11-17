package switchboard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sb "github.com/goliatone/switchboard-hub/pkg/switchboard"
)

const credentialCanary = "DUMMY-T04-PRIVATE-CREDENTIAL"

type ownedContextKey struct{}

type ownedTestStore struct {
	interrupted bool
	mu          sync.Mutex
	cfg         sb.Config
	locked      bool
	saves       int
	operations  []sb.IngressMutationScope
	progress    []sb.IngressProgress
	failRecord  string
	failSave    bool
}

func cloneConfig(cfg sb.Config) sb.Config {
	raw, _ := json.Marshal(cfg)
	var out sb.Config
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *ownedTestStore) Load(string) (sb.Config, error)                  { return cloneConfig(s.cfg), nil }
func (s *ownedTestStore) LoadOrDefault(p string) (sb.Config, error)       { return s.Load(p) }
func (s *ownedTestStore) LoadOrCreateDefault(p string) (sb.Config, error) { return s.Load(p) }
func (s *ownedTestStore) Save(_ string, cfg sb.Config) error {
	if !s.locked {
		return errors.New("save outside mutation")
	}
	if s.failSave {
		return errors.New("injected save failure")
	}
	s.saves++
	s.cfg = cloneConfig(cfg)
	return nil
}
func (s *ownedTestStore) WithIngressMutation(ctx context.Context, scope sb.IngressMutationScope, fn func(context.Context, sb.IngressRecorder) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locked = true
	defer func() { s.locked = false }()
	s.operations = append(s.operations, scope)
	return fn(ctx, s)
}
func (s *ownedTestStore) PendingProgress(context.Context) (sb.IngressProgress, bool, error) {
	return sb.IngressProgress{}, s.interrupted, nil
}

func (s *ownedTestStore) OperationID() string {
	return fmt.Sprintf("test-operation-%d", len(s.operations))
}
func (s *ownedTestStore) Record(_ context.Context, p sb.IngressProgress) error {
	if p.Phase == s.failRecord {
		return errors.New("injected journal failure")
	}
	s.progress = append(s.progress, p)
	return nil
}

type legacyTestProvider struct{ calls int }

func (p *legacyTestProvider) Name() string { return "mock" }
func (p *legacyTestProvider) Capabilities() sb.Capabilities {
	return sb.Capabilities{HTTPSForwarding: true, HTTPForwarding: true, StableHostname: true}
}
func (p *legacyTestProvider) Init(context.Context, sb.ProviderConfig) error { p.calls++; return nil }
func (p *legacyTestProvider) EnsureEndpoint(context.Context, sb.EndpointRequest) (sb.Endpoint, error) {
	p.calls++
	return sb.Endpoint{}, nil
}
func (p *legacyTestProvider) Start(context.Context, sb.StartRequest) (sb.Session, error) {
	p.calls++
	return sb.Session{}, nil
}
func (p *legacyTestProvider) Stop(context.Context, string) error           { p.calls++; return nil }
func (p *legacyTestProvider) RemoveEndpoint(context.Context, string) error { p.calls++; return nil }
func (p *legacyTestProvider) Status(context.Context, string) (sb.EndpointStatus, error) {
	p.calls++
	return sb.EndpointStatus{}, nil
}

type ownedTestProvider struct {
	legacyTestProvider
	events       []string
	resources    map[string]sb.IngressResource
	runtime      sb.IngressRuntime
	secretBuffer []byte
	failAction   string
	leakResult   bool
	contexts     []context.Context
}

func (p *ownedTestProvider) event(ctx context.Context, action string) error {
	p.contexts = append(p.contexts, ctx)
	p.events = append(p.events, action)
	if p.failAction == action {
		return errors.New(credentialCanary)
	}
	return ctx.Err()
}
func (p *ownedTestProvider) ValidateIngress(ctx context.Context, r sb.IngressResource, runtime sb.IngressRuntime) error {
	p.runtime = runtime
	return p.event(ctx, "validate")
}
func (p *ownedTestProvider) InitIngressPrivate(ctx context.Context, _ map[string]string, _ sb.IngressRuntime, secret []byte) error {
	if len(secret) > 0 && string(secret) != credentialCanary {
		return errors.New("wrong credential")
	}
	p.secretBuffer = secret
	return p.event(ctx, "init")
}
func (p *ownedTestProvider) EnsureOwnedIngress(ctx context.Context, r sb.IngressResource) (sb.IngressResource, error) {
	if err := p.event(ctx, "ensure"); err != nil {
		return r, err
	}
	if r.Binding.EndpointID == "" {
		r.Binding.EndpointID = "endpoint-" + r.Name
	}
	r.ProviderState = map[string]string{"created_by": "test-operation"}
	if p.leakResult {
		r.ProviderState["bad"] = credentialCanary
	}
	p.resources[r.Binding.EndpointID] = r
	return r, nil
}
func (p *ownedTestProvider) StartOwnedIngress(ctx context.Context, r sb.IngressResource) (sb.IngressResource, error) {
	if err := p.event(ctx, "start"); err != nil {
		return r, err
	}
	r.Session = sb.IngressSessionIdentity{ID: "session-" + r.Name, Identity: map[string]string{"node": "test-node", "generation": r.Generation}}
	p.resources[r.Binding.EndpointID] = r
	return r, nil
}
func (p *ownedTestProvider) StopOwnedIngress(ctx context.Context, r sb.IngressResource) error {
	if err := p.event(ctx, "stop"); err != nil {
		return err
	}
	r.Session = sb.IngressSessionIdentity{}
	p.resources[r.Binding.EndpointID] = r
	return nil
}
func (p *ownedTestProvider) StatusOwnedIngress(ctx context.Context, r sb.IngressResource) (sb.OwnedIngressStatus, error) {
	if err := p.event(ctx, "status"); err != nil {
		return sb.OwnedIngressStatus{}, err
	}
	current := p.resources[r.Binding.EndpointID]
	return sb.OwnedIngressStatus{Ready: current.Session.ID != "", SessionID: current.Session.ID}, nil
}
func (p *ownedTestProvider) ReleaseOwnedIngress(ctx context.Context, r sb.IngressResource) error {
	if err := p.event(ctx, "release"); err != nil {
		return err
	}
	delete(p.resources, r.Binding.EndpointID)
	return nil
}

type testProviderRegistry struct{ provider sb.Provider }

func (r testProviderRegistry) Providers() []string                 { return []string{"mock"} }
func (r testProviderRegistry) Resolve(string) (sb.Provider, error) { return r.provider, nil }

type contextCredentialResolver struct {
	calls    int
	contexts []context.Context
}

func (r *contextCredentialResolver) ResolveCredential(ctx context.Context, ref string) ([]byte, error) {
	r.calls++
	r.contexts = append(r.contexts, ctx)
	if ref != "host:test" {
		return nil, errors.New("unknown reference")
	}
	return []byte(credentialCanary), nil
}

type ownedFixture struct {
	client   *sb.Client
	store    *ownedTestStore
	provider *ownedTestProvider
	options  sb.Options
	spec     sb.IngressSpec
	resolver *contextCredentialResolver
}

func newOwnedFixture(t *testing.T) ownedFixture {
	t.Helper()
	dir := t.TempDir()
	store := &ownedTestStore{cfg: sb.Config{TLD: "test", Tunnel: sb.Tunnels{Providers: map[string]sb.TunnelProviderConfig{"mock": {CredentialRef: "host:test"}}}, Routes: []sb.Route{{Host: "untouched.test", Dial: "127.0.0.1:9090"}}}}
	provider := &ownedTestProvider{resources: map[string]sb.IngressResource{}}
	resolver := &contextCredentialResolver{}
	opts := sb.Options{ConfigPath: filepath.Join(dir, "config.yaml"), Store: store, ProviderRegistry: testProviderRegistry{provider}, Ingress: isolatedOptions(dir), ApplyFunc: func(string, sb.Config) error { t.Fatal("isolated client applied Caddy config"); return nil }}
	opts.Ingress.Credentials = resolver
	spec := sb.IngressSpec{Name: "proof", Provider: "mock", PublicHost: "proof.example.test", LocalPort: 8080, CallbackPath: "/view", Owner: sb.IngressOwnership{System: "camstream", ScopeID: "fixture"}, Binding: &sb.EndpointBinding{HTTPSPort: 8443, Path: "/view", ExpectedOrigin: "https://proof.example.test:8443"}}
	return ownedFixture{sb.New(opts), store, provider, opts, spec, resolver}
}
func refFor(v sb.Ingress) sb.IngressRef {
	return sb.IngressRef{Name: v.Name, Owner: v.Owner, ExpectedRevision: &v.Revision}
}

func TestOwnedIngressLibraryLifecycleWithoutCLIOrCaddy(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("SWITCHD_CONFIG_PATH", "/must-not-use/global.yaml")
	t.Setenv("TUNNEL_TOKEN", "unrelated-token")
	f := newOwnedFixture(t)
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), ownedContextKey{}, "present"), time.Minute)
	defer cancel()
	beforeRoutes := append([]sb.Route(nil), f.store.cfg.Routes...)
	ensured, err := f.client.EnsureIngress(ctx, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.CallbackURL != "https://proof.example.test:8443/view" || ensured.Revision != 1 || ensured.Binding.EndpointID == "" {
		t.Fatalf("wrong ensured result: %#v", ensured)
	}
	eventCount := len(f.provider.events)
	again, err := f.client.EnsureIngress(ctx, f.spec)
	if err != nil || again.Revision != ensured.Revision || len(f.provider.events) != eventCount {
		t.Fatalf("ensure is not idempotent: %v", err)
	}
	started, err := f.client.StartIngress(ctx, refFor(ensured))
	if err != nil {
		t.Fatal(err)
	}
	if started.State != sb.IngressStateRunning || started.SessionID == "" {
		t.Fatalf("not running: %#v", started)
	}
	// Recreate the client with empty service memory; durable resource identity is
	// passed to the injected provider for status/cleanup.
	recovered := sb.New(f.options)
	status, err := recovered.StatusIngress(ctx, refFor(started))
	if err != nil || status.State != sb.IngressStateRunning {
		t.Fatalf("status after restart: %#v %v", status, err)
	}
	stopped, err := recovered.StopIngress(ctx, refFor(started))
	if err != nil {
		t.Fatal(err)
	}
	if stopped.SessionID != "" || len(f.provider.resources) != 1 {
		t.Fatal("stop did not retain restartable endpoint")
	}
	replay, err := recovered.StopIngress(ctx, refFor(stopped))
	if err != nil || replay.Revision != stopped.Revision {
		t.Fatalf("repeated stop changed revision: %v", err)
	}
	restarted, err := recovered.StartIngress(ctx, refFor(stopped))
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.ReleaseIngress(ctx, refFor(restarted)); err != nil {
		t.Fatal(err)
	}
	if err := recovered.ReleaseIngress(ctx, refFor(restarted)); err != nil {
		t.Fatal(err)
	}
	if len(f.store.cfg.Apps) != 0 || len(f.provider.resources) != 0 || !reflect.DeepEqual(beforeRoutes, f.store.cfg.Routes) {
		t.Fatal("release changed unrelated routes or left owned resources")
	}
	if f.provider.calls != 0 {
		t.Fatal("owned path used legacy provider methods")
	}
	for _, seen := range append(f.provider.contexts, f.resolver.contexts...) {
		if seen.Value(ownedContextKey{}) != "present" {
			t.Fatal("lost caller context")
		}
		if _, ok := seen.Deadline(); !ok {
			t.Fatal("lost deadline")
		}
	}
	for _, b := range f.provider.secretBuffer {
		if b != 0 {
			t.Fatal("credential buffer retained after operation")
		}
	}
	if os.Getenv("TUNNEL_TOKEN") != "unrelated-token" {
		t.Fatal("global environment changed")
	}
	raw, _ := json.Marshal(f.store.cfg)
	if strings.Contains(string(raw), credentialCanary) {
		t.Fatal("secret in config")
	}
	raw, _ = json.Marshal(f.store.progress)
	if strings.Contains(string(raw), credentialCanary) {
		t.Fatal("secret in journal")
	}
	if len(f.store.progress) < 3 || f.store.progress[0].Phase != "pending" || f.store.progress[1].Phase != "confirmed" || f.store.progress[2].Phase != "persisting" {
		t.Fatalf("missing mutation ordering: %#v", f.store.progress)
	}
}

func TestOwnedIngressRejectsInvalidBindingBeforeEffects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sb.IngressSpec)
	}{
		{"port", func(s *sb.IngressSpec) { s.Binding.HTTPSPort = -1 }},
		{"origin", func(s *sb.IngressSpec) { s.Binding.ExpectedOrigin = "https://other.test" }},
		{"path", func(s *sb.IngressSpec) { s.Binding.Path = "/outside" }},
		{"encoded path", func(s *sb.IngressSpec) { s.CallbackPath = "/view/%2e%2e/private" }},
		{"target", func(s *sb.IngressSpec) { s.DialHost = "0.0.0.0" }},
		{"hostname", func(s *sb.IngressSpec) { s.PublicHost = "bad host.test" }},
		{"unowned endpoint", func(s *sb.IngressSpec) { s.Binding.EndpointID = "someone-else" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnedFixture(t)
			tc.mutate(&f.spec)
			if _, err := f.client.EnsureIngress(context.Background(), f.spec); err == nil {
				t.Fatal("invalid binding accepted")
			}
			if len(f.provider.events) != 0 || f.store.saves != 0 || f.resolver.calls != 0 {
				t.Fatal("invalid input caused provider/config/credential effects")
			}
		})
	}
}

func TestOwnedIngressConflictsAndLegacyRecoveryFailBeforeEffects(t *testing.T) {
	f := newOwnedFixture(t)
	ctx := context.Background()
	current, err := f.client.EnsureIngress(ctx, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	count, saves := len(f.provider.events), f.store.saves
	bad := refFor(current)
	bad.Owner.ScopeID = "other"
	if _, err := f.client.StartIngress(ctx, bad); err == nil {
		t.Fatal("owner conflict accepted")
	}
	bad = refFor(current)
	zero := int64(0)
	bad.ExpectedRevision = &zero
	if err := f.client.ReleaseIngress(ctx, bad); err == nil {
		t.Fatal("stale revision accepted")
	}
	spec := f.spec
	binding := *spec.Binding
	binding.EndpointID = current.EndpointID
	spec.Binding = &binding
	if _, err := f.client.EnsureIngress(ctx, spec); err == nil {
		t.Fatal("explicit endpoint resumed without expected revision")
	}
	if count != len(f.provider.events) || saves != f.store.saves {
		t.Fatal("conflict caused mutation")
	}
	legacyOpts := f.options
	legacyOpts.Ingress = nil
	legacy := sb.New(legacyOpts)
	if _, err := legacy.StopIngress(ctx, refFor(current)); err == nil {
		t.Fatal("legacy client bypassed isolated cleanup")
	}
	if err := legacy.AppDown(current.Name); err == nil {
		t.Fatal("legacy app cleanup bypassed isolated cleanup")
	}
	if err := legacy.RemoveApp(current.Name); err == nil {
		t.Fatal("legacy remove discarded ownership")
	}
	delete(f.store.cfg.Apps[0].Metadata, "switchboard.ingress.resource_v1")
	if _, err := f.client.StopIngress(ctx, refFor(current)); !errors.Is(err, sb.ErrIngressSafetyUnavailable) {
		t.Fatalf("unknown identity accepted: %v", err)
	}
	if count != len(f.provider.events) || saves != f.store.saves {
		t.Fatal("recovery refusal caused mutation")
	}
}

func TestOwnedIngressFailuresRetainPrivateRecoveryState(t *testing.T) {
	for _, action := range []string{"validate", "init", "ensure", "start", "stop", "release"} {
		t.Run(action, func(t *testing.T) {
			f := newOwnedFixture(t)
			ctx := context.Background()
			var current sb.Ingress
			var err error
			if action == "start" || action == "stop" || action == "release" {
				current, err = f.client.EnsureIngress(ctx, f.spec)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.provider.failAction = action
			switch action {
			case "start":
				_, err = f.client.StartIngress(ctx, refFor(current))
			case "stop":
				_, err = f.client.StopIngress(ctx, refFor(current))
			case "release":
				err = f.client.ReleaseIngress(ctx, refFor(current))
			default:
				_, err = f.client.EnsureIngress(ctx, f.spec)
			}
			if !errors.Is(err, sb.ErrIngressProviderFailure) || strings.Contains(err.Error(), credentialCanary) {
				t.Fatalf("unsafe failure: %v", err)
			}
			if current.Name != "" && len(f.store.cfg.Apps) != 1 {
				t.Fatal("failure discarded ownership")
			}
			if action != "validate" && action != "init" && f.store.progress[len(f.store.progress)-1].Phase != "uncertain" {
				t.Fatal("failed provider result not recorded as uncertain")
			}
		})
	}
	f := newOwnedFixture(t)
	f.provider.leakResult = true
	if _, err := f.client.EnsureIngress(context.Background(), f.spec); err == nil || strings.Contains(err.Error(), credentialCanary) {
		t.Fatalf("secret result not rejected: %v", err)
	}
	if f.store.saves != 0 {
		t.Fatal("secret result persisted")
	}
	raw, _ := json.Marshal(f.store.progress)
	if strings.Contains(string(raw), credentialCanary) {
		t.Fatal("secret result in journal")
	}
	for _, phase := range []string{"pending", "confirmed", "persisting"} {
		t.Run("journal-"+phase, func(t *testing.T) {
			f := newOwnedFixture(t)
			f.store.failRecord = phase
			if _, err := f.client.EnsureIngress(context.Background(), f.spec); err == nil {
				t.Fatal("journal failure ignored")
			}
			if f.store.saves != 0 {
				t.Fatal("save followed failed journal")
			}
			if phase == "pending" && len(f.provider.resources) != 0 {
				t.Fatal("provider mutation preceded durable intent")
			}
		})
	}
	f = newOwnedFixture(t)
	f.store.failSave = true
	if _, err := f.client.EnsureIngress(context.Background(), f.spec); err == nil {
		t.Fatal("save failure ignored")
	}
	if f.store.progress[len(f.store.progress)-1].Phase != "persisting" {
		t.Fatal("save failure lost recovery progress")
	}
}

func TestOwnedIngressMissingCapabilitiesAndCancellation(t *testing.T) {
	f := newOwnedFixture(t)
	base := &legacyTestProvider{}
	opts := f.options
	opts.ProviderRegistry = testProviderRegistry{base}
	if _, err := sb.New(opts).EnsureIngress(context.Background(), f.spec); !errors.Is(err, sb.ErrIngressSafetyUnavailable) {
		t.Fatalf("missing owned capability: %v", err)
	}
	if base.calls != 0 || f.store.saves != 0 {
		t.Fatal("missing capability had effects")
	}
	// An owned-only wrapper must not accidentally gain the private capability.
	type ownedOnly struct {
		*legacyTestProvider
		sb.OwnedIngressProvider
	}
	opts.ProviderRegistry = testProviderRegistry{ownedOnly{base, f.provider}}
	if _, err := sb.New(opts).EnsureIngress(context.Background(), f.spec); !errors.Is(err, sb.ErrIngressSafetyUnavailable) {
		t.Fatalf("missing private capability: %v", err)
	}
	if len(f.provider.events) != 0 || f.resolver.calls != 0 {
		t.Fatal("private capability refusal happened too late")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client.EnsureIngress(ctx, f.spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
}

func TestOwnedIngressHelperInjectionAndDetection(t *testing.T) {
	f := newOwnedFixture(t)
	helper := filepath.Join(t.TempDir(), "helper")
	// This helper must never be executed by discovery or this fake provider.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	f.options.Ingress.Helpers = map[string]string{"mock": helper}
	client := sb.New(f.options)
	f.options.Ingress.Helpers["mock"] = "/changed-after-new"
	if _, err := client.EnsureIngress(context.Background(), f.spec); err != nil {
		t.Fatal(err)
	}
	if f.provider.runtime.HelperPath != helper {
		t.Fatal("helper configuration not copied/forwarded")
	}
	f = newOwnedFixture(t)
	f.options.Ingress.Helpers = map[string]string{"mock": "/missing/t04/helper"}
	if _, err := sb.New(f.options).EnsureIngress(context.Background(), f.spec); err == nil {
		t.Fatal("missing helper accepted")
	}
	if len(f.provider.events) != 0 || f.resolver.calls != 0 || f.store.saves != 0 {
		t.Fatal("helper failure had effects")
	}
}

func TestOwnedIngressUnavailableSessionRequiresExplicitRecovery(t *testing.T) {
	f := newOwnedFixture(t)
	ctx := context.Background()
	current, err := f.client.EnsureIngress(ctx, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	running, err := f.client.StartIngress(ctx, refFor(current))
	if err != nil {
		t.Fatal(err)
	}
	remote := f.provider.resources[running.EndpointID]
	remote.Session = sb.IngressSessionIdentity{}
	f.provider.resources[running.EndpointID] = remote
	saves := f.store.saves
	status, err := f.client.StatusIngress(ctx, refFor(running))
	if err != nil || status.State != sb.IngressStateStopped {
		t.Fatalf("unavailable status: %#v %v", status, err)
	}
	if _, err := f.client.StartIngress(ctx, refFor(running)); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("start silently succeeded without an owned session: %v", err)
	}
	if f.store.saves != saves {
		t.Fatal("observation restarted or rewrote the session")
	}
	stopped, err := f.client.StopIngress(ctx, refFor(running))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.StartIngress(ctx, refFor(stopped)); err != nil {
		t.Fatal(err)
	}
}

type injectedHelper struct{ seen string }

func (h *injectedHelper) ResolveIngressHelper(_ context.Context, _ string, configured string) (string, error) {
	h.seen = configured
	return "/injected/helper", nil
}

func TestOwnedIngressOptionalHelperAndCredentialCapabilities(t *testing.T) {
	f := newOwnedFixture(t)
	helper := &injectedHelper{}
	f.options.Ingress.HelperResolver = helper
	f.options.Ingress.Helpers = map[string]string{"mock": "/configured/helper"}
	providerCfg := f.store.cfg.Tunnel.Providers["mock"]
	providerCfg.CredentialRef = ""
	f.store.cfg.Tunnel.Providers["mock"] = providerCfg
	type ownedOnly struct {
		*legacyTestProvider
		sb.OwnedIngressProvider
	}
	legacy := &legacyTestProvider{}
	f.options.ProviderRegistry = testProviderRegistry{ownedOnly{legacy, f.provider}}
	if _, err := sb.New(f.options).EnsureIngress(context.Background(), f.spec); err != nil {
		t.Fatal(err)
	}
	if helper.seen != "/configured/helper" || f.provider.runtime.HelperPath != "/injected/helper" || legacy.calls != 1 || f.resolver.calls != 0 {
		t.Fatal("optional helper/initializer capability was not forwarded correctly")
	}
}

func TestOwnedIngressInterruptedMutationCannotBlindlyRetry(t *testing.T) {
	f := newOwnedFixture(t)
	f.store.interrupted = true
	if _, err := f.client.EnsureIngress(context.Background(), f.spec); !errors.Is(err, sb.ErrIngressSafetyUnavailable) || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("interrupted mutation accepted: %v", err)
	}
	if len(f.provider.events) != 0 || f.store.saves != 0 || f.resolver.calls != 0 {
		t.Fatal("retry caused effects before reconciliation")
	}
}

// The production default disk coordinator and optional recovery/private
// capabilities are exercised through the exported facade, without any CLI.
type recoveringTestProvider struct {
	*ownedTestProvider
	loseReply bool
}

func (p *recoveringTestProvider) EnsureOwnedIngress(ctx context.Context, r sb.IngressResource) (sb.IngressResource, error) {
	updated, err := p.ownedTestProvider.EnsureOwnedIngress(ctx, r)
	if err == nil && p.loseReply {
		p.loseReply = false
		return r, errors.New(credentialCanary)
	}
	return updated, err
}
func (p *recoveringTestProvider) ReconcileOwnedIngress(_ context.Context, progress sb.IngressProgress) (sb.IngressRecoveryResult, error) {
	resource := p.resources["endpoint-"+progress.Resource.Name]
	if resource.OperationID != progress.Resource.OperationID {
		return sb.IngressRecoveryResult{Outcome: "unknown"}, nil
	}
	return sb.IngressRecoveryResult{Outcome: "applied", Resource: resource}, nil
}
func (p *recoveringTestProvider) VerifyOwnedIngressSession(context.Context, sb.IngressResource) error {
	return nil
}

func TestDefaultIsolatedStoreRecoversThroughFacade(t *testing.T) {
	f := newOwnedFixture(t)
	options := f.options
	options.Store = nil
	provider := &recoveringTestProvider{ownedTestProvider: f.provider, loseReply: true}
	options.ProviderRegistry = testProviderRegistry{provider: provider}
	if err := sb.New(sb.Options{ConfigPath: options.ConfigPath}).SaveConfig(f.store.cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if _, err := sb.New(options).EnsureIngress(context.Background(), f.spec); err == nil || strings.Contains(err.Error(), credentialCanary) {
		t.Fatalf("lost reply=%v", err)
	}
	journals, journalErr := filepath.Glob(filepath.Join(options.Ingress.StateDir, "*.json"))
	if journalErr != nil || len(journals) != 1 {
		t.Fatal("missing recovery evidence", journals, journalErr)
	}
	for _, path := range journals {
		raw, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(raw), credentialCanary) {
			t.Fatal("credential in journal", err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("journal permissions", err)
		}
	}
	result, err := sb.New(options).EnsureIngress(context.Background(), f.spec)
	if err != nil || result.Revision != 1 {
		t.Fatalf("recover=%+v %v", result, err)
	}
	calls := 0
	for _, event := range provider.events {
		if event == "ensure" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate provision: %d", calls)
	}
	paths, err := filepath.Glob(filepath.Join(options.Ingress.StateDir, "*.json"))
	if err != nil || len(paths) != 0 {
		t.Fatal("unfinished progress", paths, err)
	}
	raw, err := os.ReadFile(options.ConfigPath)
	if err != nil || strings.Contains(string(raw), credentialCanary) {
		t.Fatal("credential persisted", err)
	}
}
