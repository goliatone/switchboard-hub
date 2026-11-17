package switchboard_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sb "github.com/goliatone/switchboard-hub/pkg/switchboard"
	"gopkg.in/yaml.v3"
)

// These deliberately implement the unchanged consumer interfaces only.
type untouchedStore struct{ calls int }

func (s *untouchedStore) Load(string) (sb.Config, error)          { s.calls++; return sb.Config{}, nil }
func (s *untouchedStore) LoadOrDefault(string) (sb.Config, error) { s.calls++; return sb.Config{}, nil }
func (s *untouchedStore) LoadOrCreateDefault(string) (sb.Config, error) {
	s.calls++
	return sb.Config{}, nil
}
func (s *untouchedStore) Save(string, sb.Config) error { s.calls++; return nil }

type untouchedRegistry struct{ calls int }

func (r *untouchedRegistry) Providers() []string { r.calls++; return nil }
func (r *untouchedRegistry) Resolve(string) (sb.Provider, error) {
	r.calls++
	return nil, errors.New("unexpected provider resolution")
}

type dummyResolver struct {
	Secret string
	calls  int
}

func (r *dummyResolver) ResolveCredential(context.Context, string) ([]byte, error) {
	r.calls++
	return []byte(r.Secret), nil
}

func isolatedOptions(dir string) *sb.IngressOptions {
	return &sb.IngressOptions{Mode: sb.IngressModeTunnelOnly, StateDir: filepath.Join(dir, "state"), RuntimeDir: filepath.Join(dir, "runtime")}
}

func TestIsolatedIngressRejectsIncompleteConfigurationBeforeIO(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SWITCHD_CONFIG_PATH", filepath.Join(root, "global.yaml"))
	cases := []struct {
		name   string
		config string
		change func(*sb.IngressOptions)
	}{
		{"no config", "", func(*sb.IngressOptions) {}},
		{"relative config", "local.yaml", func(*sb.IngressOptions) {}},
		{"missing mode", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.Mode = "" }},
		{"unknown mode", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.Mode = "auto" }},
		{"missing state", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.StateDir = "" }},
		{"relative runtime", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.RuntimeDir = "runtime" }},
		{"same directories", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.RuntimeDir = o.StateDir }},
		{"runtime under state", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.RuntimeDir = filepath.Join(o.StateDir, "tmp") }},
		{"state under runtime", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.StateDir = filepath.Join(o.RuntimeDir, "state") }},
		{"config under runtime", filepath.Join(root, "runtime", "config.yaml"), func(*sb.IngressOptions) {}},
		{"runtime root", filepath.Join(root, "config.yaml"), func(o *sb.IngressOptions) { o.RuntimeDir = string(filepath.Separator) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, registry := &untouchedStore{}, &untouchedRegistry{}
			opts := isolatedOptions(root)
			tc.change(opts)
			client := sb.New(sb.Options{ConfigPath: tc.config, Ingress: opts, Store: store, ProviderRegistry: registry})
			if _, err := client.ConfigPath(); err == nil {
				t.Fatal("invalid options accepted")
			}
			if _, err := client.LoadConfig(); err == nil {
				t.Fatal("invalid options reached store")
			}
			if _, err := client.EnsureIngress(context.Background(), sb.IngressSpec{}); err == nil {
				t.Fatal("invalid options reached mutation")
			}
			if store.calls != 0 || registry.calls != 0 {
				t.Fatalf("side effects: store=%d provider=%d", store.calls, registry.calls)
			}
		})
	}
}

func TestIsolatedIngressCannotFallThroughToLegacyMutations(t *testing.T) {
	dir := t.TempDir()
	store, registry := &untouchedStore{}, &untouchedRegistry{}
	resolver := &dummyResolver{Secret: "DUMMY-DO-NOT-DISCLOSE"}
	opts := isolatedOptions(dir)
	opts.Credentials = resolver
	applyCalls := 0
	client := sb.New(sb.Options{ConfigPath: filepath.Join(dir, "config.yaml"), Ingress: opts, Store: store, ProviderRegistry: registry, ApplyFunc: func(string, sb.Config) error { applyCalls++; return nil }})
	ctx := context.Background()
	ref := sb.IngressRef{Name: "proof", Owner: sb.IngressOwnership{System: "camstream", ScopeID: "test"}}
	actions := []func() error{
		func() error {
			_, err := client.EnsureIngress(ctx, sb.IngressSpec{Name: "proof", LocalPort: 8080, Provider: "mock", PublicHost: "proof.example.com", CallbackPath: "/", Owner: ref.Owner})
			return err
		},
		func() error { _, err := client.StartIngress(ctx, ref); return err },
		func() error { _, err := client.StopIngress(ctx, ref); return err },
		func() error { return client.ReleaseIngress(ctx, ref) },
		func() error { _, err := client.LoadOrCreateDefaultConfig(); return err },
		func() error { return client.SaveConfig(sb.Config{}) },
		func() error { return client.CreateApp("proof", 8080, nil) },
		func() error { return client.RemoveApp("proof") },
		func() error { return client.ExposeApp("proof", "mock", "proof.example.com") },
		func() error { return client.AppUp("proof") },
		func() error { return client.AppDown("proof") },
		func() error { return client.TunnelInit("mock", sb.TunnelInitOptions{}) },
	}
	for i, act := range actions {
		if err := act(); !errors.Is(err, sb.ErrIngressSafetyUnavailable) {
			t.Fatalf("action %d: expected safety refusal, got %v", i, err)
		}
	}
	if store.calls != 0 || registry.calls != 0 || applyCalls != 0 || resolver.calls != 0 {
		t.Fatalf("unexpected I/O: store=%d provider=%d apply=%d credentials=%d", store.calls, registry.calls, applyCalls, resolver.calls)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("created runtime/config artifacts: %v", entries)
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		data, err := marshal(opts)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), resolver.Secret) {
			t.Fatal("serialized credential resolver secret")
		}
	}
}

func TestIsolatedOptionsAreCopiedAndLegacyPathResolutionPreserved(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.yaml")
	explicit := filepath.Join(dir, "isolated.yaml")
	t.Setenv("SWITCHD_CONFIG_PATH", global)
	legacy := sb.New(sb.Options{})
	if got, err := legacy.ConfigPath(); err != nil || got != global {
		t.Fatalf("legacy resolution: %q %v", got, err)
	}
	opts := isolatedOptions(dir)
	client := sb.New(sb.Options{ConfigPath: explicit, Ingress: opts})
	opts.Mode = "invalid"
	opts.StateDir = ""
	if got, err := client.ConfigPath(); err != nil || got != explicit {
		t.Fatalf("isolated config changed after New: %q %v", got, err)
	}
}

func TestCredentialReferenceRoundTripPreservesLegacyConfiguration(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	registry := &untouchedRegistry{}
	client := sb.New(sb.Options{ConfigPath: configPath, ProviderRegistry: registry})
	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tunnel.Providers["mock"] = sb.TunnelProviderConfig{Enabled: true, AccountID: "account", Zone: "zone", CredentialRef: "keychain:camstream/test", Values: map[string]string{"binary": "/bundled/cloudflared"}}
	cfg.Routes = []sb.Route{{Host: "preserve.test", Dial: "127.0.0.1:1234"}}
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// Legacy loading migrates route-only records to app records in memory.
	baseline, err := client.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline.Tunnel, cfg.Tunnel) {
		t.Fatal("provider fields changed on round trip")
	}
	readClient := sb.New(sb.Options{ConfigPath: configPath, Ingress: isolatedOptions(dir), ProviderRegistry: registry})
	got, err := readClient.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline, got) {
		t.Fatalf("configuration round trip changed fields\nwant %#v\ngot %#v", baseline, got)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "credential_ref: keychain:camstream/test") {
		t.Fatal("credential reference not persisted")
	}
}
