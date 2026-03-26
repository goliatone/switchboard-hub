package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
	buildTags = ""
	gitTag    = ""
)

type globalFlags struct {
	Debug   bool   `name:"debug" help:"Enable debug logs."`
	Verbose bool   `name:"verbose" short:"v" help:"Alias for --debug."`
	Quiet   bool   `name:"quiet" short:"q" help:"Suppress non-essential success/info output."`
	JSON    bool   `name:"json" help:"Print machine-readable JSON output when supported."`
	Output  string `name:"output" enum:"text,json" default:"text" help:"Output mode."`
}

func (g globalFlags) useJSON() bool {
	if g.JSON {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(g.Output), "json")
}

type CLI struct {
	globalFlags

	Init      InitCmd      `cmd:"" help:"Initialize switchd local DNS + Caddy setup."`
	App       AppCmd       `cmd:"" help:"Manage app definitions and tunnel exposure."`
	Tunnel    TunnelCmd    `cmd:"" help:"Manage tunnel providers."`
	Add       AddCmd       `cmd:"" help:"Add a route."`
	Rm        RmCmd        `cmd:"" help:"Remove a route."`
	Ls        LsCmd        `cmd:"" help:"List routes."`
	Apply     ApplyCmd     `cmd:"" help:"Apply config to Caddy admin API."`
	TLS       TLSCmd       `cmd:"" name:"tls" help:"TLS helpers."`
	Open      OpenCmd      `cmd:"" help:"Open app URL in browser."`
	Uninstall UninstallCmd `cmd:"" help:"Uninstall switchd local setup."`
	Status    StatusCmd    `cmd:"" help:"Show status diagnostics."`
	Version   VersionCmd   `cmd:"" help:"Show build/version info."`
	Caddy     CaddyCmd     `cmd:"" help:"Caddy control commands."`
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
}

func (c *AppCreateCmd) Run(r *runContext) error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("--port must be 1..65535")
	}
	if err := app.CreateApp(c.NameOrHost, c.Port); err != nil {
		return err
	}
	apps, err := app.ListApps()
	if err == nil {
		if created, ok := findAppByInput(apps, c.NameOrHost); ok {
			r.out.ok("App created", map[string]any{
				"app":   created.Name,
				"local": created.LocalHost,
				"port":  created.LocalPort,
			})
			return nil
		}
	}
	r.out.ok("App created", map[string]any{"app": c.NameOrHost, "port": c.Port})
	return nil
}

type AppRmCmd struct {
	Name string `arg:"" name:"name" help:"App name."`
}

func (c *AppRmCmd) Run(r *runContext) error {
	if err := app.RemoveApp(c.Name); err != nil {
		return err
	}
	r.out.ok("App removed", map[string]any{"app": c.Name})
	return nil
}

type AppLsCmd struct{}

func (c *AppLsCmd) Run(r *runContext) error {
	apps, err := app.ListApps()
	if err != nil {
		return err
	}
	if r.out.opts.JSON {
		r.out.jsonOut(os.Stdout, map[string]any{"apps": apps, "count": len(apps)})
		return nil
	}
	if len(apps) == 0 {
		r.out.info("No apps configured", nil)
		return nil
	}
	rows := make([][]string, 0, len(apps))
	for _, a := range apps {
		public := strings.TrimSpace(a.PublicEndpoint.Host)
		if public == "" {
			public = "-"
		}
		oauth := "off"
		if a.OAuth.Google.Enabled {
			oauth = "google"
		}
		rows = append(rows, []string{
			a.Name,
			a.LocalHost,
			fmt.Sprintf("%d", a.LocalPort),
			public,
			oauth,
			appSessionLabel(a),
		})
	}
	r.out.printTable([]string{"NAME", "LOCAL_HOST", "PORT", "PUBLIC_HOST", "OAUTH", "TUNNEL"}, rows)
	return nil
}

type AppExposeCmd struct {
	Name       string `arg:"" name:"name" help:"App name."`
	Provider   string `name:"provider" help:"Tunnel provider (defaults to config tunnels.default_provider)."`
	PublicHost string `name:"public-host" help:"Public callback host."`
}

func (c *AppExposeCmd) Run(r *runContext) error {
	if err := app.ExposeApp(c.Name, c.Provider, c.PublicHost); err != nil {
		return err
	}
	apps, err := app.ListApps()
	if err == nil {
		if exposed, ok := findAppByInput(apps, c.Name); ok {
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
	if err := app.AppUp(c.Name); err != nil {
		return err
	}
	apps, err := app.ListApps()
	if err == nil {
		if runtime, ok := findAppByInput(apps, c.Name); ok {
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
	beforeApps, _ := app.ListApps()
	before, _ := findAppByInput(beforeApps, c.Name)
	if err := app.AppDown(c.Name); err != nil {
		return err
	}
	afterApps, err := app.ListApps()
	if err == nil {
		if after, ok := findAppByInput(afterApps, c.Name); ok {
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

type TunnelProvidersCmd struct{}

func (c *TunnelProvidersCmd) Run(r *runContext) error {
	providers := app.TunnelProviders()
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
	if err := app.TunnelInitWithOptions(c.Provider, app.TunnelInitOptions{
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
	r.out.ok("Tunnel provider initialized", map[string]any{"provider": c.Provider, "mode": c.Mode, "base_domain": c.BaseDomain})
	return nil
}

type TunnelStatusCmd struct {
	Provider string `name:"provider" help:"Provider name (optional)."`
}

func (c *TunnelStatusCmd) Run(r *runContext) error {
	statuses, err := app.TunnelStatus(c.Provider)
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
	if err := app.AddRoute(nameOrHost, c.Port); err != nil {
		return err
	}
	r.out.ok("Route added", map[string]any{"host": nameOrHost, "port": c.Port})
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
		return fmt.Errorf("JSON output is not supported for this command yet")
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
	if r.out.opts.JSON {
		return fmt.Errorf("JSON output is not supported for this command yet")
	}
	return app.Status()
}

type CaddyCmd struct {
	Run CaddyRunCmd `cmd:"" name:"run" help:"Run Caddy foreground process."`
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
	Quiet bool
	JSON  bool
}

type cliOutput struct {
	opts outputOptions
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
	fmt.Fprintf(w, "[%s] %s\n", level, msg)
	for _, k := range sortedFieldKeys(fields) {
		fmt.Fprintf(w, "  %s: %v\n", k, fields[k])
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
		command = "switchd"
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
		fmt.Printf("%-*s", widths[i], h)
		if i < len(headers)-1 {
			fmt.Print("  ")
		}
	}
	fmt.Println()
	for i := range headers {
		fmt.Printf("%-*s", widths[i], strings.Repeat("-", widths[i]))
		if i < len(headers)-1 {
			fmt.Print("  ")
		}
	}
	fmt.Println()
	for _, row := range rows {
		for i := 0; i < len(headers); i++ {
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

type runContext struct {
	parser *kong.Kong
	out    cliOutput
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

func boolLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
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

func main() {
	cli := CLI{}
	parser, err := kong.New(
		&cli,
		kong.Name("switchd"),
		kong.Description("manage *.test local DNS + Caddy routes (switchboard-hub)"),
		kong.UsageOnError(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "switchd error:", err)
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
			Quiet: cli.Quiet,
			JSON:  cli.useJSON(),
		}},
	}

	if err := ctx.Run(rc); err != nil {
		rc.out.commandError(ctx.Command(), err)
		os.Exit(1)
	}
}
