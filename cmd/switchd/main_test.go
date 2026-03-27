package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/pkg/switchboard"
)

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
