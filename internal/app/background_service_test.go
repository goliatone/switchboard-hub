package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	launchctlRun = func(args ...string) (string, error) {
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
		return "", fmt.Errorf("launchctl %s: could not find service", strings.Join(args, " "))
	}
	if err := ServiceInstall(); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("expected bootstrap error from mocked launchctl, got %v", err)
	}

	if len(calls) < 2 || calls[0] != "bootout" || calls[1] != "disable" {
		t.Fatalf("expected install to unload existing definition first, got %v", calls)
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
	if err := d.resumePersistedApps(); err != nil {
		t.Fatalf("resumePersistedApps returned error: %v", err)
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

func (p *testDaemonProcess) Wait() error {
	if p.done != nil {
		<-p.done
	}
	return nil
}
