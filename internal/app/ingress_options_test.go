package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type credentialInitProbe struct {
	mockProvider
	calls int
}

func (p *credentialInitProbe) Init(context.Context, tunnel.ProviderConfig) error {
	p.calls++
	return nil
}

func TestCredentialReferenceNeverFallsBackToLegacyInit(t *testing.T) {
	cfg := config.Default("test", "127.0.0.1")
	cfg.Tunnel.Providers["mock"] = config.TunnelProviderCfg{CredentialRef: "private-reference-not-for-errors"}
	provider := &credentialInitProbe{}
	err := initProviderWithConfigContext(context.Background(), provider, cfg, " MOCK ", time.Second)
	if err == nil || !strings.Contains(err.Error(), "credential integration") {
		t.Fatalf("expected actionable refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "private-reference-not-for-errors") {
		t.Fatal("reference disclosed in error")
	}
	if provider.calls != 0 {
		t.Fatal("legacy provider initialized despite unresolved reference")
	}
	cfg.Tunnel.Providers["mock"] = config.TunnelProviderCfg{}
	if err := initProviderWithConfigContext(context.Background(), provider, cfg, "mock", time.Second); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatal("legacy initialization behavior changed")
	}
}
