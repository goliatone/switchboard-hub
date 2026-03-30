package app

import (
	"errors"
	"path/filepath"
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

func TestCfgPathCanBeOverriddenForTesting(t *testing.T) {
	want := filepath.Join(t.TempDir(), "switchd-config.yaml")
	t.Setenv("SWITCHD_CONFIG_PATH", want)
	got, err := cfgPath()
	if err != nil {
		t.Fatalf("cfgPath returned error: %v", err)
	}
	if got != want {
		t.Fatalf("unexpected cfgPath override: got=%q want=%q", got, want)
	}
}

func TestStatusReportInfoIncludesChecksAndApps(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Caddy.Admin = "http://127.0.0.1:2019"
	cfg.Caddy.TLS.Enabled = true
	cfg.Caddy.TLS.Mode = "internal"
	cfg.Apps = []config.App{
		{Name: "web", LocalHost: "web.test", LocalPort: 3000},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	origExists := statusCommandExists
	origRunCapture := statusRunCapture
	origCheckCaddy := statusCheckCaddy
	defer func() {
		statusCommandExists = origExists
		statusRunCapture = origRunCapture
		statusCheckCaddy = origCheckCaddy
	}()

	statusCommandExists = func(name string) bool { return name == "dig" }
	statusRunCapture = func(name string, args ...string) (string, error) {
		return "10.0.0.1\n", nil
	}
	statusCheckCaddy = func(string) error { return nil }

	report, err := StatusReportInfo()
	if err != nil {
		t.Fatalf("StatusReportInfo returned error: %v", err)
	}
	if report.TLS.Mode != "internal" || !report.TLS.Valid {
		t.Fatalf("unexpected tls report: %#v", report.TLS)
	}
	if report.DNS.Status != "ok" {
		t.Fatalf("unexpected dns report: %#v", report.DNS)
	}
	if report.Caddy.Status != "ok" {
		t.Fatalf("unexpected caddy report: %#v", report.Caddy)
	}
	if len(report.Apps) != 1 || report.Apps[0].Name != "web" {
		t.Fatalf("unexpected apps report: %#v", report.Apps)
	}
	if len(report.TunnelHealth) != 1 || report.TunnelHealth[0].Status != "none" {
		t.Fatalf("unexpected tunnel health: %#v", report.TunnelHealth)
	}
}

func TestStatusReportInfoSetsCaddyStartHintWhenUnavailable(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	origExists := statusCommandExists
	origRunCapture := statusRunCapture
	origCheckCaddy := statusCheckCaddy
	defer func() {
		statusCommandExists = origExists
		statusRunCapture = origRunCapture
		statusCheckCaddy = origCheckCaddy
	}()

	statusCommandExists = func(string) bool { return false }
	statusRunCapture = func(string, ...string) (string, error) {
		return "", nil
	}
	statusCheckCaddy = func(string) error { return errors.New("connection refused") }

	report, err := StatusReportInfo()
	if err != nil {
		t.Fatalf("StatusReportInfo returned error: %v", err)
	}
	if report.Caddy.Status != "error" {
		t.Fatalf("unexpected caddy status: %#v", report.Caddy)
	}
	if report.Caddy.StartHint == "" {
		t.Fatalf("expected start hint in caddy status: %#v", report.Caddy)
	}
}

func TestAddRouteWithoutDialHostPreservesAutoAppDialHost(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	cfg := config.Default("test", "10.0.0.1")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}

	if err := AddRoute("demo", 3000, ""); err != nil {
		t.Fatalf("AddRoute returned error: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if len(got.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(got.Apps))
	}
	if got.Apps[0].DialHost != "" {
		t.Fatalf("expected auto dial host, got %q", got.Apps[0].DialHost)
	}
}
