package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

func TestRenderLaunchdPlistIncludesExpectedFields(t *testing.T) {
	got, err := renderLaunchdPlist(launchdPlistSpec{
		Label:            launchdServiceLabel,
		ProgramArguments: []string{"/usr/local/bin/switchd", "daemon", "run"},
		Environment: map[string]string{
			"PATH":                     "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
			"SWITCHD_CONFIG_PATH":      "/Users/test/.config/switchboard-hub/config.yaml",
			"SWITCHD_CONFIG_OWNER_UID": "501",
			"SWITCHD_CONFIG_OWNER_GID": "20",
		},
		StandardOutPath: "/var/log/switchboard-hub/stdout.log",
		StandardErrPath: "/var/log/switchboard-hub/stderr.log",
	})
	if err != nil {
		t.Fatalf("renderLaunchdPlist returned error: %v", err)
	}
	out := string(got)
	for _, want := range []string{
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/usr/local/bin/switchd</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<key>SWITCHD_CONFIG_PATH</key>",
		"<string>/Users/test/.config/switchboard-hub/config.yaml</string>",
		"<key>PATH</key>",
		"<string>/var/log/switchboard-hub/stdout.log</string>",
		"<string>/var/log/switchboard-hub/stderr.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plist missing %q\n%s", want, out)
		}
	}
}

func TestServiceStatusInfoReportsStaleRuntimeState(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(filepath.Dir(launchdPlistPath), 0o755); err != nil {
		t.Fatalf("MkdirAll plist dir returned error: %v", err)
	}
	if err := os.WriteFile(launchdPlistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("WriteFile plist returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(serviceStatePath), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeDaemonRuntimeState(daemonRuntimeState{
		PID:        999999,
		CaddyPID:   888888,
		StartedAt:  "2026-03-27T10:00:00Z",
		ConfigPath: "/tmp/config.yaml",
	}); err != nil {
		t.Fatalf("writeDaemonRuntimeState returned error: %v", err)
	}

	st, err := ServiceStatusInfo()
	if err != nil {
		t.Fatalf("ServiceStatusInfo returned error: %v", err)
	}
	if !st.Installed {
		t.Fatal("expected installed service")
	}
	if st.Running {
		t.Fatal("did not expect running service")
	}
	if !st.Stale {
		t.Fatal("expected stale service state")
	}
	if st.PID != 999999 || st.CaddyPID != 888888 {
		t.Fatalf("unexpected pids: %#v", st)
	}
	if st.Phase != "" || st.Ready {
		t.Fatalf("unexpected readiness state: %#v", st)
	}
}

func TestAcquireDaemonLockRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := acquireDaemonLock(path)
	if err != nil {
		t.Fatalf("acquireDaemonLock returned error: %v", err)
	}
	defer first.close()

	if _, err := acquireDaemonLock(path); err == nil {
		t.Fatal("expected duplicate lock error")
	}
}

func TestServiceStartRequiresInstalledPlist(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := ServiceStart(); err == nil || !strings.Contains(err.Error(), "service is not installed") {
		t.Fatalf("expected install error, got %v", err)
	}
}

func TestServiceStartWaitsForReadyState(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(filepath.Dir(launchdPlistPath), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(launchdPlistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	calls := []string{}
	launchctlRun = func(args ...string) (string, error) {
		if len(args) > 0 {
			calls = append(calls, args[0])
		}
		if len(args) > 0 && args[0] == "bootstrap" {
			if len(calls) < 2 || calls[len(calls)-2] != "enable" {
				t.Fatalf("bootstrap ran before enable: %v", calls)
			}
		}
		if len(args) > 0 && args[0] == "kickstart" {
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = writeDaemonRuntimeState(daemonRuntimeState{
					PID:        os.Getpid(),
					CaddyPID:   4242,
					StartedAt:  "2026-03-27T10:00:00Z",
					ConfigPath: "/tmp/config.yaml",
					Ready:      true,
					Phase:      "ready",
				})
			}()
		}
		return "", nil
	}

	if err := ServiceStart(); err != nil {
		t.Fatalf("ServiceStart returned error: %v", err)
	}
	if len(calls) < 4 || calls[0] != "bootout" || calls[1] != "enable" || calls[2] != "bootstrap" || calls[3] != "kickstart" {
		t.Fatalf("unexpected service start order: %v", calls)
	}
}

func TestServiceInstallReloadsExistingDefinition(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	t.Setenv("SWITCHD_CONFIG_PATH", filepath.Join(t.TempDir(), "config.yaml"))
	resolveExecutable = func() (string, error) {
		return "/usr/local/bin/switchd", nil
	}
	defer func() { resolveExecutable = defaultResolveExecutable }()

	calls := []string{}
	launchctlRun = func(args ...string) (string, error) {
		if len(args) > 0 {
			calls = append(calls, args[0])
		}
		if len(args) > 0 && args[0] == "kickstart" {
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = writeDaemonRuntimeState(daemonRuntimeState{
					PID:        os.Getpid(),
					CaddyPID:   4242,
					StartedAt:  "2026-03-27T10:00:00Z",
					ConfigPath: os.Getenv("SWITCHD_CONFIG_PATH"),
					Ready:      true,
					Phase:      "ready",
				})
			}()
		}
		if len(args) > 0 && args[0] == "bootout" {
			return "", fmt.Errorf("launchctl %s: could not find service", strings.Join(args, " "))
		}
		return "", nil
	}
	if err := ServiceInstall(); err != nil {
		t.Fatalf("ServiceInstall returned error: %v", err)
	}

	if len(calls) < 5 || calls[0] != "bootout" || calls[1] != "bootout" || calls[2] != "enable" || calls[3] != "bootstrap" || calls[4] != "kickstart" {
		t.Fatalf("expected install to unload then enable/bootstrap/kickstart, got %v", calls)
	}
}

func TestServiceStopRemovesStaleRuntimeState(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(filepath.Dir(serviceStatePath), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeDaemonRuntimeState(daemonRuntimeState{PID: 999999}); err != nil {
		t.Fatalf("writeDaemonRuntimeState returned error: %v", err)
	}
	launchctlRun = func(args ...string) (string, error) {
		return "", errors.New("launchctl bootout system/com.goliatone.switchd: could not find service")
	}

	if err := ServiceStop(); err != nil {
		t.Fatalf("ServiceStop returned error: %v", err)
	}
	if _, err := os.Stat(serviceStatePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale runtime state removed, stat err=%v", err)
	}
}

func TestBackgroundDaemonResumePersistedApps(t *testing.T) {
	restorePaths := installTestServicePaths(t)
	defer restorePaths()
	restoreProvider := installBackgroundTestProvider(t, &backgroundTestProvider{
		statusReady: false,
		startPID:    4242,
	})
	defer restoreProvider()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Apps = []config.App{
		{
			Name:      "esign",
			LocalHost: "esign.test",
			LocalPort: 3000,
			PublicEndpoint: config.AppPublicEndpoint{
				Provider:             "mock",
				Host:                 "esign.example.com",
				EndpointID:           "endpoint-1",
				ActiveSessionID:      "old-session",
				ActiveSessionPID:     0,
				ActiveSessionStarted: "2026-03-27T10:00:00Z",
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}

	svc := NewService(ServiceOptions{
		ConfigPath: cfgPath,
	})
	d := &backgroundDaemon{
		service:    svc,
		configPath: cfgPath,
		now:        time.Now,
	}
	report, err := d.resumePersistedApps()
	if err != nil {
		t.Fatalf("resumePersistedApps returned error: %v", err)
	}
	if report.ActiveApps != 1 || report.ResumedApps != 1 || len(report.FailedApps) != 0 {
		t.Fatalf("unexpected resume report: %+v", report)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if got.Apps[0].PublicEndpoint.ActiveSessionID != "endpoint-1-session" {
		t.Fatalf("unexpected session after resume: %#v", got.Apps[0].PublicEndpoint)
	}
	if got.Apps[0].PublicEndpoint.ActiveSessionPID != 4242 {
		t.Fatalf("unexpected pid after resume: %#v", got.Apps[0].PublicEndpoint)
	}
}

func TestBackgroundDaemonResumePersistedAppsDegradesOnProviderError(t *testing.T) {
	restorePaths := installTestServicePaths(t)
	defer restorePaths()
	restoreProvider := installBackgroundTestProvider(t, &backgroundTestProvider{
		statusReady: false,
		startErr:    errors.New("cloudflare API token is missing [CF_API_TOKEN_MISSING]"),
	})
	defer restoreProvider()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Apps = []config.App{
		{
			Name:      "esign",
			LocalHost: "esign.test",
			LocalPort: 3000,
			PublicEndpoint: config.AppPublicEndpoint{
				Provider:             "mock",
				Host:                 "esign.example.com",
				EndpointID:           "endpoint-1",
				ActiveSessionID:      "old-session",
				ActiveSessionPID:     0,
				ActiveSessionStarted: "2026-03-27T10:00:00Z",
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}

	svc := NewService(ServiceOptions{
		ConfigPath: cfgPath,
	})
	d := &backgroundDaemon{
		service:    svc,
		configPath: cfgPath,
		now:        time.Now,
	}
	report, err := d.resumePersistedApps()
	if err != nil {
		t.Fatalf("resumePersistedApps returned unexpected error: %v", err)
	}
	if report.ActiveApps != 1 || report.ResumedApps != 0 || len(report.FailedApps) != 1 {
		t.Fatalf("unexpected degraded resume report: %+v", report)
	}
	if !strings.Contains(report.FailedApps[0], "CF_API_TOKEN_MISSING") {
		t.Fatalf("expected actionable provider failure, got %#v", report.FailedApps)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if got.Apps[0].PublicEndpoint.ActiveSessionID != "old-session" {
		t.Fatalf("expected existing persisted session metadata kept for retry, got %#v", got.Apps[0].PublicEndpoint)
	}
}

func TestResolveServiceEnvironmentUsesServiceEnvFile(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Tunnel.Providers["cloudflare"] = config.TunnelProviderCfg{
		Enabled: true,
		Values: map[string]string{
			"mode":          "api",
			"account_id":    "acct-1",
			"zone_id":       "zone-1",
			"base_domain":   "tnl.example.com",
			"api_token_env": "CF_SERVICE_TOKEN",
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}

	envPath := serviceEnvPath(cfgPath)
	if err := os.WriteFile(envPath, []byte("# comment\nexport CF_SERVICE_TOKEN=\"token-from-file\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	report, env, err := resolveServiceEnvironment(cfgPath)
	if err != nil {
		t.Fatalf("resolveServiceEnvironment returned error: %v", err)
	}
	if report.EnvFilePath != envPath {
		t.Fatalf("unexpected env file path %q want %q", report.EnvFilePath, envPath)
	}
	if len(report.RequiredEnvVars) != 1 || report.RequiredEnvVars[0] != "CF_SERVICE_TOKEN" {
		t.Fatalf("unexpected required env vars: %#v", report.RequiredEnvVars)
	}
	if len(report.MissingEnvVars) != 0 {
		t.Fatalf("expected no missing env vars, got %#v", report.MissingEnvVars)
	}
	if len(report.ConfiguredEnvVars) != 1 || report.ConfiguredEnvVars[0] != "CF_SERVICE_TOKEN" {
		t.Fatalf("unexpected configured env vars: %#v", report.ConfiguredEnvVars)
	}
	if got := env["CF_SERVICE_TOKEN"]; got != "token-from-file" {
		t.Fatalf("unexpected env token %q", got)
	}
	if got := env["HOME"]; got == "" {
		t.Fatal("expected HOME in launchd environment")
	}
}

func TestBackgroundCommandEnvInjectsMissingHome(t *testing.T) {
	t.Setenv("HOME", "")

	env := backgroundCommandEnv()
	home := ""
	for _, entry := range env {
		if !strings.HasPrefix(entry, "HOME=") {
			continue
		}
		home = strings.TrimPrefix(entry, "HOME=")
	}
	if home != "/var/root" {
		t.Fatalf("unexpected HOME=%q", home)
	}
}

func TestResolveServiceEnvironmentReportsMissingRequiredVars(t *testing.T) {
	t.Setenv("SWITCHD_CF_API_TOKEN", "")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Tunnel.Providers["cloudflare"] = config.TunnelProviderCfg{
		Enabled: true,
		Values: map[string]string{
			"mode":        "api",
			"account_id":  "acct-1",
			"zone_id":     "zone-1",
			"base_domain": "tnl.example.com",
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}

	report, env, err := resolveServiceEnvironment(cfgPath)
	if err != nil {
		t.Fatalf("resolveServiceEnvironment returned error: %v", err)
	}
	if len(report.RequiredEnvVars) != 1 || report.RequiredEnvVars[0] != "SWITCHD_CF_API_TOKEN" {
		t.Fatalf("unexpected required env vars: %#v", report.RequiredEnvVars)
	}
	if len(report.MissingEnvVars) != 1 || report.MissingEnvVars[0] != "SWITCHD_CF_API_TOKEN" {
		t.Fatalf("unexpected missing env vars: %#v", report.MissingEnvVars)
	}
	if _, ok := env["SWITCHD_CF_API_TOKEN"]; ok {
		t.Fatalf("unexpected injected missing env var in launchd environment: %#v", env)
	}
}

func TestServiceStatusInfoIncludesEnvironmentReport(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()
	t.Setenv("SWITCHD_CF_API_TOKEN", "")

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Tunnel.Providers["cloudflare"] = config.TunnelProviderCfg{
		Enabled: true,
		Values: map[string]string{
			"mode":        "api",
			"account_id":  "acct-1",
			"zone_id":     "zone-1",
			"base_domain": "tnl.example.com",
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	st, err := ServiceStatusInfo()
	if err != nil {
		t.Fatalf("ServiceStatusInfo returned error: %v", err)
	}
	if st.ConfigPath != cfgPath {
		t.Fatalf("unexpected config path %q want %q", st.ConfigPath, cfgPath)
	}
	if st.EnvFilePath != serviceEnvPath(cfgPath) {
		t.Fatalf("unexpected env file path %q", st.EnvFilePath)
	}
	if len(st.MissingEnvVars) != 1 || st.MissingEnvVars[0] != "SWITCHD_CF_API_TOKEN" {
		t.Fatalf("unexpected missing env vars: %#v", st.MissingEnvVars)
	}
}

func TestPrepareServiceEnvironmentCreatesTemplateForMissingVars(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Tunnel.Providers["cloudflare"] = config.TunnelProviderCfg{
		Enabled: true,
		Values: map[string]string{
			"mode":        "api",
			"account_id":  "acct-1",
			"zone_id":     "zone-1",
			"base_domain": "tnl.example.com",
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)
	t.Setenv("SWITCHD_CF_API_TOKEN", "")

	report, err := PrepareServiceEnvironment()
	if err != nil {
		t.Fatalf("PrepareServiceEnvironment returned error: %v", err)
	}
	if !report.EnvFileCreated || !report.EnvFileUpdated {
		t.Fatalf("expected env file creation, got %#v", report)
	}
	if len(report.EnvFileTemplateVars) != 1 || report.EnvFileTemplateVars[0] != "SWITCHD_CF_API_TOKEN" {
		t.Fatalf("unexpected template vars: %#v", report.EnvFileTemplateVars)
	}
	data, err := os.ReadFile(report.EnvFilePath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if got := string(data); !strings.Contains(got, "SWITCHD_CF_API_TOKEN=\n") {
		t.Fatalf("unexpected env file contents %q", got)
	}
}

func TestPrepareServiceEnvironmentAppendsMissingVars(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Tunnel.Providers["cloudflare"] = config.TunnelProviderCfg{
		Enabled: true,
		Values: map[string]string{
			"mode":          "api",
			"account_id":    "acct-1",
			"zone_id":       "zone-1",
			"base_domain":   "tnl.example.com",
			"api_token_env": "CF_SERVICE_TOKEN",
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	envPath := serviceEnvPath(cfgPath)
	if err := os.WriteFile(envPath, []byte("EXISTING_TOKEN=value"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)
	t.Setenv("CF_SERVICE_TOKEN", "")

	report, err := PrepareServiceEnvironment()
	if err != nil {
		t.Fatalf("PrepareServiceEnvironment returned error: %v", err)
	}
	if report.EnvFileCreated {
		t.Fatalf("did not expect env file creation: %#v", report)
	}
	if !report.EnvFileUpdated {
		t.Fatalf("expected env file update: %#v", report)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "EXISTING_TOKEN=value\n") {
		t.Fatalf("expected existing entry to be preserved, got %q", got)
	}
	if !strings.Contains(got, "CF_SERVICE_TOKEN=\n") {
		t.Fatalf("expected missing token placeholder, got %q", got)
	}
}

func TestSaveServiceEnvValuesUpdatesExistingAndAppendsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.env")
	if err := os.WriteFile(path, []byte("# comment\nCF_SERVICE_TOKEN=\nEXISTING=value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := SaveServiceEnvValues(path, map[string]string{
		"CF_SERVICE_TOKEN": "secret-token",
		"NEW_TOKEN":        "another",
	}); err != nil {
		t.Fatalf("SaveServiceEnvValues returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "# comment\n") {
		t.Fatalf("expected comments to be preserved, got %q", got)
	}
	if !strings.Contains(got, "CF_SERVICE_TOKEN=\"secret-token\"\n") {
		t.Fatalf("expected updated token value, got %q", got)
	}
	if !strings.Contains(got, "EXISTING=value\n") {
		t.Fatalf("expected existing value preserved, got %q", got)
	}
	if !strings.Contains(got, "NEW_TOKEN=\"another\"\n") {
		t.Fatalf("expected appended value, got %q", got)
	}
}

func TestEnsureAppRuntimeKeepsAlivePersistedPID(t *testing.T) {
	provider := &backgroundTestProvider{statusReady: false, startPID: 5151}
	reg := tunnel.NewRegistry()
	if err := reg.Register("mock", func() tunnel.Provider { return provider }); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	svc := NewService(ServiceOptions{
		ProviderRegistry: reg,
	})
	cfg := config.Default("test", "10.0.0.1")
	cfg.Apps = []config.App{
		{
			Name:      "esign",
			LocalHost: "esign.test",
			LocalPort: 3000,
			PublicEndpoint: config.AppPublicEndpoint{
				Provider:             "mock",
				Host:                 "esign.example.com",
				EndpointID:           "endpoint-1",
				ActiveSessionID:      "existing-session",
				ActiveSessionPID:     os.Getpid(),
				ActiveSessionStarted: "2026-03-27T10:00:00Z",
			},
		},
	}

	got, err := svc.EnsureAppRuntime(cfg, "esign")
	if err != nil {
		t.Fatalf("EnsureAppRuntime returned error: %v", err)
	}
	if got.PublicEndpoint.ActiveSessionID != "existing-session" {
		t.Fatalf("unexpected session: %#v", got.PublicEndpoint)
	}
	if provider.startCalls != 0 {
		t.Fatalf("expected no provider start, got %d", provider.startCalls)
	}
}

func TestExecBackgroundProcessWaitIsReplayable(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	proc := &execBackgroundProcess{cmd: cmd}
	if err := proc.Wait(); err != nil {
		t.Fatalf("first Wait returned error: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("second Wait returned error: %v", err)
	}
}

func TestBackgroundDaemonShutdownStopsActiveApps(t *testing.T) {
	restoreProvider := installBackgroundTestProvider(t, &backgroundTestProvider{
		statusReady: true,
		startPID:    4242,
	})
	defer restoreProvider()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Apps = []config.App{
		{
			Name:      "esign",
			LocalHost: "esign.test",
			LocalPort: 3000,
			PublicEndpoint: config.AppPublicEndpoint{
				Provider:             "mock",
				Host:                 "esign.example.com",
				EndpointID:           "endpoint-1",
				ActiveSessionID:      "active-session",
				ActiveSessionPID:     4242,
				ActiveSessionStarted: "2026-03-27T10:00:00Z",
			},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}

	svc := NewService(ServiceOptions{
		ConfigPath: cfgPath,
	})
	d := &backgroundDaemon{
		service:    svc,
		configPath: cfgPath,
		now:        time.Now,
	}
	proc := &testDaemonProcess{pid: 5151, done: make(chan struct{})}
	if err := d.shutdown(proc); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if got.Apps[0].PublicEndpoint.ActiveSessionID != "" || got.Apps[0].PublicEndpoint.ActiveSessionPID != 0 {
		t.Fatalf("expected cleared session state, got %#v", got.Apps[0].PublicEndpoint)
	}
	if proc.signalCalls == 0 {
		t.Fatal("expected managed process to receive a signal")
	}
}

func TestServiceLogSnapshotStdoutOnly(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  2,
		Follow: false,
		Stream: "stdout",
		Stdout: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	if got, want := out.String(), "two\nthree\n"; got != want {
		t.Fatalf("stdout snapshot=%q want %q", got, want)
	}
}

func TestServiceLogSnapshotStderrOnly(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("warn\nboom\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  1,
		Follow: false,
		Stream: "stderr",
		Stderr: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	if got, want := out.String(), "boom\n"; got != want {
		t.Fatalf("stderr snapshot=%q want %q", got, want)
	}
}

func TestServiceLogSnapshotJSONStdoutOnly(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  2,
		Follow: false,
		Stream: "stdout",
		JSON:   true,
		Stdout: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	events := decodeServiceLogEvents(t, out.String())
	if got, want := events, []serviceLogEvent{
		{Stream: "stdout", Line: "two"},
		{Stream: "stdout", Line: "three"},
	}; !serviceLogEventsEqual(got, want) {
		t.Fatalf("events=%#v want %#v", got, want)
	}
}

func TestServiceLogSnapshotJSONStderrOnly(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("warn\nboom\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  1,
		Follow: false,
		Stream: "stderr",
		JSON:   true,
		Stderr: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	events := decodeServiceLogEvents(t, out.String())
	if got, want := events, []serviceLogEvent{
		{Stream: "stderr", Line: "boom"},
	}; !serviceLogEventsEqual(got, want) {
		t.Fatalf("events=%#v want %#v", got, want)
	}
}

func TestServiceLogSnapshotJSONCombinedStreams(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("ready\nserving\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stdout returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("warning\npanic\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stderr returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  2,
		Follow: false,
		Stream: "all",
		JSON:   true,
		Stdout: &out,
		Stderr: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	events := decodeServiceLogEvents(t, out.String())
	if got, want := events, []serviceLogEvent{
		{Stream: "stdout", Line: "ready"},
		{Stream: "stdout", Line: "serving"},
		{Stream: "stderr", Line: "warning"},
		{Stream: "stderr", Line: "panic"},
	}; !serviceLogEventsEqual(got, want) {
		t.Fatalf("events=%#v want %#v", got, want)
	}
}

func TestServiceLogSnapshotCombinedPrefixesAndOrder(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("ready\nserving\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stdout returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("warning\npanic\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stderr returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  2,
		Follow: false,
		Stream: "all",
		Stdout: &out,
		Stderr: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	want := "stdout: ready\nstdout: serving\nstderr: warning\nstderr: panic\n"
	if got := out.String(); got != want {
		t.Fatalf("combined snapshot=%q want %q", got, want)
	}
}

func TestServiceLogSnapshotPreservesExistingLines(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("wrote: /Users/test/.config/switchboard-hub/last-applied.json\nready\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stdout returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("{\"level\":\"warn\",\"msg\":\"$HOME environment variable is empty - please fix; some assets might be stored in ./caddy\"}\npanic\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stderr returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  10,
		Follow: false,
		Stream: "all",
		Stdout: &out,
		Stderr: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	want := "stdout: wrote: /Users/test/.config/switchboard-hub/last-applied.json\nstdout: ready\nstderr: {\"level\":\"warn\",\"msg\":\"$HOME environment variable is empty - please fix; some assets might be stored in ./caddy\"}\nstderr: panic\n"
	if got := out.String(); got != want {
		t.Fatalf("snapshot=%q want %q", got, want)
	}
}

func TestServiceLogSnapshotJSONPreservesExistingLinesAndBlankLines(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("wrote: /Users/test/.config/switchboard-hub/last-applied.json\n\nready\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stdout returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("{\"level\":\"warn\",\"msg\":\"$HOME environment variable is empty - please fix; some assets might be stored in ./caddy\"}\npanic\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stderr returned error: %v", err)
	}

	var out strings.Builder
	if err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  10,
		Follow: false,
		Stream: "all",
		JSON:   true,
		Stdout: &out,
		Stderr: &out,
	}); err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}

	events := decodeServiceLogEvents(t, out.String())
	if got, want := events, []serviceLogEvent{
		{Stream: "stdout", Line: "wrote: /Users/test/.config/switchboard-hub/last-applied.json"},
		{Stream: "stdout", Line: ""},
		{Stream: "stdout", Line: "ready"},
		{Stream: "stderr", Line: "{\"level\":\"warn\",\"msg\":\"$HOME environment variable is empty - please fix; some assets might be stored in ./caddy\"}"},
		{Stream: "stderr", Line: "panic"},
	}; !serviceLogEventsEqual(got, want) {
		t.Fatalf("events=%#v want %#v", got, want)
	}
}

func TestServiceLogFollowStreamsAppendedLines(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serviceLogWithContext(ctx, ServiceLogOptions{
			Lines:  0,
			Follow: true,
			Stream: "stdout",
			Stdout: buf,
		})
	}()

	time.Sleep(2 * serviceLogPollInterval)
	if err := appendFile(serviceStdoutPath, "alpha\n"); err != nil {
		t.Fatalf("appendFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "alpha\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}
}

func TestServiceLogFollowStreamsAppendedLinesJSON(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serviceLogWithContext(ctx, ServiceLogOptions{
			Lines:  0,
			Follow: true,
			Stream: "stdout",
			JSON:   true,
			Stdout: buf,
		})
	}()

	time.Sleep(2 * serviceLogPollInterval)
	if err := appendFile(serviceStdoutPath, "alpha\n"); err != nil {
		t.Fatalf("appendFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "{\"stream\":\"stdout\",\"line\":\"alpha\"}\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}
}

func TestServiceLogFollowStreamsFileAppearingLater(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serviceLogWithContext(ctx, ServiceLogOptions{
			Lines:  0,
			Follow: true,
			Stream: "all",
			Stdout: buf,
			Stderr: buf,
		})
	}()

	time.Sleep(2 * serviceLogPollInterval)
	if err := os.WriteFile(serviceStdoutPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "stdout: hello\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}
}

func TestPrepareServiceLogFilesTruncatesExistingLogs(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("old stdout\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stdout returned error: %v", err)
	}
	if err := os.WriteFile(serviceStderrPath, []byte("old stderr\n"), 0o644); err != nil {
		t.Fatalf("WriteFile stderr returned error: %v", err)
	}

	if err := prepareServiceLogFiles(); err != nil {
		t.Fatalf("prepareServiceLogFiles returned error: %v", err)
	}

	for _, path := range []string{serviceStdoutPath, serviceStderrPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s returned error: %v", path, err)
		}
		if len(data) != 0 {
			t.Fatalf("expected truncated log file %s, got %q", path, string(data))
		}
	}
}

func TestServiceLogFollowHandlesRecreatedFiles(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serviceLogWithContext(ctx, ServiceLogOptions{
			Lines:  0,
			Follow: true,
			Stream: "stdout",
			Stdout: buf,
		})
	}()

	time.Sleep(2 * serviceLogPollInterval)
	if err := appendFile(serviceStdoutPath, "before\n"); err != nil {
		t.Fatalf("appendFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "before\n")

	if err := os.Remove(serviceStdoutPath); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("after\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "after\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}
}

func TestServiceLogFollowHandlesRecreatedFilesJSON(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serviceLogWithContext(ctx, ServiceLogOptions{
			Lines:  0,
			Follow: true,
			Stream: "stdout",
			JSON:   true,
			Stdout: buf,
		})
	}()

	time.Sleep(2 * serviceLogPollInterval)
	if err := appendFile(serviceStdoutPath, "before\n"); err != nil {
		t.Fatalf("appendFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "{\"stream\":\"stdout\",\"line\":\"before\"}\n")

	if err := os.Remove(serviceStdoutPath); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if err := os.WriteFile(serviceStdoutPath, []byte("after\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	waitForBufferContains(t, buf, "{\"stream\":\"stdout\",\"line\":\"after\"}\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serviceLogWithContext returned error: %v", err)
	}
}

func TestServiceLogMissingLogsReturnsHelpfulError(t *testing.T) {
	restore := installTestServicePaths(t)
	defer restore()

	err := serviceLogWithContext(context.Background(), ServiceLogOptions{
		Lines:  50,
		Follow: false,
		Stream: "all",
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
	})
	if err == nil {
		t.Fatal("expected missing log error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "service logs not found") {
		t.Fatalf("expected missing logs message, got %q", msg)
	}
	if !strings.Contains(msg, "switchd service status") {
		t.Fatalf("expected status guidance, got %q", msg)
	}
	if !strings.Contains(msg, "service install") || !strings.Contains(msg, "service start") {
		t.Fatalf("expected install/start guidance, got %q", msg)
	}
}

func installTestServicePaths(t *testing.T) func() {
	t.Helper()
	tmp := t.TempDir()
	origPlist := launchdPlistPath
	origRunDir := launchdRuntimeDir
	origLogDir := launchdLogDir
	origState := serviceStatePath
	origLock := serviceLockPath
	origStdout := serviceStdoutPath
	origStderr := serviceStderrPath
	origLaunchctl := launchctlRun
	origUID := effectiveUID

	launchdPlistPath = filepath.Join(tmp, "LaunchDaemons", launchdServiceLabel+".plist")
	launchdRuntimeDir = filepath.Join(tmp, "run")
	launchdLogDir = filepath.Join(tmp, "log")
	serviceStatePath = filepath.Join(launchdRuntimeDir, "daemon-state.json")
	serviceLockPath = filepath.Join(launchdRuntimeDir, "daemon.lock")
	serviceStdoutPath = filepath.Join(launchdLogDir, "switchd.stdout.log")
	serviceStderrPath = filepath.Join(launchdLogDir, "switchd.stderr.log")
	launchctlRun = func(args ...string) (string, error) { return "", nil }
	effectiveUID = func() int { return 0 }

	return func() {
		launchdPlistPath = origPlist
		launchdRuntimeDir = origRunDir
		launchdLogDir = origLogDir
		serviceStatePath = origState
		serviceLockPath = origLock
		serviceStdoutPath = origStdout
		serviceStderrPath = origStderr
		launchctlRun = origLaunchctl
		effectiveUID = origUID
	}
}

func decodeServiceLogEvents(t *testing.T, raw string) []serviceLogEvent {
	t.Helper()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	events := make([]serviceLogEvent, 0, len(lines))
	for _, line := range lines {
		var event serviceLogEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("json.Unmarshal(%q) returned error: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func serviceLogEventsEqual(a, b []serviceLogEvent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func installBackgroundTestProvider(t *testing.T, provider *backgroundTestProvider) func() {
	t.Helper()
	orig := providerRegistryFactory
	providerRegistryFactory = func() *tunnel.Registry {
		reg := tunnel.NewRegistry()
		_ = reg.Register("mock", func() tunnel.Provider { return provider })
		return reg
	}
	return func() {
		providerRegistryFactory = orig
	}
}

type backgroundTestProvider struct {
	statusReady bool
	startPID    int
	startErr    error
	startCalls  int
	stopCalls   int
}

func (p *backgroundTestProvider) Name() string { return "mock" }

func (p *backgroundTestProvider) Capabilities() tunnel.Capabilities {
	return tunnel.Capabilities{
		StableHostname:  true,
		HTTPForwarding:  true,
		HTTPSForwarding: true,
		OAuthSuitable:   true,
	}
}

func (p *backgroundTestProvider) Init(context.Context, tunnel.ProviderConfig) error { return nil }

func (p *backgroundTestProvider) EnsureEndpoint(_ context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	return tunnel.Endpoint{
		ID:       req.PublicHost,
		Provider: "mock",
		Name:     req.Name,
		Host:     req.PublicHost,
		Metadata: req.Metadata,
	}, nil
}

func (p *backgroundTestProvider) Start(_ context.Context, req tunnel.StartRequest) (tunnel.Session, error) {
	p.startCalls++
	if p.startErr != nil {
		return tunnel.Session{}, p.startErr
	}
	return tunnel.Session{
		ID:         req.Endpoint.ID + "-session",
		Provider:   "mock",
		EndpointID: req.Endpoint.ID,
		PID:        p.startPID,
		StartedAt:  time.Date(2026, 3, 27, 10, 0, 0, 0, time.UTC),
	}, nil
}

func (p *backgroundTestProvider) Stop(_ context.Context, sessionID string) error {
	p.stopCalls++
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	return nil
}

func (p *backgroundTestProvider) RemoveEndpoint(context.Context, string) error { return nil }

func (p *backgroundTestProvider) Status(_ context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	return tunnel.EndpointStatus{
		Ready: p.statusReady,
		Endpoint: tunnel.Endpoint{
			ID:       endpointID,
			Provider: "mock",
		},
		Message: "mock status",
	}, nil
}

type testDaemonProcess struct {
	pid         int
	signalCalls int
	killCalls   int
	done        chan struct{}
}

func (p *testDaemonProcess) PID() int { return p.pid }

func (p *testDaemonProcess) Kill() error {
	p.killCalls++
	return nil
}

func (p *testDaemonProcess) Signal(os.Signal) error {
	p.signalCalls++
	if p.done != nil {
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
	return nil
}

type lockedBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func waitForBufferContains(t *testing.T, buf *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %q", want, buf.String())
}

func appendFile(path, contents string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(contents)
	return err
}

func (p *testDaemonProcess) Wait() error {
	if p.done != nil {
		<-p.done
	}
	return nil
}
