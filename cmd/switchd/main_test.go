package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/pkg/switchboard"
)

type testCLIRegistry struct {
	provider switchboard.Provider
}

func (r testCLIRegistry) Providers() []string { return []string{"mock"} }

func (r testCLIRegistry) Resolve(string) (switchboard.Provider, error) { return r.provider, nil }

type testCLIProvider struct {
	endpoints map[string]switchboard.Endpoint
	sessions  map[string]switchboard.Session
}

func newTestCLIProvider() *testCLIProvider {
	return &testCLIProvider{
		endpoints: map[string]switchboard.Endpoint{},
		sessions:  map[string]switchboard.Session{},
	}
}

func (p *testCLIProvider) Name() string { return "mock" }

func (p *testCLIProvider) Capabilities() switchboard.Capabilities {
	return switchboard.Capabilities{StableHostname: true, HTTPForwarding: true, HTTPSForwarding: true, OAuthSuitable: true}
}

func (p *testCLIProvider) Init(context.Context, switchboard.ProviderConfig) error { return nil }

func (p *testCLIProvider) EnsureEndpoint(_ context.Context, req switchboard.EndpointRequest) (switchboard.Endpoint, error) {
	ep := switchboard.Endpoint{ID: req.PublicHost, Provider: "mock", Name: req.Name, Host: req.PublicHost, Metadata: req.Metadata}
	p.endpoints[ep.ID] = ep
	return ep, nil
}

func (p *testCLIProvider) Start(_ context.Context, req switchboard.StartRequest) (switchboard.Session, error) {
	session := switchboard.Session{
		ID:         req.Endpoint.ID + "-session",
		Provider:   "mock",
		EndpointID: req.Endpoint.ID,
		PID:        1234,
		StartedAt:  time.Date(2026, 3, 29, 23, 0, 0, 0, time.UTC),
	}
	p.sessions[session.ID] = session
	return session, nil
}

func (p *testCLIProvider) Stop(_ context.Context, sessionID string) error {
	delete(p.sessions, sessionID)
	return nil
}

func (p *testCLIProvider) RemoveEndpoint(_ context.Context, endpointID string) error {
	delete(p.endpoints, endpointID)
	return nil
}

func (p *testCLIProvider) Status(_ context.Context, endpointID string) (switchboard.EndpointStatus, error) {
	for _, session := range p.sessions {
		if session.EndpointID == endpointID {
			return switchboard.EndpointStatus{Ready: true, Endpoint: p.endpoints[endpointID], SessionID: session.ID, Message: "active"}, nil
		}
	}
	return switchboard.EndpointStatus{Ready: false, Endpoint: p.endpoints[endpointID], Message: "inactive"}, nil
}

func TestGlobalFlagsUseJSON(t *testing.T) {
	cases := []struct {
		name string
		in   globalFlags
		want bool
	}{
		{name: "default text", in: globalFlags{Output: "text"}, want: false},
		{name: "explicit json flag", in: globalFlags{JSON: true, Output: "text"}, want: true},
		{name: "output json", in: globalFlags{Output: "json"}, want: true},
		{name: "output json uppercase", in: globalFlags{Output: "JSON"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.useJSON(); got != tc.want {
				t.Fatalf("useJSON()=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestGlobalFlagsUIMode(t *testing.T) {
	cases := []struct {
		name string
		in   globalFlags
		want string
	}{
		{name: "default auto", in: globalFlags{}, want: uiModeAuto},
		{name: "keeps tui", in: globalFlags{UI: "tui"}, want: uiModeTUI},
		{name: "normalizes uppercase", in: globalFlags{UI: "PLAIN"}, want: uiModePlain},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.uiMode(); got != tc.want {
				t.Fatalf("uiMode()=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestRunContextTUIRouting(t *testing.T) {
	cases := []struct {
		name        string
		opts        outputOptions
		check       func(*runContext) (bool, error)
		wantEnabled bool
		wantErr     string
	}{
		{
			name:        "service log auto interactive",
			opts:        outputOptions{UI: uiModeAuto, Interactive: true},
			check:       (*runContext).wantsTUIForServiceLog,
			wantEnabled: true,
		},
		{
			name:        "service log auto non interactive",
			opts:        outputOptions{UI: uiModeAuto, Interactive: false},
			check:       (*runContext).wantsTUIForServiceLog,
			wantEnabled: false,
		},
		{
			name:        "service log plain interactive",
			opts:        outputOptions{UI: uiModePlain, Interactive: true},
			check:       (*runContext).wantsTUIForServiceLog,
			wantEnabled: false,
		},
		{
			name:    "service log explicit tui non interactive errors",
			opts:    outputOptions{UI: uiModeTUI, Interactive: false},
			check:   (*runContext).wantsTUIForServiceLog,
			wantErr: "--ui=tui requires an interactive terminal",
		},
		{
			name:        "status auto interactive stays plain",
			opts:        outputOptions{UI: uiModeAuto, Interactive: true},
			check:       (*runContext).wantsTUIForStatus,
			wantEnabled: false,
		},
		{
			name:        "status explicit tui interactive",
			opts:        outputOptions{UI: uiModeTUI, Interactive: true},
			check:       (*runContext).wantsTUIForStatus,
			wantEnabled: true,
		},
		{
			name:        "app list explicit tui interactive",
			opts:        outputOptions{UI: uiModeTUI, Interactive: true},
			check:       (*runContext).wantsTUIForAppList,
			wantEnabled: true,
		},
		{
			name:        "stack explicit tui interactive",
			opts:        outputOptions{UI: uiModeTUI, Interactive: true},
			check:       (*runContext).wantsTUIForStack,
			wantEnabled: true,
		},
		{
			name:        "service status explicit tui interactive",
			opts:        outputOptions{UI: uiModeTUI, Interactive: true},
			check:       (*runContext).wantsTUIForServiceStatus,
			wantEnabled: true,
		},
		{
			name:        "json disables tui even when explicit",
			opts:        outputOptions{UI: uiModeTUI, Interactive: true, JSON: true},
			check:       (*runContext).wantsTUIForServiceStatus,
			wantEnabled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &runContext{out: cliOutput{opts: tc.opts}}
			got, err := tc.check(r)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantEnabled {
				t.Fatalf("enabled=%v want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestFindAppByInput(t *testing.T) {
	apps := []config.App{
		{Name: "esign", LocalHost: "esign.test"},
		{Name: "api", LocalHost: "api.test"},
	}

	if got, ok := findAppByInput(apps, "esign"); !ok || got.Name != "esign" {
		t.Fatalf("expected name match for esign, got=%#v ok=%v", got, ok)
	}
	if got, ok := findAppByInput(apps, "https://api.test"); !ok || got.Name != "api" {
		t.Fatalf("expected host match for api.test, got=%#v ok=%v", got, ok)
	}
	if _, ok := findAppByInput(apps, "missing"); ok {
		t.Fatal("expected no match for missing app")
	}
}

func TestCommandName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "default when blank", in: "", want: defaultCommandName},
		{name: "keeps bare name", in: "sbd", want: "sbd"},
		{name: "uses basename", in: "/usr/local/bin/sbd", want: "sbd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandName(tc.in); got != tc.want {
				t.Fatalf("commandName(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewParserUsesInvocationName(t *testing.T) {
	cli := CLI{}
	parser, err := newParser(&cli, "/usr/local/bin/sbd")
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	if parser.Model.Name != "sbd" {
		t.Fatalf("parser.Model.Name=%q want %q", parser.Model.Name, "sbd")
	}
}

func TestCommandErrorUsesInvocationName(t *testing.T) {
	stderr, err := captureStderr(t, func() error {
		cliOutput{opts: outputOptions{CommandName: "sbd"}}.commandError("", errors.New("boom"))
		return nil
	})
	if err != nil {
		t.Fatalf("captureStderr returned error: %v", err)
	}
	if !strings.Contains(stderr, "[ERR] sbd failed") {
		t.Fatalf("stderr=%q does not include alias command name", stderr)
	}
	if !strings.Contains(stderr, "detail: boom") {
		t.Fatalf("stderr=%q does not include error detail", stderr)
	}
}

func TestServiceLogCommandParses(t *testing.T) {
	cli := CLI{}
	parser, err := newParser(&cli, defaultCommandName)
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	if _, err := parser.Parse([]string{"service", "log", "--stream", "stderr", "--no-follow"}); err != nil {
		t.Fatalf("expected service log to parse, got %v", err)
	}
}

func TestServiceLogCommandRejectsInvalidStream(t *testing.T) {
	cli := CLI{}
	parser, err := newParser(&cli, defaultCommandName)
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	if _, err := parser.Parse([]string{"service", "log", "--stream", "nope"}); err == nil {
		t.Fatal("expected invalid stream parse error")
	}
}

func TestServiceLogCommandSupportsJSONMode(t *testing.T) {
	orig := serviceLogRun
	defer func() { serviceLogRun = orig }()

	serviceLogRun = func(opts app.ServiceLogOptions) error {
		if !opts.JSON {
			t.Fatal("expected JSON mode to be passed to service log")
		}
		if opts.Follow {
			t.Fatal("expected --no-follow to disable follow mode")
		}
		if opts.Stream != "all" {
			t.Fatalf("expected default stream all, got %q", opts.Stream)
		}
		if opts.Stdout != opts.Stderr {
			t.Fatal("expected JSON service log output to share stdout writer")
		}
		_, err := io.WriteString(opts.Stdout, "{\"stream\":\"stdout\",\"line\":\"ready\"}\n")
		return err
	}

	stdout, _, err := runCLIForTest(t, nil, []string{"--json", "service", "log", "--no-follow"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	if got, want := stdout, "{\"stream\":\"stdout\",\"line\":\"ready\"}\n"; got != want {
		t.Fatalf("stdout=%q want %q", got, want)
	}
}

func TestServiceLogCommandSupportsOutputJSONMode(t *testing.T) {
	orig := serviceLogRun
	defer func() { serviceLogRun = orig }()

	serviceLogRun = func(opts app.ServiceLogOptions) error {
		if !opts.JSON {
			t.Fatal("expected --output json to enable JSON mode")
		}
		if opts.Stream != "stderr" {
			t.Fatalf("expected stream stderr, got %q", opts.Stream)
		}
		_, err := io.WriteString(opts.Stdout, "{\"stream\":\"stderr\",\"line\":\"warn\"}\n")
		return err
	}

	stdout, _, err := runCLIForTest(t, nil, []string{"--output", "json", "service", "log", "--stream", "stderr", "--no-follow"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	if got, want := stdout, "{\"stream\":\"stderr\",\"line\":\"warn\"}\n"; got != want {
		t.Fatalf("stdout=%q want %q", got, want)
	}
}

func TestServiceLogCommandJSONErrorWritesJSONToStderr(t *testing.T) {
	orig := serviceLogRun
	defer func() { serviceLogRun = orig }()

	serviceLogRun = func(app.ServiceLogOptions) error {
		return errors.New("boom")
	}

	cli := CLI{}
	parser, err := newParser(&cli, defaultCommandName)
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	ctx, err := parser.Parse([]string{"--json", "service", "log", "--no-follow"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	stderr, err := captureStderr(t, func() error {
		rc := &runContext{
			parser: parser,
			out: cliOutput{opts: outputOptions{
				CommandName: defaultCommandName,
				Quiet:       cli.Quiet,
				JSON:        cli.useJSON(),
				UI:          cli.uiMode(),
			}},
		}
		runErr := ctx.Run(rc)
		if runErr != nil {
			rc.out.commandError(ctx.Command(), runErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("captureStderr returned error: %v", err)
	}
	if !strings.Contains(stderr, "\"ok\": false") {
		t.Fatalf("stderr=%q missing ok=false", stderr)
	}
	if !strings.Contains(stderr, "\"command\": \"service log\"") {
		t.Fatalf("stderr=%q missing command", stderr)
	}
	if !strings.Contains(stderr, "\"error\": \"boom\"") {
		t.Fatalf("stderr=%q missing error", stderr)
	}
}

func TestStatusCommandSupportsJSONMode(t *testing.T) {
	orig := statusReportInfo
	defer func() { statusReportInfo = orig }()

	statusReportInfo = func() (app.StatusReport, error) {
		return app.StatusReport{
			ConfigPath: "/tmp/config.yaml",
			TLD:        "test",
			DNSIP:      "10.0.0.1",
			CaddyAdmin: "http://127.0.0.1:2019",
			TLS: app.StatusTLSReport{
				Enabled: true,
				Mode:    "internal",
				Valid:   true,
			},
			DNS:   app.StatusCheckReport{Status: "ok", Message: "ok (dig returned: 10.0.0.1)"},
			Caddy: app.StatusCheckReport{Status: "ok", Message: "admin reachable (background service)"},
			Apps: []app.StatusAppReport{
				{Name: "web", Host: "web.test", Port: 3000},
			},
			TunnelHealth: []app.StatusTunnelHealthItem{
				{AppName: "web", Provider: "cloudflare", EndpointHost: "web.example.com", Status: "ready", Message: "active"},
			},
		}, nil
	}

	stdout, _, err := runCLIForTest(t, nil, []string{"--json", "status"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	for _, want := range []string{
		"\"config_path\": \"/tmp/config.yaml\"",
		"\"tld\": \"test\"",
		"\"dns_ip\": \"10.0.0.1\"",
		"\"app_name\": \"web\"",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout=%q missing %q", stdout, want)
		}
	}
}

func TestTunnelInitCommandJSONIncludesEnvSummary(t *testing.T) {
	orig := prepareServiceEnvRun
	defer func() { prepareServiceEnvRun = orig }()

	prepareServiceEnvRun = func() (app.ServiceEnvironmentReport, error) {
		return app.ServiceEnvironmentReport{
			ConfigPath:          "/tmp/config.yaml",
			EnvFilePath:         "/tmp/service.env",
			RequiredEnvVars:     []string{"SWITCHD_CF_API_TOKEN"},
			MissingEnvVars:      []string{"SWITCHD_CF_API_TOKEN"},
			EnvFileCreated:      true,
			EnvFileUpdated:      true,
			EnvFileTemplateVars: []string{"SWITCHD_CF_API_TOKEN"},
		}, nil
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := switchboard.New(switchboard.Options{
		ConfigPath: cfgPath,
		ProviderRegistry: testCLIRegistry{
			provider: newTestCLIProvider(),
		},
		ApplyFunc: func(string, switchboard.Config) error { return nil },
	})

	stdout, _, err := runCLIForTest(t, client, []string{
		"--output", "json",
		"tunnel", "init",
		"--provider", "cloudflare",
		"--mode", "api",
		"--account-id", "acct-1",
		"--zone-id", "zone-1",
		"--base-domain", "tnl.example.com",
	})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	for _, want := range []string{
		"\"provider\": \"cloudflare\"",
		"\"mode\": \"api\"",
		"\"base_domain\": \"tnl.example.com\"",
		"\"api_token_env\": \"SWITCHD_CF_API_TOKEN\"",
		"\"env_file\": \"/tmp/service.env\"",
		"\"missing_env\": \"SWITCHD_CF_API_TOKEN\"",
		"\"env_file_created\": true",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout=%q missing %q", stdout, want)
		}
	}
}

func TestLsCommandSupportsJSONMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Routes = []config.Route{
		{Host: "api.test", Dial: "127.0.0.1:4000"},
		{Host: "web.test", Dial: "127.0.0.1:3000"},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	stdout, _, err := runCLIForTest(t, nil, []string{"--json", "ls"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	for _, want := range []string{
		"\"count\": 2",
		"\"host\": \"api.test\"",
		"\"dial\": \"127.0.0.1:3000\"",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout=%q missing %q", stdout, want)
		}
	}
}

func TestLsCommandTextOutputIsSorted(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default("test", "10.0.0.1")
	cfg.Routes = []config.Route{
		{Host: "web.test", Dial: "127.0.0.1:3000"},
		{Host: "api.test", Dial: "127.0.0.1:4000"},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save returned error: %v", err)
	}
	t.Setenv("SWITCHD_CONFIG_PATH", cfgPath)

	stdout, _, err := runCLIForTest(t, nil, []string{"ls"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	apiIndex := strings.Index(stdout, "api.test")
	webIndex := strings.Index(stdout, "web.test")
	if apiIndex < 0 || webIndex < 0 {
		t.Fatalf("stdout=%q missing routes", stdout)
	}
	if apiIndex > webIndex {
		t.Fatalf("expected sorted output, got %q", stdout)
	}
}

func TestStackEnvCommand(t *testing.T) {
	client, stackPath := setupCLIStackFixture(t)
	stdout, _, err := runCLIForTest(t, client, []string{"stack", "env", "-f", stackPath})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	want := "APP_HTTP__BASE_URL=https://app.carina.getctx.com\n"
	if stdout != want {
		t.Fatalf("stdout=%q want %q", stdout, want)
	}
}

func TestStackPlanCommandJSON(t *testing.T) {
	client, stackPath := setupCLIStackFixture(t)
	stdout, _, err := runCLIForTest(t, client, []string{"--output", "json", "stack", "plan", "-f", stackPath})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	if !bytes.Contains([]byte(stdout), []byte(`"command": "plan"`)) {
		t.Fatalf("expected json output to contain command, got %s", stdout)
	}
	if !bytes.Contains([]byte(stdout), []byte(`"type": "create_app"`)) {
		t.Fatalf("expected json output to contain create_app action, got %s", stdout)
	}
}

func TestBuildStackReportViewModel(t *testing.T) {
	report := switchboard.StackReport{
		StackName:  "carina",
		StackFile:  "/tmp/stack.yaml",
		HasChanges: true,
		HasUnsafe:  true,
		Services: []switchboard.StackServiceStatus{
			{
				Name:              "app",
				GeneratedAppName:  "carina-app",
				LocalHost:         "app.test",
				LocalPort:         8383,
				DesiredPublicHost: "app.example.com",
				Provider:          "mock",
				SessionActive:     true,
				Managed:           true,
				Drift:             []string{"public_host"},
				Actions: []switchboard.StackAction{
					{Type: "create_app"},
					{Type: "expose"},
				},
			},
			{
				Name:             "worker",
				GeneratedAppName: "carina-worker",
				LocalHost:        "worker.test",
				LocalPort:        9393,
				Collision:        "managed app already exists",
			},
		},
		Orphans: []switchboard.StackManagedOrphan{
			{AppName: "old-app", Service: "old", LocalHost: "old.test", PublicHost: "old.example.com"},
		},
	}

	model := buildStackReportViewModel("plan", report)
	if model.Command != "plan" || model.StackName != "carina" || model.StackFile != "/tmp/stack.yaml" {
		t.Fatalf("unexpected model header %#v", model)
	}
	if len(model.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %#v", model)
	}
	if model.Rows[0].Name != "app" {
		t.Fatalf("expected sorted app first, got %#v", model.Rows)
	}
	if model.Rows[0].Session != "active" {
		t.Fatalf("expected active session, got %#v", model.Rows[0])
	}
	if model.Rows[1].Session != "collision" {
		t.Fatalf("expected collision session, got %#v", model.Rows[1])
	}
	if len(model.Collisions) != 1 || !strings.Contains(model.Collisions[0], "managed app already exists") {
		t.Fatalf("unexpected collisions %#v", model.Collisions)
	}
	if len(model.Orphans) != 1 || !strings.Contains(model.Orphans[0], "old-app") {
		t.Fatalf("unexpected orphans %#v", model.Orphans)
	}
}

func TestRenderStackReportPlainIncludesCollisionsAndOrphans(t *testing.T) {
	rc := &runContext{out: cliOutput{}}
	model := stackReportViewModel{
		Command:   "plan",
		StackName: "carina",
		StackFile: "/tmp/stack.yaml",
		Rows: []stackServiceRow{
			{
				Name:       "app",
				AppName:    "carina-app",
				LocalHost:  "app.test",
				Port:       8383,
				PublicHost: "app.example.com",
				Provider:   "mock",
				Session:    "active",
				Actions:    []string{"create_app"},
			},
		},
		Collisions: []string{"worker: managed app already exists"},
		Orphans:    []string{"app=old-app service=old local_host=old.test public_host=old.example.com"},
	}

	stdout, err := captureStdout(t, func() error {
		return rc.renderStackReportPlain(model)
	})
	if err != nil {
		t.Fatalf("captureStdout returned error: %v", err)
	}
	for _, want := range []string{
		"stack: carina",
		"collision worker: managed app already exists",
		"orphan: app=old-app service=old local_host=old.test public_host=old.example.com",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout=%q missing %q", stdout, want)
		}
	}
}

func TestStackPlanCommandRoutesToTUI(t *testing.T) {
	orig := stackReportTUIRun
	defer func() { stackReportTUIRun = orig }()

	called := false
	stackReportTUIRun = func(model stackReportViewModel, styles cliStyles) error {
		called = true
		if model.Command != "plan" {
			t.Fatalf("expected plan command, got %#v", model)
		}
		return nil
	}

	client, stackPath := setupCLIStackFixture(t)
	cli := CLI{}
	parser, err := newParser(&cli, defaultCommandName)
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	ctx, err := parser.Parse([]string{"--ui", "tui", "stack", "plan", "-f", stackPath})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if err := ctx.Run(&runContext{
		parser: parser,
		out: cliOutput{opts: outputOptions{
			CommandName: defaultCommandName,
			UI:          uiModeTUI,
			Interactive: true,
		}},
		client: client,
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("expected stack TUI runner to be called")
	}
}

func TestAppLsShowsTunnelHealth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := switchboard.New(switchboard.Options{
		ConfigPath: cfgPath,
		ProviderRegistry: testCLIRegistry{
			provider: newTestCLIProvider(),
		},
		ApplyFunc: func(string, switchboard.Config) error { return nil },
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
	if err := client.CreateApp("active-app", 3000, nil); err != nil {
		t.Fatalf("CreateApp active returned error: %v", err)
	}
	if err := client.ExposeApp("active-app", "mock", "active.example.com"); err != nil {
		t.Fatalf("ExposeApp active returned error: %v", err)
	}
	if err := client.AppUp("active-app"); err != nil {
		t.Fatalf("AppUp active returned error: %v", err)
	}
	if err := client.CreateApp("idle-app", 3001, nil); err != nil {
		t.Fatalf("CreateApp idle returned error: %v", err)
	}
	if err := client.ExposeApp("idle-app", "mock", "idle.example.com"); err != nil {
		t.Fatalf("ExposeApp idle returned error: %v", err)
	}

	stdout, _, err := runCLIForTest(t, client, []string{"app", "ls"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	if !strings.Contains(stdout, "active OK") {
		t.Fatalf("stdout=%q missing active OK", stdout)
	}
	if !strings.Contains(stdout, "idle KO") {
		t.Fatalf("stdout=%q missing idle KO", stdout)
	}
}

type unavailableStatusProvider struct {
	*testCLIProvider
}

func (p *unavailableStatusProvider) Status(context.Context, string) (switchboard.EndpointStatus, error) {
	return switchboard.EndpointStatus{}, errors.New("provider status unavailable")
}

func TestAppLsShowsUnknownTunnelHealthWhenStatusUnavailable(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := switchboard.New(switchboard.Options{
		ConfigPath: cfgPath,
		ProviderRegistry: testCLIRegistry{
			provider: &unavailableStatusProvider{testCLIProvider: newTestCLIProvider()},
		},
		ApplyFunc: func(string, switchboard.Config) error { return nil },
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

	stdout, _, err := runCLIForTest(t, client, []string{"app", "ls"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	if !strings.Contains(stdout, "active ?") {
		t.Fatalf("stdout=%q missing active ?", stdout)
	}
}

func TestBuildAppListViewModel(t *testing.T) {
	apps := []switchboard.App{
		{
			Name:      "active-app",
			LocalHost: "active.test",
			LocalPort: 3000,
			PublicEndpoint: switchboard.AppPublicEndpoint{
				Provider:             "mock",
				Host:                 "active.example.com",
				EndpointID:           "ep-active",
				ActiveSessionID:      "sess-active",
				ActiveSessionPID:     1234,
				ActiveSessionStarted: "2026-03-29T23:00:00Z",
			},
		},
		{
			Name:      "idle-app",
			LocalHost: "idle.test",
			LocalPort: 3001,
			PublicEndpoint: switchboard.AppPublicEndpoint{
				Provider:   "mock",
				Host:       "idle.example.com",
				EndpointID: "ep-idle",
			},
			OAuth: switchboard.AppOAuth{
				Google: switchboard.AppGoogleOAuth{Enabled: true},
			},
		},
	}
	health := []switchboard.AppTunnelHealth{
		{
			AppName:      "active-app",
			Provider:     "mock",
			EndpointHost: "active.example.com",
			SessionID:    "sess-active",
			SessionPID:   1234,
			StartedAt:    "2026-03-29T23:00:00Z",
			Ready:        true,
			Message:      "active",
		},
		{
			AppName:      "idle-app",
			Provider:     "mock",
			EndpointHost: "idle.example.com",
			Ready:        false,
			Message:      "inactive",
		},
	}

	model := buildAppListViewModel(apps, health, nil)
	if len(model.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(model.Rows))
	}
	if model.Rows[0].Name != "active-app" {
		t.Fatalf("expected sorted active-app first, got %#v", model.Rows)
	}
	if model.Rows[0].TunnelLabel != "active OK" || model.Rows[0].TunnelHealth != "ok" {
		t.Fatalf("unexpected active row %#v", model.Rows[0])
	}
	if model.Rows[1].TunnelLabel != "idle KO" || model.Rows[1].TunnelHealth != "warning" {
		t.Fatalf("unexpected idle row %#v", model.Rows[1])
	}
	if model.Rows[1].OAuth != "google" {
		t.Fatalf("expected oauth google, got %#v", model.Rows[1])
	}

	unknown := buildAppListViewModel(apps[:1], nil, errors.New("provider status unavailable"))
	if unknown.HealthError != "provider status unavailable" {
		t.Fatalf("unexpected health error %#v", unknown)
	}
	if unknown.Rows[0].TunnelLabel != "active ?" || unknown.Rows[0].TunnelHealth != "unknown" {
		t.Fatalf("unexpected unknown row %#v", unknown.Rows[0])
	}
}

func TestAppLsCommandRoutesToTUI(t *testing.T) {
	orig := appListTUIRun
	defer func() { appListTUIRun = orig }()

	called := false
	appListTUIRun = func(model appListViewModel, styles cliStyles) error {
		called = true
		if len(model.Rows) != 1 {
			t.Fatalf("expected 1 row, got %#v", model)
		}
		return nil
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := switchboard.New(switchboard.Options{
		ConfigPath: cfgPath,
		ProviderRegistry: testCLIRegistry{
			provider: newTestCLIProvider(),
		},
		ApplyFunc: func(string, switchboard.Config) error { return nil },
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

	cli := CLI{}
	parser, err := newParser(&cli, defaultCommandName)
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	ctx, err := parser.Parse([]string{"--ui", "tui", "app", "ls"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if err := ctx.Run(&runContext{
		parser: parser,
		out: cliOutput{opts: outputOptions{
			CommandName: defaultCommandName,
			UI:          uiModeTUI,
			Interactive: true,
		}},
		client: client,
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("expected TUI runner to be called")
	}
}

func TestAppLsCommandJSONBypassesTUI(t *testing.T) {
	orig := appListTUIRun
	defer func() { appListTUIRun = orig }()

	appListTUIRun = func(model appListViewModel, styles cliStyles) error {
		t.Fatal("did not expect app list TUI in JSON mode")
		return nil
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := switchboard.New(switchboard.Options{ConfigPath: cfgPath})
	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatalf("LoadOrCreateDefaultConfig returned error: %v", err)
	}
	cfg.Caddy.TLS.Enabled = false
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}
	if err := client.CreateApp("demo", 3000, nil); err != nil {
		t.Fatalf("CreateApp returned error: %v", err)
	}

	stdout, _, err := runCLIForTest(t, client, []string{"--json", "--ui", "tui", "app", "ls"})
	if err != nil {
		t.Fatalf("runCLIForTest returned error: %v", err)
	}
	if !strings.Contains(stdout, "\"apps\"") {
		t.Fatalf("expected json apps payload, got %q", stdout)
	}
}

func TestAppLsCommandTUIRequiresInteractive(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	client := switchboard.New(switchboard.Options{ConfigPath: cfgPath})
	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatalf("LoadOrCreateDefaultConfig returned error: %v", err)
	}
	cfg.Caddy.TLS.Enabled = false
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}

	_, _, err = runCLIForTest(t, client, []string{"--ui", "tui", "app", "ls"})
	if err == nil || !strings.Contains(err.Error(), "--ui=tui requires an interactive terminal") {
		t.Fatalf("err=%v want interactive terminal error", err)
	}
}

func setupCLIStackFixture(t *testing.T) (*switchboard.Client, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	stackPath := filepath.Join(dir, "stack.yaml")

	client := switchboard.New(switchboard.Options{ConfigPath: cfgPath})
	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatalf("LoadOrCreateDefaultConfig returned error: %v", err)
	}
	cfg.TLD = "test"
	cfg.Caddy.TLS.Enabled = false
	if err := client.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}

	stackYAML := `
version: 1
name: carina
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
	return client, stackPath
}

func runCLIForTest(t *testing.T, client *switchboard.Client, args []string) (string, string, error) {
	t.Helper()
	cli := CLI{}
	parser, err := newParser(&cli, defaultCommandName)
	if err != nil {
		t.Fatalf("newParser returned error: %v", err)
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		return "", "", err
	}

	stdout, runErr := captureStdout(t, func() error {
		return ctx.Run(&runContext{
			parser: parser,
			out: cliOutput{opts: outputOptions{
				CommandName: defaultCommandName,
				Quiet:       cli.Quiet,
				JSON:        cli.useJSON(),
				UI:          cli.uiMode(),
			}},
			client: client,
		})
	})
	return stdout, "", runErr
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned error: %v", err)
	}
	defer r.Close()
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	b, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll returned error: %v", readErr)
	}
	return string(b), runErr
}

func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned error: %v", err)
	}
	defer r.Close()
	os.Stderr = w
	runErr := fn()
	_ = w.Close()
	os.Stderr = orig
	b, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll returned error: %v", readErr)
	}
	return string(b), runErr
}
