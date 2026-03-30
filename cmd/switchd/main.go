package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
	"github.com/goliatone/switchboard-hub/pkg/switchboard"
)

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
	buildTags = ""
	gitTag    = ""

	serviceLogRun        = app.ServiceLog
	statusReportInfo     = app.StatusReportInfo
	prepareServiceEnvRun = app.PrepareServiceEnvironment
	appListTUIRun        = runAppListTUI
	stackReportTUIRun    = runStackReportTUI
)

const defaultCommandName = "switchd"

type globalFlags struct {
	Debug   bool   `name:"debug" help:"Enable debug logs."`
	Verbose bool   `name:"verbose" short:"v" help:"Alias for --debug."`
	Quiet   bool   `name:"quiet" short:"q" help:"Suppress non-essential success/info output."`
	JSON    bool   `name:"json" help:"Print machine-readable JSON output when supported."`
	Output  string `name:"output" enum:"text,json" default:"text" help:"Output mode."`
	UI      string `name:"ui" enum:"auto,plain,tui" default:"auto" help:"Human UI mode."`
}

func (g globalFlags) useJSON() bool {
	if g.JSON {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(g.Output), "json")
}

func (g globalFlags) uiMode() string {
	mode := strings.ToLower(strings.TrimSpace(g.UI))
	if mode == "" {
		return "auto"
	}
	return mode
}

type CLI struct {
	globalFlags

	Init      InitCmd      `cmd:"" help:"Initialize switchd local DNS + Caddy setup."`
	App       AppCmd       `cmd:"" help:"Manage app definitions and tunnel exposure."`
	Stack     StackCmd     `cmd:"" help:"Manage declarative stacks."`
	Tunnel    TunnelCmd    `cmd:"" help:"Manage tunnel providers."`
	Add       AddCmd       `cmd:"" help:"Add a route."`
	Rm        RmCmd        `cmd:"" help:"Remove a route."`
	Ls        LsCmd        `cmd:"" help:"List routes."`
	Apply     ApplyCmd     `cmd:"" help:"Apply config to Caddy admin API."`
	TLS       TLSCmd       `cmd:"" name:"tls" help:"TLS helpers."`
	Open      OpenCmd      `cmd:"" help:"Open app URL in browser."`
	Uninstall UninstallCmd `cmd:"" help:"Uninstall switchd local setup."`
	Status    StatusCmd    `cmd:"" help:"Show status diagnostics."`
	Service   ServiceCmd   `cmd:"" help:"Manage the macOS background service."`
	Version   VersionCmd   `cmd:"" help:"Show build/version info."`
	Caddy     CaddyCmd     `cmd:"" help:"Caddy control commands."`
	Daemon    DaemonCmd    `cmd:"" help:"Internal daemon commands."`
	Help      HelpCmd      `cmd:"" help:"Show help for a command path."`
}

type InitCmd struct {
	TLD         string `name:"tld" default:"test" help:"Development TLD to manage."`
	DNSIP       string `name:"dns-ip" default:"10.0.0.1" help:"Loopback alias IP for dnsmasq."`
	TLS         bool   `name:"tls" default:"true" help:"Enable HTTPS listeners in Caddy."`
	TLSMode     string `name:"tls-mode" default:"internal" enum:"internal,file" help:"TLS mode."`
	TLSCertFile string `name:"tls-cert-file" help:"Certificate file path (PEM)."`
	TLSKeyFile  string `name:"tls-key-file" help:"Key file path (PEM)."`
}

func (c *InitCmd) Run(r *runContext) error {
	if err := app.Init(c.TLD, c.DNSIP, c.TLS, c.TLSMode, c.TLSCertFile, c.TLSKeyFile); err != nil {
		return err
	}
	r.out.ok("Initialization complete", map[string]any{
		"tld":    c.TLD,
		"dns_ip": c.DNSIP,
		"tls":    c.TLS,
	})
	return nil
}

type AppCmd struct {
	Create AppCreateCmd `cmd:"" help:"Create an app."`
	Rm     AppRmCmd     `cmd:"" name:"rm" help:"Remove an app."`
	Ls     AppLsCmd     `cmd:"" name:"ls" help:"List apps."`
	Expose AppExposeCmd `cmd:"" help:"Expose app through tunnel provider."`
	Up     AppUpCmd     `cmd:"" help:"Start app runtime and tunnel connector."`
	Down   AppDownCmd   `cmd:"" help:"Stop app tunnel connector session."`
	OAuth  AppOAuthCmd  `cmd:"" name:"oauth" help:"OAuth helpers."`
}

type AppCreateCmd struct {
	NameOrHost string `arg:"" name:"name-or-host" help:"App name or host."`
	Port       int    `name:"port" required:"" help:"Local port to proxy to."`
	DialHost   string `name:"dial-host" help:"Explicit upstream host for the app port, for example 127.0.0.1 or ::1. Defaults to auto detection."`
}

func (c *AppCreateCmd) Run(r *runContext) error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("--port must be 1..65535")
	}
	var opts *switchboard.CreateAppOptions
	if strings.TrimSpace(c.DialHost) != "" {
		opts = &switchboard.CreateAppOptions{DialHost: c.DialHost}
	}
	if err := r.client.CreateApp(c.NameOrHost, c.Port, opts); err != nil {
		return err
	}
	apps, err := r.client.ListApps()
	if err == nil {
		if created, ok := findSwitchboardAppByInput(apps, c.NameOrHost); ok {
			fields := map[string]any{
				"app":       created.Name,
				"local":     created.LocalHost,
				"port":      created.LocalPort,
				"dial_host": "auto",
			}
			if strings.TrimSpace(created.DialHost) != "" {
				fields["dial_host"] = created.DialHost
			}
			r.out.ok("App created", fields)
			return nil
		}
	}
	fallbackDialHost := "auto"
	if strings.TrimSpace(c.DialHost) != "" {
		fallbackDialHost = strings.TrimSpace(c.DialHost)
	}
	r.out.ok("App created", map[string]any{"app": c.NameOrHost, "port": c.Port, "dial_host": fallbackDialHost})
	return nil
}

type AppRmCmd struct {
	Name string `arg:"" name:"name" help:"App name."`
}

func (c *AppRmCmd) Run(r *runContext) error {
	if err := r.client.RemoveApp(c.Name); err != nil {
		return err
	}
	r.out.ok("App removed", map[string]any{"app": c.Name})
	return nil
}

type AppLsCmd struct{}

func (c *AppLsCmd) Run(r *runContext) error {
	apps, err := r.client.ListApps()
	if err != nil {
		return err
	}
	health, healthErr := r.client.AppTunnelHealthStatus()
	if r.out.opts.JSON {
		payload := map[string]any{"apps": apps, "count": len(apps)}
		if healthErr == nil {
			payload["health"] = health
		} else {
			payload["health_error"] = strings.TrimSpace(healthErr.Error())
		}
		r.out.jsonOut(os.Stdout, payload)
		return nil
	}
	model := buildAppListViewModel(apps, health, healthErr)
	if useTUI, err := r.wantsTUIForAppList(); err != nil {
		return err
	} else if useTUI {
		return appListTUIRun(model, r.out.styles())
	}
	if len(model.Rows) == 0 {
		r.out.info("No apps configured", nil)
		return nil
	}
	r.out.printTable([]string{"NAME", "LOCAL_HOST", "PORT", "PUBLIC_HOST", "OAUTH", "TUNNEL"}, buildAppListTableRows(model))
	return nil
}

type AppExposeCmd struct {
	Name       string `arg:"" name:"name" help:"App name."`
	Provider   string `name:"provider" help:"Tunnel provider (defaults to config tunnels.default_provider)."`
	PublicHost string `name:"public-host" help:"Public callback host."`
}

func (c *AppExposeCmd) Run(r *runContext) error {
	if err := r.client.ExposeApp(c.Name, c.Provider, c.PublicHost); err != nil {
		return err
	}
	apps, err := r.client.ListApps()
	if err == nil {
		if exposed, ok := findSwitchboardAppByInput(apps, c.Name); ok {
			r.out.ok("Public endpoint configured", map[string]any{
				"app":         exposed.Name,
				"provider":    exposed.PublicEndpoint.Provider,
				"public_host": exposed.PublicEndpoint.Host,
				"endpoint_id": exposed.PublicEndpoint.EndpointID,
			})
			return nil
		}
	}
	r.out.ok("Public endpoint configured", map[string]any{"app": c.Name})
	return nil
}

type AppUpCmd struct {
	Name string `arg:"" name:"name" help:"App name."`
}

func (c *AppUpCmd) Run(r *runContext) error {
	if err := r.client.AppUp(c.Name); err != nil {
		return err
	}
	apps, err := r.client.ListApps()
	if err == nil {
		if runtime, ok := findSwitchboardAppByInput(apps, c.Name); ok {
			fields := map[string]any{
				"app":         runtime.Name,
				"local_url":   fmt.Sprintf("http://%s", runtime.LocalHost),
				"public_host": runtime.PublicEndpoint.Host,
			}
			if strings.TrimSpace(runtime.PublicEndpoint.ActiveSessionID) != "" {
				fields["session_id"] = runtime.PublicEndpoint.ActiveSessionID
			}
			r.out.ok("App runtime started", fields)
			return nil
		}
	}
	r.out.ok("App runtime started", map[string]any{"app": c.Name})
	return nil
}

type AppDownCmd struct {
	Name string `arg:"" name:"name" help:"App name."`
}

func (c *AppDownCmd) Run(r *runContext) error {
	beforeApps, _ := r.client.ListApps()
	before, _ := findSwitchboardAppByInput(beforeApps, c.Name)
	if err := r.client.AppDown(c.Name); err != nil {
		return err
	}
	afterApps, err := r.client.ListApps()
	if err == nil {
		if after, ok := findSwitchboardAppByInput(afterApps, c.Name); ok {
			if strings.TrimSpace(before.PublicEndpoint.ActiveSessionID) == "" {
				r.out.warn("App tunnel already stopped", map[string]any{"app": after.Name})
				return nil
			}
			r.out.ok("App tunnel stopped", map[string]any{"app": after.Name, "public_host": after.PublicEndpoint.Host})
			return nil
		}
	}
	r.out.ok("App tunnel stopped", map[string]any{"app": c.Name})
	return nil
}

type AppOAuthCmd struct {
	Enable AppOAuthEnableCmd `cmd:"" name:"enable" help:"Enable OAuth for an app."`
	Print  AppOAuthPrintCmd  `cmd:"" name:"print" help:"Print OAuth redirect URI details for an app."`
}

type AppOAuthEnableCmd struct {
	Name         string `arg:"" name:"name" help:"App name."`
	Provider     string `name:"provider" required:"" enum:"google" help:"OAuth provider name."`
	CallbackPath string `name:"callback-path" required:"" help:"OAuth callback path (must start with /)."`
}

func (c *AppOAuthEnableCmd) Run(r *runContext) error {
	switch c.Provider {
	case "google":
		if err := app.OAuthGoogleEnable(c.Name, c.CallbackPath); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported oauth provider %q", c.Provider)
	}

	apps, err := app.ListApps()
	if err == nil {
		if current, ok := findAppByInput(apps, c.Name); ok {
			r.out.ok("OAuth enabled", map[string]any{
				"app":           current.Name,
				"provider":      c.Provider,
				"callback_path": current.OAuth.Google.CallbackPath,
				"redirect_uri":  current.OAuth.Google.RedirectURI,
			})
			return nil
		}
	}
	r.out.ok("OAuth enabled", map[string]any{
		"app":           c.Name,
		"provider":      c.Provider,
		"callback_path": c.CallbackPath,
	})
	return nil
}

type AppOAuthPrintCmd struct {
	Name     string `arg:"" name:"name" help:"App name."`
	Provider string `name:"provider" required:"" enum:"google" help:"OAuth provider name."`
}

func (c *AppOAuthPrintCmd) Run(r *runContext) error {
	var (
		block string
		err   error
	)
	switch c.Provider {
	case "google":
		block, err = app.OAuthGooglePrint(c.Name)
	default:
		return fmt.Errorf("unsupported oauth provider %q", c.Provider)
	}
	if err != nil {
		return err
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, map[string]any{
			"app":      c.Name,
			"provider": c.Provider,
			"output":   block,
		})
		return nil
	}
	fmt.Println(block)
	return nil
}

type TunnelCmd struct {
	Providers TunnelProvidersCmd `cmd:"" name:"providers" help:"List available tunnel providers."`
	Init      TunnelInitCmd      `cmd:"" name:"init" help:"Initialize tunnel provider."`
	Status    TunnelStatusCmd    `cmd:"" name:"status" help:"Show tunnel provider health/status."`
}

type StackCmd struct {
	Plan   StackPlanCmd   `cmd:"" help:"Show stack reconciliation plan."`
	Up     StackUpCmd     `cmd:"" help:"Reconcile stack and start managed runtime sessions."`
	Down   StackDownCmd   `cmd:"" help:"Stop runtime sessions for stack services."`
	Status StackStatusCmd `cmd:"" help:"Show stack desired vs actual status."`
	Env    StackEnvCmd    `cmd:"" help:"Render stack outputs as KEY=value lines."`
}

type StackPlanCmd struct {
	File string `name:"file" short:"f" required:"" help:"Stack file path."`
}

func (c *StackPlanCmd) Run(r *runContext) error {
	report, err := r.client.StackPlan(c.File)
	if err != nil {
		return err
	}
	return r.renderStackReport("plan", report)
}

type StackUpCmd struct {
	File string `name:"file" short:"f" required:"" help:"Stack file path."`
}

func (c *StackUpCmd) Run(r *runContext) error {
	report, err := r.client.StackUp(c.File)
	if err != nil {
		if report.StackName != "" {
			_ = r.renderStackReport("up", report)
		}
		return err
	}
	return r.renderStackReport("up", report)
}

type StackDownCmd struct {
	File string `name:"file" short:"f" required:"" help:"Stack file path."`
}

func (c *StackDownCmd) Run(r *runContext) error {
	report, err := r.client.StackDown(c.File)
	if err != nil {
		if report.StackName != "" {
			_ = r.renderStackReport("down", report)
		}
		return err
	}
	return r.renderStackReport("down", report)
}

type StackStatusCmd struct {
	File string `name:"file" short:"f" required:"" help:"Stack file path."`
}

func (c *StackStatusCmd) Run(r *runContext) error {
	report, err := r.client.StackStatus(c.File)
	if err != nil {
		return err
	}
	return r.renderStackReport("status", report)
}

type StackEnvCmd struct {
	File string `name:"file" short:"f" required:"" help:"Stack file path."`
}

func (c *StackEnvCmd) Run(r *runContext) error {
	lines, err := r.client.RenderStackEnv(c.File)
	if err != nil {
		return err
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, map[string]any{"env": lines, "count": len(lines)})
		return nil
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

type TunnelProvidersCmd struct{}

func (c *TunnelProvidersCmd) Run(r *runContext) error {
	providers := r.client.TunnelProviders()
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, map[string]any{"providers": providers, "count": len(providers)})
		return nil
	}
	rows := make([][]string, 0, len(providers))
	for _, p := range providers {
		rows = append(rows, []string{p})
	}
	r.out.printTable([]string{"PROVIDER"}, rows)
	return nil
}

type TunnelInitCmd struct {
	Provider       string `name:"provider" required:"" help:"Provider name."`
	Mode           string `name:"mode" help:"Cloudflare mode (cli or api)."`
	Setup          bool   `name:"setup" help:"Attempt provider bootstrap if preflight fails."`
	NonInteractive bool   `name:"non-interactive" help:"Disable interactive setup flow."`
	OriginCert     string `name:"origincert" help:"Cloudflare origin cert path override."`
	AccountID      string `name:"account-id" help:"Cloudflare account id (for mode=api)."`
	ZoneID         string `name:"zone-id" help:"Cloudflare zone id (for mode=api)."`
	BaseDomain     string `name:"base-domain" help:"Default public tunnel base domain, e.g. tnl.example.com."`
	APITokenEnv    string `name:"api-token-env" help:"Env var holding Cloudflare API token."`
}

func (c *TunnelInitCmd) Run(r *runContext) error {
	if err := r.client.TunnelInit(c.Provider, switchboard.TunnelInitOptions{
		Setup:          c.Setup,
		NonInteractive: c.NonInteractive,
		OriginCert:     c.OriginCert,
		Mode:           c.Mode,
		AccountID:      c.AccountID,
		ZoneID:         c.ZoneID,
		BaseDomain:     c.BaseDomain,
		APITokenEnv:    c.APITokenEnv,
	}); err != nil {
		return err
	}
	fields := tunnelInitFields(r.client, c.Provider)
	if _, ok := fields["provider"]; !ok && strings.TrimSpace(c.Provider) != "" {
		fields["provider"] = strings.TrimSpace(c.Provider)
	}
	if report, err := maybeCollectMissingServiceEnv(r); err == nil {
		if strings.TrimSpace(report.EnvFilePath) != "" {
			fields["env_file"] = report.EnvFilePath
		}
		if len(report.RequiredEnvVars) > 0 {
			fields["required_env"] = strings.Join(report.RequiredEnvVars, ", ")
		}
		if len(report.MissingEnvVars) > 0 {
			fields["missing_env"] = strings.Join(report.MissingEnvVars, ", ")
		}
		if report.EnvFileCreated {
			fields["env_file_created"] = true
		}
		if report.EnvFileUpdated {
			fields["env_file_updated"] = true
		}
		r.out.ok("Tunnel provider initialized", fields)
		renderServiceEnvWarnings(r, report)
		return nil
	} else {
		r.out.ok("Tunnel provider initialized", fields)
		r.out.warn("Unable to prepare service env template", map[string]any{"detail": err})
		return nil
	}
}

type TunnelStatusCmd struct {
	Provider string `name:"provider" help:"Provider name (optional)."`
}

func (c *TunnelStatusCmd) Run(r *runContext) error {
	statuses, err := r.client.TunnelStatus(c.Provider)
	if err != nil {
		return err
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, map[string]any{"providers": statuses, "count": len(statuses)})
		return nil
	}
	rows := make([][]string, 0, len(statuses))
	for _, st := range statuses {
		status := "ok"
		detail := "ready"
		if !st.Available {
			status = "error"
			detail = st.Err
		}
		rows = append(rows, []string{st.Name, boolLabel(st.Enabled), boolLabel(st.OAuthSuitable), status, detail})
	}
	r.out.printTable([]string{"PROVIDER", "ENABLED", "OAUTH", "STATUS", "DETAIL"}, rows)
	for _, st := range statuses {
		for _, note := range st.Notes {
			note = strings.TrimSpace(note)
			if note == "" {
				continue
			}
			r.out.info("provider note", map[string]any{"provider": st.Name, "note": note})
		}
	}
	return nil
}

type AddCmd struct {
	NameOrHost string `arg:"" optional:"" name:"name-or-host" help:"Route name or host."`
	Port       int    `name:"port" required:"" help:"Local port to proxy to."`
	Host       string `name:"host" help:"Explicit host (mutually exclusive with positional arg)."`
	DialHost   string `name:"dial-host" help:"Explicit upstream host for the route port, for example 127.0.0.1 or ::1."`
}

func (c *AddCmd) Run(r *runContext) error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("--port must be 1..65535")
	}
	if strings.TrimSpace(c.Host) == "" && strings.TrimSpace(c.NameOrHost) == "" {
		return fmt.Errorf("provide <name-or-host> or --host")
	}
	if strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.NameOrHost) != "" {
		return fmt.Errorf("use either positional <name-or-host> or --host, not both")
	}
	nameOrHost := strings.TrimSpace(c.Host)
	if nameOrHost == "" {
		nameOrHost = strings.TrimSpace(c.NameOrHost)
	}
	if err := app.AddRoute(nameOrHost, c.Port, c.DialHost); err != nil {
		return err
	}
	fields := map[string]any{"host": nameOrHost, "port": c.Port}
	if strings.TrimSpace(c.DialHost) != "" {
		fields["dial_host"] = strings.TrimSpace(c.DialHost)
	}
	r.out.ok("Route added", fields)
	return nil
}

type RmCmd struct {
	NameOrHost string `arg:"" name:"name-or-host" help:"Route name or host."`
}

func (c *RmCmd) Run(r *runContext) error {
	target := strings.TrimSpace(c.NameOrHost)
	if target == "" {
		return fmt.Errorf("empty target")
	}
	if err := app.RemoveRoute(target); err != nil {
		return err
	}
	r.out.ok("Route removed", map[string]any{"host": target})
	return nil
}

type LsCmd struct{}

func (c *LsCmd) Run(r *runContext) error {
	if r.out.opts.JSON {
		routes, err := app.ListRoutesInfo()
		if err != nil {
			return err
		}
		r.out.jsonOut(os.Stdout, map[string]any{"routes": routes, "count": len(routes)})
		return nil
	}
	return app.ListRoutes()
}

type ApplyCmd struct{}

func (c *ApplyCmd) Run(r *runContext) error {
	if err := app.Apply(); err != nil {
		return err
	}
	r.out.ok("Configuration applied to Caddy", nil)
	return nil
}

type TLSCmd struct {
	Mkcert TLSMkcertCmd `cmd:"" name:"mkcert" help:"Generate local TLS cert/key with mkcert."`
}

type TLSMkcertCmd struct {
	Install  bool   `name:"install" help:"Run mkcert -install before generating certs."`
	CertFile string `name:"cert-file" help:"Output certificate file path."`
	KeyFile  string `name:"key-file" help:"Output key file path."`
}

func (c *TLSMkcertCmd) Run(_ *runContext) error {
	return app.TLSMkcert(c.CertFile, c.KeyFile, c.Install)
}

type OpenCmd struct {
	NameOrHost string `arg:"" name:"name-or-host" help:"Route name or host."`
	Scheme     string `name:"scheme" help:"URL scheme (http or https)."`
}

func (c *OpenCmd) Run(_ *runContext) error {
	return app.Open(c.NameOrHost, c.Scheme)
}

type UninstallCmd struct{}

func (c *UninstallCmd) Run(r *runContext) error {
	if err := app.Uninstall(); err != nil {
		return err
	}
	r.out.ok("Uninstall complete", nil)
	return nil
}

type StatusCmd struct{}

func (c *StatusCmd) Run(r *runContext) error {
	report, err := statusReportInfo()
	if err != nil {
		return err
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, report)
		return nil
	}
	if useTUI, err := r.wantsTUIForStatus(); err != nil {
		return err
	} else if useTUI {
		return runStatusTUI(statusReportInfo, r.out.styles())
	}
	renderStatusReport(report)
	return nil
}

type CaddyCmd struct {
	Run CaddyRunCmd `cmd:"" name:"run" help:"Run Caddy foreground process."`
}

type ServiceCmd struct {
	Install   ServiceInstallCmd   `cmd:"" help:"Install and start the background service."`
	Log       ServiceLogCmd       `cmd:"" help:"Show recent background service logs and follow live output."`
	Start     ServiceStartCmd     `cmd:"" help:"Start the installed background service."`
	Stop      ServiceStopCmd      `cmd:"" help:"Stop the background service."`
	Status    ServiceStatusCmd    `cmd:"" help:"Show background service status."`
	Uninstall ServiceUninstallCmd `cmd:"" help:"Stop and remove the background service."`
}

type DaemonCmd struct {
	Run DaemonRunCmd `cmd:"" name:"run" help:"Run the internal switchd background daemon."`
}

type VersionCmd struct{}

func (c *VersionCmd) Run(r *runContext) error {
	info := map[string]any{
		"version":    strings.TrimSpace(version),
		"git_tag":    strings.TrimSpace(gitTag),
		"commit":     strings.TrimSpace(commit),
		"build_date": strings.TrimSpace(buildDate),
		"build_tags": strings.TrimSpace(buildTags),
		"go_version": runtime.Version(),
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, info)
		return nil
	}
	fmt.Printf("version:    %s\n", info["version"])
	if gt := fmt.Sprint(info["git_tag"]); gt != "" {
		fmt.Printf("tag:        %s\n", gt)
	}
	fmt.Printf("commit:     %s\n", info["commit"])
	fmt.Printf("build date: %s\n", info["build_date"])
	if bt := fmt.Sprint(info["build_tags"]); bt != "" {
		fmt.Printf("build tags: %s\n", bt)
	}
	fmt.Printf("go:         %s\n", info["go_version"])
	return nil
}

type CaddyRunCmd struct{}

func (c *CaddyRunCmd) Run(_ *runContext) error {
	return app.CaddyRun()
}

type ServiceInstallCmd struct{}

func (c *ServiceInstallCmd) Run(r *runContext) error {
	if _, err := maybeCollectMissingServiceEnv(r); err != nil {
		return err
	}
	report, err := app.ServiceInstallWithReport()
	if err != nil {
		return err
	}
	r.out.ok("Background service installed", map[string]any{"label": "com.goliatone.switchd"})
	renderServiceEnvWarnings(r, report)
	return nil
}

type ServiceStartCmd struct{}

func (c *ServiceStartCmd) Run(r *runContext) error {
	if _, err := maybeCollectMissingServiceEnv(r); err != nil {
		return err
	}
	report, err := app.ServiceStartWithReport()
	if err != nil {
		return err
	}
	r.out.ok("Background service started", map[string]any{"label": "com.goliatone.switchd"})
	renderServiceEnvWarnings(r, report)
	return nil
}

type ServiceStopCmd struct{}

func (c *ServiceStopCmd) Run(r *runContext) error {
	if err := app.ServiceStop(); err != nil {
		return err
	}
	r.out.ok("Background service stopped", map[string]any{"label": "com.goliatone.switchd"})
	return nil
}

type ServiceStatusCmd struct{}

func (c *ServiceStatusCmd) Run(r *runContext) error {
	st, err := app.ServiceStatusInfo()
	if err != nil {
		return err
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, st)
		return nil
	}
	fmt.Printf("label:       %s\n", st.Label)
	fmt.Printf("installed:   %s\n", boolLabel(st.Installed))
	fmt.Printf("running:     %s\n", boolLabel(st.Running))
	fmt.Printf("ready:       %s\n", boolLabel(st.Ready))
	if st.Stale {
		fmt.Printf("stale:       %s\n", boolLabel(true))
	}
	if st.Phase != "" {
		fmt.Printf("phase:       %s\n", st.Phase)
	}
	if st.PID > 0 {
		fmt.Printf("pid:         %d\n", st.PID)
	}
	if st.CaddyPID > 0 {
		fmt.Printf("caddy pid:   %d\n", st.CaddyPID)
	}
	if st.StartedAt != "" {
		fmt.Printf("started at:  %s\n", st.StartedAt)
	}
	if st.ConfigPath != "" {
		fmt.Printf("config path: %s\n", st.ConfigPath)
	}
	if st.EnvFilePath != "" {
		fmt.Printf("env file:    %s\n", st.EnvFilePath)
	}
	if len(st.RequiredEnvVars) > 0 {
		fmt.Printf("required env: %s\n", strings.Join(st.RequiredEnvVars, ", "))
	}
	if len(st.ConfiguredEnvVars) > 0 {
		fmt.Printf("configured env: %s\n", strings.Join(st.ConfiguredEnvVars, ", "))
	}
	if len(st.MissingEnvVars) > 0 {
		fmt.Printf("missing env: %s\n", strings.Join(st.MissingEnvVars, ", "))
	}
	fmt.Printf("plist path:  %s\n", st.PlistPath)
	fmt.Printf("state path:  %s\n", st.RuntimeStatePath)
	fmt.Printf("log dir:     %s\n", st.LogDir)
	if st.StateError != "" {
		fmt.Printf("state err:   %s\n", st.StateError)
	}
	return nil
}

type ServiceLogCmd struct {
	Lines    int    `name:"lines" default:"50" help:"Number of recent lines to print before following."`
	Follow   bool   `name:"follow" default:"true" help:"Keep streaming appended log lines."`
	NoFollow bool   `name:"no-follow" help:"Print the recent snapshot and exit."`
	Stream   string `name:"stream" enum:"stdout,stderr,all" default:"all" help:"Log stream to read."`
}

func (c *ServiceLogCmd) Run(r *runContext) error {
	follow := c.Follow
	if c.NoFollow {
		follow = false
	}
	if c.Lines < 0 {
		return fmt.Errorf("--lines must be >= 0")
	}
	if useTUI, err := r.wantsTUIForServiceLog(); err != nil {
		return err
	} else if useTUI {
		return runServiceLogTUI(app.ServiceLogOptions{
			Lines:  c.Lines,
			Follow: follow,
			Stream: c.Stream,
		}, commandName(r.out.opts.CommandName), r.out.styles())
	}
	return serviceLogRun(app.ServiceLogOptions{
		Lines:  c.Lines,
		Follow: follow,
		Stream: c.Stream,
		JSON:   r.out.opts.JSON,
		Stdout: os.Stdout,
		Stderr: os.Stdout,
	})
}

type ServiceUninstallCmd struct{}

func (c *ServiceUninstallCmd) Run(r *runContext) error {
	if err := app.ServiceUninstall(); err != nil {
		return err
	}
	r.out.ok("Background service uninstalled", map[string]any{"label": "com.goliatone.switchd"})
	return nil
}

type DaemonRunCmd struct{}

func (c *DaemonRunCmd) Run(_ *runContext) error {
	return app.DaemonRun()
}

type HelpCmd struct {
	Path []string `arg:"" optional:"" help:"Command path to show help for."`
}

func (c *HelpCmd) Run(r *runContext) error {
	if r.parser == nil {
		return nil
	}
	args := append([]string{}, c.Path...)
	args = append(args, "--help")
	ctx, err := r.parser.Parse(args)
	if err != nil {
		return err
	}
	return ctx.PrintUsage(false)
}

type outputOptions struct {
	CommandName string
	Quiet       bool
	JSON        bool
	UI          string
	Interactive bool
}

type cliOutput struct {
	opts outputOptions
}

func (o cliOutput) styles() cliStyles {
	return newCLIStyles(o.opts.Interactive)
}

func (o cliOutput) jsonOut(w io.Writer, payload any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func sortedFieldKeys(fields map[string]any) []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (o cliOutput) textEvent(w io.Writer, level, msg string, fields map[string]any) {
	styles := o.styles()
	badge := "[" + level + "]"
	switch level {
	case "OK":
		badge = styles.badgeOK.Render("[" + level + "]")
	case "INFO":
		badge = styles.badgeInfo.Render("[" + level + "]")
	case "WARN":
		badge = styles.badgeWarn.Render("[" + level + "]")
	case "ERR":
		badge = styles.badgeErr.Render("[" + level + "]")
	}
	fmt.Fprintf(w, "%s %s\n", badge, msg)
	for _, k := range sortedFieldKeys(fields) {
		fmt.Fprintf(w, "  %s: %v\n", styles.key.Render(k), fields[k])
	}
}

func (o cliOutput) ok(msg string, fields map[string]any) {
	if o.opts.JSON {
		payload := map[string]any{"ok": true, "level": "ok", "message": msg}
		for k, v := range fields {
			payload[k] = v
		}
		o.jsonOut(os.Stdout, payload)
		return
	}
	if o.opts.Quiet {
		return
	}
	o.textEvent(os.Stdout, "OK", msg, fields)
}

func (o cliOutput) info(msg string, fields map[string]any) {
	if o.opts.JSON {
		payload := map[string]any{"ok": true, "level": "info", "message": msg}
		for k, v := range fields {
			payload[k] = v
		}
		o.jsonOut(os.Stdout, payload)
		return
	}
	if o.opts.Quiet {
		return
	}
	o.textEvent(os.Stdout, "INFO", msg, fields)
}

func (o cliOutput) warn(msg string, fields map[string]any) {
	if o.opts.JSON {
		payload := map[string]any{"ok": true, "level": "warn", "message": msg}
		for k, v := range fields {
			payload[k] = v
		}
		o.jsonOut(os.Stdout, payload)
		return
	}
	o.textEvent(os.Stdout, "WARN", msg, fields)
}

func (o cliOutput) commandError(command string, err error) {
	command = strings.TrimSpace(command)
	if command == "" {
		command = commandName(o.opts.CommandName)
	}

	if o.opts.JSON {
		payload := map[string]any{
			"ok":      false,
			"command": command,
			"error":   strings.TrimSpace(err.Error()),
		}
		if details, ok := tunnel.ActionableFromError(err); ok {
			payload["code"] = strings.TrimSpace(details.Code)
			payload["what"] = strings.TrimSpace(details.What)
			payload["why"] = strings.TrimSpace(details.Why)
			payload["checks"] = details.Checks
			payload["next_steps"] = details.NextSteps
		}
		o.jsonOut(os.Stderr, payload)
		return
	}

	if details, ok := tunnel.ActionableFromError(err); ok {
		o.textEvent(os.Stderr, "ERR", command+" failed", map[string]any{"code": strings.TrimSpace(details.Code)})
		if strings.TrimSpace(details.What) != "" {
			fmt.Fprintln(os.Stderr, "  what:", details.What)
		}
		if strings.TrimSpace(details.Why) != "" {
			fmt.Fprintln(os.Stderr, "  why:", details.Why)
		}
		for _, check := range details.Checks {
			check = strings.TrimSpace(check)
			if check == "" {
				continue
			}
			fmt.Fprintln(os.Stderr, "  check:", check)
		}
		for i, step := range details.NextSteps {
			step = strings.TrimSpace(step)
			if step == "" {
				continue
			}
			fmt.Fprintf(os.Stderr, "  next %d: %s\n", i+1, step)
		}
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			fmt.Fprintln(os.Stderr, "  detail:", err)
		}
		return
	}
	o.textEvent(os.Stderr, "ERR", command+" failed", map[string]any{"detail": err})
}

func (o cliOutput) printTable(headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}
	styles := o.styles()
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i := 0; i < len(headers) && i < len(row); i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	for i, h := range headers {
		fmt.Print(styles.tableHeader.Render(padCell(h, widths[i])))
		if i < len(headers)-1 {
			fmt.Print(styles.tableBorder.Render("  "))
		}
	}
	fmt.Println()
	for i := range headers {
		fmt.Print(styles.tableBorder.Render(strings.Repeat("-", widths[i])))
		if i < len(headers)-1 {
			fmt.Print(styles.tableBorder.Render("  "))
		}
	}
	fmt.Println()
	for _, row := range rows {
		for i := range headers {
			val := ""
			if i < len(row) {
				val = row[i]
			}
			fmt.Printf("%-*s", widths[i], val)
			if i < len(headers)-1 {
				fmt.Print("  ")
			}
		}
		fmt.Println()
	}
}

func padCell(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

type runContext struct {
	parser *kong.Kong
	out    cliOutput
	client *switchboard.Client
}

func (r *runContext) renderStackReport(command string, report switchboard.StackReport) error {
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, map[string]any{"stack": report, "command": command})
		return nil
	}
	model := buildStackReportViewModel(command, report)
	if useTUI, err := r.wantsTUIForStack(); err != nil {
		return err
	} else if useTUI {
		return stackReportTUIRun(model, r.out.styles())
	}
	return r.renderStackReportPlain(model)
}

func (r *runContext) renderStackReportPlain(model stackReportViewModel) error {
	fmt.Printf("stack: %s\n", model.StackName)
	fmt.Printf("file:  %s\n", model.StackFile)
	r.out.printTable([]string{"SERVICE", "APP", "LOCAL_HOST", "PORT", "PUBLIC_HOST", "PROVIDER", "SESSION", "DRIFT", "ACTIONS"}, buildStackReportTableRows(model))
	for _, collision := range model.Collisions {
		fmt.Printf("collision %s\n", collision)
	}
	for _, orphan := range model.Orphans {
		fmt.Printf("orphan: %s\n", orphan)
	}
	return nil
}

func findAppByInput(apps []config.App, raw string) (config.App, bool) {
	needle := strings.ToLower(strings.TrimSpace(raw))
	needle = strings.TrimSuffix(needle, ".")
	needle = strings.TrimPrefix(needle, "http://")
	needle = strings.TrimPrefix(needle, "https://")
	for _, a := range apps {
		if strings.EqualFold(strings.TrimSpace(a.Name), needle) {
			return a, true
		}
		if strings.EqualFold(strings.TrimSpace(a.LocalHost), needle) {
			return a, true
		}
	}
	return config.App{}, false
}

func findSwitchboardAppByInput(apps []switchboard.App, raw string) (switchboard.App, bool) {
	needle := strings.ToLower(strings.TrimSpace(raw))
	needle = strings.TrimSuffix(needle, ".")
	needle = strings.TrimPrefix(needle, "http://")
	needle = strings.TrimPrefix(needle, "https://")
	for _, a := range apps {
		if strings.EqualFold(strings.TrimSpace(a.Name), needle) {
			return a, true
		}
		if strings.EqualFold(strings.TrimSpace(a.LocalHost), needle) {
			return a, true
		}
	}
	return switchboard.App{}, false
}

func boolLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func valueOrDash(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	return v
}

func appSessionLabel(a config.App) string {
	if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
		return "active"
	}
	if strings.TrimSpace(a.PublicEndpoint.Host) != "" {
		return "idle"
	}
	return "none"
}

func switchboardAppSessionLabel(a switchboard.App, health switchboard.AppTunnelHealth, healthKnown bool) string {
	state := "none"
	if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
		state = "active"
	} else if strings.TrimSpace(a.PublicEndpoint.Host) != "" {
		state = "idle"
	}
	if state == "none" {
		return state
	}
	if !healthKnown || strings.TrimSpace(health.Err) != "" {
		return state + " ?"
	}
	if health.Ready {
		return state + " OK"
	}
	return state + " KO"
}

func renderServiceEnvWarnings(r *runContext, report app.ServiceEnvironmentReport) {
	if len(report.RequiredEnvVars) == 0 {
		return
	}
	if report.EnvFileCreated {
		r.out.info("Created background service env template", map[string]any{
			"env_file": report.EnvFilePath,
		})
	} else if report.EnvFileUpdated {
		r.out.info("Updated background service env template", map[string]any{
			"env_file": report.EnvFilePath,
		})
	}
	if len(report.EnvFileTemplateVars) > 0 {
		r.out.info("Added placeholder env vars to service env file", map[string]any{
			"env_file": report.EnvFilePath,
			"added":    strings.Join(report.EnvFileTemplateVars, ", "),
		})
	}
	r.out.info("Background service credentials are loaded from launchd environment, not your interactive shell", map[string]any{
		"env_file": report.EnvFilePath,
		"required": strings.Join(report.RequiredEnvVars, ", "),
	})
	if len(report.ConfiguredEnvVars) > 0 {
		r.out.info("Background service env vars configured", map[string]any{
			"configured": strings.Join(report.ConfiguredEnvVars, ", "),
			"env_file":   report.EnvFilePath,
		})
	}
	if len(report.MissingEnvVars) == 0 {
		return
	}
	r.out.warn("Background service is missing required env vars for provider resume", map[string]any{
		"env_file": report.EnvFilePath,
		"missing":  strings.Join(report.MissingEnvVars, ", "),
	})
	r.out.info("Add the missing values to the service env file and re-run `sudo switchd service start` or `sudo switchd service install`", map[string]any{
		"env_file": report.EnvFilePath,
	})
}

func tunnelInitFields(client *switchboard.Client, rawProvider string) map[string]any {
	fields := map[string]any{}
	if client == nil {
		if strings.TrimSpace(rawProvider) != "" {
			fields["provider"] = strings.TrimSpace(rawProvider)
		}
		return fields
	}
	cfg, err := client.LoadOrCreateDefaultConfig()
	if err != nil {
		if strings.TrimSpace(rawProvider) != "" {
			fields["provider"] = strings.TrimSpace(rawProvider)
		}
		return fields
	}
	providerName := strings.ToLower(strings.TrimSpace(rawProvider))
	if providerName == "" {
		providerName = strings.ToLower(strings.TrimSpace(cfg.Tunnel.DefaultProvider))
	}
	if providerName == "" {
		return fields
	}
	fields["provider"] = providerName
	providerCfg, ok := cfg.Tunnel.Providers[providerName]
	if !ok {
		return fields
	}
	if mode := strings.TrimSpace(providerCfg.Values["mode"]); mode != "" {
		fields["mode"] = mode
	}
	if baseDomain := strings.TrimSpace(providerCfg.Values["base_domain"]); baseDomain != "" {
		fields["base_domain"] = baseDomain
	}
	if tokenEnv := strings.TrimSpace(providerCfg.Values["api_token_env"]); tokenEnv != "" {
		fields["api_token_env"] = tokenEnv
	}
	if originCert := strings.TrimSpace(providerCfg.Values["origincert"]); originCert != "" {
		fields["origincert"] = originCert
	}
	if providerCfg.AccountID != "" {
		fields["account_id"] = providerCfg.AccountID
	}
	if providerCfg.Zone != "" {
		fields["zone_id"] = providerCfg.Zone
	}
	return fields
}

func renderStatusReport(report app.StatusReport) {
	fmt.Println("Config:", report.ConfigPath)
	fmt.Println("TLD:   ", report.TLD)
	fmt.Println("DNS IP:", report.DNSIP)
	fmt.Println("Caddy:", report.CaddyAdmin)
	fmt.Printf("TLS:    enabled=%t mode=%s\n", report.TLS.Enabled, report.TLS.Mode)

	fmt.Println()
	fmt.Println("Checks:")

	if strings.TrimSpace(report.ServiceError) != "" {
		fmt.Println("- Service:", "error:", report.ServiceError)
	} else {
		st := report.Service
		switch {
		case st.Running:
			if st.Phase != "" {
				fmt.Printf("- Service: installed=%s running=%s ready=%s pid=%d phase=%s\n", boolLabel(st.Installed), boolLabel(true), boolLabel(st.Ready), st.PID, st.Phase)
			} else {
				fmt.Printf("- Service: installed=%s running=%s ready=%s pid=%d\n", boolLabel(st.Installed), boolLabel(true), boolLabel(st.Ready), st.PID)
			}
		case st.Stale:
			if st.StateError != "" {
				fmt.Printf("- Service: installed=%s running=%s stale_runtime_state pid=%d error=%s\n", boolLabel(st.Installed), boolLabel(false), st.PID, st.StateError)
			} else {
				fmt.Printf("- Service: installed=%s running=%s stale_runtime_state pid=%d\n", boolLabel(st.Installed), boolLabel(false), st.PID)
			}
		default:
			fmt.Printf("- Service: installed=%s running=%s ready=%s\n", boolLabel(st.Installed), boolLabel(false), boolLabel(st.Ready))
		}
		if len(st.MissingEnvVars) > 0 {
			fmt.Printf("  Missing env for background resume: %s\n", strings.Join(st.MissingEnvVars, ", "))
			if strings.TrimSpace(st.EnvFilePath) != "" {
				fmt.Printf("  Add them to: %s\n", st.EnvFilePath)
			}
		}
	}

	if !report.TLS.Valid {
		fmt.Println("- TLS:", "invalid:", report.TLS.Error)
	} else {
		fmt.Println("- TLS:", "config ok")
	}
	for _, warning := range report.TLS.Warnings {
		fmt.Println("- TLS:", "warning:", warning)
	}

	fmt.Println("- DNS:", report.DNS.Message)
	fmt.Println("- Caddy:", report.Caddy.Message)
	if strings.TrimSpace(report.Caddy.StartHint) != "" {
		fmt.Println("  Start:", report.Caddy.StartHint)
	}

	fmt.Println("- Apps:", fmt.Sprintf("%d configured", len(report.Apps)))
	if len(report.Apps) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, item := range report.Apps {
			fmt.Printf("  - %s: host=%s port=%d\n", item.Name, item.Host, item.Port)
		}
	}

	if strings.TrimSpace(report.TunnelHealthError) != "" {
		fmt.Println("- Tunnel health:", "error:", report.TunnelHealthError)
		return
	}
	fmt.Println("- Tunnel health:")
	if len(report.TunnelHealth) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, item := range report.TunnelHealth {
		if strings.TrimSpace(item.Provider) == "" {
			fmt.Printf("  - %s: no tunnel configured\n", item.AppName)
			continue
		}
		base := fmt.Sprintf("  - %s: provider=%s host=%s", item.AppName, item.Provider, item.EndpointHost)
		if strings.TrimSpace(item.Error) != "" {
			fmt.Printf("%s status=error %s\n", base, item.Error)
			continue
		}
		sessionInfo := strings.TrimSpace(item.SessionSummary)
		if sessionInfo != "" {
			sessionInfo = " " + sessionInfo
		}
		fmt.Printf("%s status=%s %s%s\n", base, item.Status, item.Message, sessionInfo)
	}
}

func commandName(raw string) string {
	name := filepath.Base(strings.TrimSpace(raw))
	if name == "" || name == "." || name == string(os.PathSeparator) {
		return defaultCommandName
	}
	return name
}

func newParser(cli *CLI, rawCommandName string) (*kong.Kong, error) {
	name := commandName(rawCommandName)
	return kong.New(
		cli,
		kong.Name(name),
		kong.Description("manage *.test local DNS + Caddy routes (switchboard-hub)"),
		kong.UsageOnError(),
	)
}

func main() {
	cli := CLI{}
	invokedName := commandName(os.Args[0])
	parser, err := newParser(&cli, invokedName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", invokedName, err)
		os.Exit(1)
	}

	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"--help"}
	}

	ctx, err := parser.Parse(args)
	if err != nil {
		parser.FatalIfErrorf(err)
		return
	}

	diag.SetDebug(cli.Debug || cli.Verbose)
	rc := &runContext{
		parser: parser,
		out: cliOutput{opts: outputOptions{
			CommandName: invokedName,
			Quiet:       cli.Quiet,
			JSON:        cli.useJSON(),
			UI:          cli.uiMode(),
			Interactive: isInteractiveTTY(),
		}},
		client: switchboard.Default(),
	}

	if err := ctx.Run(rc); err != nil {
		rc.out.commandError(ctx.Command(), err)
		os.Exit(1)
	}
}
