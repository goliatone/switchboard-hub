package app

import (
	"strings"
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func TestMkcertHostsIncludesWildcardAndRoutes(t *testing.T) {
	c := &config.Config{
		TLD: "test",
		Routes: []config.Route{
			{Host: "api.my-local-app.test"},
			{Host: "my-local-app.test"},
			{Host: "api.my-local-app.test"},
		},
	}

	got := mkcertHosts(c)
	want := []string{"*.test", "api.my-local-app.test", "my-local-app.test"}
	if len(got) != len(want) {
		t.Fatalf("unexpected host count: got=%d want=%d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected host at %d: got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestResolvePathInputRelativeToBaseDir(t *testing.T) {
	base := t.TempDir()
	got, err := resolvePathInput("certs/dev.pem", base)
	if err != nil {
		t.Fatalf("resolvePathInput returned error: %v", err)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("expected path %q to be under base %q", got, base)
	}
}

func TestValidateTLSConfigFileModeRequiresAbsolutePaths(t *testing.T) {
	c := &config.Config{
		Caddy: config.Caddy{
			TLS: config.CaddyTLS{
				Enabled:  true,
				Mode:     "file",
				CertFile: "relative-cert.pem",
				KeyFile:  "relative-key.pem",
			},
		},
	}
	err := validateTLSConfig(c)
	if err == nil {
		t.Fatal("expected validation error for relative TLS paths")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("unexpected error: %v", err)
	}
}
