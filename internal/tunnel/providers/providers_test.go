package providers

import "testing"

func TestRegistryIncludesBuiltInProviders(t *testing.T) {
	r := Registry()
	got := r.Providers()
	if len(got) != 2 {
		t.Fatalf("expected 2 providers, got %d (%v)", len(got), got)
	}
	if got[0] != "cloudflare" || got[1] != "tailscale" {
		t.Fatalf("unexpected providers: %v", got)
	}
}
