package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

func usage() {
	printRootUsage(os.Stderr)
}

func printRootUsage(w io.Writer) {
	fmt.Fprintf(w, `switchd - manage *.test local DNS + Caddy routes (switchboard-hub)

Usage:
  switchd [global options] <command> ...

Global options:
  --debug            Enable debug logs
  --verbose, -v      Alias for --debug
  --quiet, -q        Suppress non-essential success/info output
  --json             Print machine-readable JSON output when supported
  --output text|json Output mode (same as --json when set to json)
  -h, --help         Show help

Commands:
  switchd init [--tld test] [--dns-ip 10.0.0.1] [--tls] [--tls-mode internal|file] [--tls-cert-file <path>] [--tls-key-file <path>] # sudo
  switchd app <subcommand>
  switchd tunnel <subcommand>
  switchd add <name-or-host> --port <port>
  switchd rm <name-or-host>
  switchd ls
  switchd apply
  switchd tls mkcert [--install] [--cert-file <path>] [--key-file <path>]
  switchd open <name-or-host> [--scheme http|https]
  switchd uninstall                                       # sudo
  switchd status
  switchd caddy run                                       # sudo (binds :80/:443)
  switchd help [command]

Examples:
  sudo switchd init --tld test --dns-ip 10.0.0.1
  switchd app create esign --port 3000
  switchd app expose esign --provider cloudflare --public-host esign.getctx.com
  switchd app up esign
  switchd tunnel init --provider cloudflare --mode api --account-id <id> --zone-id <id> --base-domain getctx.com

Help:
  switchd help app
  switchd help tunnel
  switchd app --help
  switchd tunnel --help
`)
}

func printAppUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage:
  switchd app create <name-or-host> --port <port>
  switchd app rm <name>
  switchd app ls
  switchd app expose <name> [--provider <provider>] [--public-host <fqdn>]
  switchd app up <name>
  switchd app down <name>
  switchd app oauth google enable <name> --callback-path <path>
  switchd app oauth google print <name>
`)
}

func printAppSubUsage(w io.Writer, sub string) {
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "create":
		fmt.Fprintf(w, "Usage:\n  switchd app create <name-or-host> --port <port>\n")
	case "rm":
		fmt.Fprintf(w, "Usage:\n  switchd app rm <name>\n")
	case "ls":
		fmt.Fprintf(w, "Usage:\n  switchd app ls\n")
	case "expose":
		fmt.Fprintf(w, "Usage:\n  switchd app expose <name> [--provider <provider>] [--public-host <fqdn>]\n")
	case "up":
		fmt.Fprintf(w, "Usage:\n  switchd app up <name>\n")
	case "down":
		fmt.Fprintf(w, "Usage:\n  switchd app down <name>\n")
	case "oauth":
		fmt.Fprintf(w, `Usage:
  switchd app oauth google enable <name> --callback-path <path>
  switchd app oauth google print <name>
`)
	case "oauth-enable":
		fmt.Fprintf(w, "Usage:\n  switchd app oauth google enable <name> --callback-path <path>\n")
	case "oauth-print":
		fmt.Fprintf(w, "Usage:\n  switchd app oauth google print <name>\n")
	default:
		printAppUsage(w)
	}
}

func printTunnelUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage:
  switchd tunnel providers
  switchd tunnel init --provider <provider> [--mode cli|api] [--setup] [--non-interactive] [--origincert <path>] [--account-id <id>] [--zone-id <id>] [--base-domain <domain>] [--api-token-env <ENV_NAME>]
  switchd tunnel status [--provider <provider>]
`)
}

func printTunnelSubUsage(w io.Writer, sub string) {
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "providers":
		fmt.Fprintf(w, "Usage:\n  switchd tunnel providers\n")
	case "init":
		fmt.Fprintf(w, "Usage:\n  switchd tunnel init --provider <provider> [--mode cli|api] [--setup] [--non-interactive] [--origincert <path>] [--account-id <id>] [--zone-id <id>] [--base-domain <domain>] [--api-token-env <ENV_NAME>]\n")
	case "status":
		fmt.Fprintf(w, "Usage:\n  switchd tunnel status [--provider <provider>]\n")
	default:
		printTunnelUsage(w)
	}
}

func printHelp(args []string) {
	if len(args) == 0 {
		printRootUsage(os.Stdout)
		return
	}
	cmd := strings.ToLower(strings.TrimSpace(args[0]))
	switch cmd {
	case "app":
		if len(args) == 1 {
			printAppUsage(os.Stdout)
			return
		}
		if len(args) >= 3 && args[1] == "oauth" && args[2] == "google" {
			if len(args) >= 4 {
				printAppSubUsage(os.Stdout, "oauth-"+args[3])
				return
			}
			printAppSubUsage(os.Stdout, "oauth")
			return
		}
		printAppSubUsage(os.Stdout, args[1])
	case "tunnel":
		if len(args) == 1 {
			printTunnelUsage(os.Stdout)
			return
		}
		printTunnelSubUsage(os.Stdout, args[1])
	default:
		printRootUsage(os.Stdout)
	}
}

type boolFlag interface {
	IsBoolFlag() bool
}

// parseInterspersedFlags allows both:
// - switchd add --port 3030 myapp
// - switchd add myapp --port 3030
func parseInterspersedFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	flagArgs := make([]string, 0, len(args))
	posArgs := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			posArgs = append(posArgs, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			posArgs = append(posArgs, a)
			continue
		}

		name := strings.TrimLeft(a, "-")
		if eq := strings.Index(name, "="); eq >= 0 {
			name = name[:eq]
		}
		f := fs.Lookup(name)
		if f == nil {
			if a == "-h" || a == "--help" {
				flagArgs = append(flagArgs, a)
				continue
			}
			return nil, fmt.Errorf("unknown flag: %s", a)
		}

		flagArgs = append(flagArgs, a)
		if strings.Contains(a, "=") {
			continue
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag needs a value: %s", a)
		}
		i++
		flagArgs = append(flagArgs, args[i])
	}

	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	return append(fs.Args(), posArgs...), nil
}

type globalOptions struct {
	Debug   bool
	Verbose bool
	Quiet   bool
	JSON    bool
}

func extractGlobalFlags(args []string) ([]string, globalOptions, error) {
	out := make([]string, 0, len(args))
	opts := globalOptions{}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--debug" || a == "-debug":
			opts.Debug = true
		case a == "--verbose" || a == "-v":
			opts.Verbose = true
		case a == "--quiet" || a == "-q":
			opts.Quiet = true
		case a == "--json":
			opts.JSON = true
		case a == "--output":
			if i+1 >= len(args) {
				return nil, opts, fmt.Errorf("--output requires a value: text or json")
			}
			i++
			if err := applyOutputMode(&opts, args[i]); err != nil {
				return nil, opts, err
			}
		case strings.HasPrefix(a, "--output="):
			if err := applyOutputMode(&opts, strings.TrimPrefix(a, "--output=")); err != nil {
				return nil, opts, err
			}
		default:
			out = append(out, a)
		}
	}
	return out, opts, nil
}

func applyOutputMode(opts *globalOptions, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "text", "":
		opts.JSON = false
		return nil
	case "json":
		opts.JSON = true
		return nil
	default:
		return fmt.Errorf("invalid --output value %q (expected text or json)", mode)
	}
}

func isHelpToken(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "-h" || s == "--help" || s == "help"
}

func hasHelpToken(args []string) bool {
	for _, a := range args {
		if isHelpToken(a) {
			return true
		}
	}
	return false
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

type cliOutput struct {
	opts globalOptions
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

func (o cliOutput) usageError(message string) {
	if o.opts.JSON {
		o.jsonOut(os.Stderr, map[string]any{"ok": false, "error": message, "kind": "usage"})
		return
	}
	fmt.Fprintf(os.Stderr, "[ERR] %s\n", message)
}

func (o cliOutput) commandError(command string, err error) {
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
	rawArgs, opts, err := extractGlobalFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "switchd error:", err)
		os.Exit(2)
	}
	diag.SetDebug(opts.Debug || opts.Verbose)
	out := cliOutput{opts: opts}

	if len(rawArgs) < 1 {
		usage()
		os.Exit(2)
	}

	if isHelpToken(rawArgs[0]) {
		printHelp(rawArgs[1:])
		return
	}

	cmd := rawArgs[0]
	args := rawArgs[1:]

	switch cmd {
	case "help":
		printHelp(args)
		return

	case "init":
		if hasHelpToken(args) {
			printRootUsage(os.Stdout)
			return
		}
		fs := newFlagSet("init")
		tld := fs.String("tld", "test", "development TLD to manage (e.g. test)")
		dnsIP := fs.String("dns-ip", "10.0.0.1", "loopback alias IP for dnsmasq (e.g. 10.0.0.1)")
		tlsEnabled := fs.Bool("tls", true, "enable HTTPS listeners in Caddy")
		tlsMode := fs.String("tls-mode", "", "TLS mode: internal or file (default: internal)")
		tlsCertFile := fs.String("tls-cert-file", "", "certificate file path (PEM), required when --tls-mode file")
		tlsKeyFile := fs.String("tls-key-file", "", "key file path (PEM), required when --tls-mode file")
		if err := fs.Parse(args); err != nil {
			out.usageError("init: " + err.Error())
			os.Exit(2)
		}

		if err := app.Init(*tld, *dnsIP, *tlsEnabled, *tlsMode, *tlsCertFile, *tlsKeyFile); err != nil {
			out.commandError("init", err)
			os.Exit(1)
		}
		out.ok("Initialization complete", map[string]any{"tld": *tld, "dns_ip": *dnsIP, "tls": *tlsEnabled})

	case "app":
		if len(args) < 1 || isHelpToken(args[0]) {
			printAppUsage(os.Stdout)
			return
		}
		sub := args[0]
		switch sub {
		case "create":
			if hasHelpToken(args[1:]) {
				printAppSubUsage(os.Stdout, "create")
				return
			}
			fs := newFlagSet("app create")
			port := fs.Int("port", 0, "local port to proxy to (required)")
			pos, err := parseInterspersedFlags(fs, args[1:])
			if err != nil {
				out.usageError("app create: " + err.Error())
				os.Exit(2)
			}
			if *port <= 0 || *port > 65535 {
				out.usageError("app create: --port is required and must be 1..65535")
				os.Exit(2)
			}
			if len(pos) != 1 {
				out.usageError("app create: provide exactly one <name-or-host>")
				os.Exit(2)
			}
			if err := app.CreateApp(pos[0], *port); err != nil {
				out.commandError("app create", err)
				os.Exit(1)
			}
			apps, _ := app.ListApps()
			created, ok := findAppByInput(apps, pos[0])
			if ok {
				out.ok("App created", map[string]any{
					"app":   created.Name,
					"local": created.LocalHost,
					"port":  created.LocalPort,
				})
				return
			}
			out.ok("App created", map[string]any{"app": pos[0], "port": *port})

		case "rm":
			if hasHelpToken(args[1:]) {
				printAppSubUsage(os.Stdout, "rm")
				return
			}
			if len(args) != 2 {
				out.usageError("app rm: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.RemoveApp(args[1]); err != nil {
				out.commandError("app rm", err)
				os.Exit(1)
			}
			out.ok("App removed", map[string]any{"app": args[1]})

		case "ls":
			if hasHelpToken(args[1:]) {
				printAppSubUsage(os.Stdout, "ls")
				return
			}
			apps, err := app.ListApps()
			if err != nil {
				out.commandError("app ls", err)
				os.Exit(1)
			}
			if out.opts.JSON {
				out.jsonOut(os.Stdout, map[string]any{"apps": apps, "count": len(apps)})
				return
			}
			if len(apps) == 0 {
				out.info("No apps configured", nil)
				return
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
			out.printTable([]string{"NAME", "LOCAL_HOST", "PORT", "PUBLIC_HOST", "OAUTH", "TUNNEL"}, rows)

		case "expose":
			if hasHelpToken(args[1:]) {
				printAppSubUsage(os.Stdout, "expose")
				return
			}
			fs := newFlagSet("app expose")
			provider := fs.String("provider", "", "tunnel provider (defaults to config tunnels.default_provider)")
			publicHost := fs.String("public-host", "", "public callback host (optional for cloudflare with base_domain configured)")
			pos, err := parseInterspersedFlags(fs, args[1:])
			if err != nil {
				out.usageError("app expose: " + err.Error())
				os.Exit(2)
			}
			if len(pos) != 1 {
				out.usageError("app expose: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.ExposeApp(pos[0], *provider, *publicHost); err != nil {
				out.commandError("app expose", err)
				os.Exit(1)
			}
			apps, _ := app.ListApps()
			exposed, ok := findAppByInput(apps, pos[0])
			if ok {
				out.ok("Public endpoint configured", map[string]any{
					"app":         exposed.Name,
					"provider":    exposed.PublicEndpoint.Provider,
					"public_host": exposed.PublicEndpoint.Host,
					"endpoint_id": exposed.PublicEndpoint.EndpointID,
				})
				return
			}
			out.ok("Public endpoint configured", map[string]any{"app": pos[0]})

		case "up":
			if hasHelpToken(args[1:]) {
				printAppSubUsage(os.Stdout, "up")
				return
			}
			if len(args) != 2 {
				out.usageError("app up: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.AppUp(args[1]); err != nil {
				out.commandError("app up", err)
				os.Exit(1)
			}
			apps, _ := app.ListApps()
			runtime, ok := findAppByInput(apps, args[1])
			if ok {
				fields := map[string]any{
					"app":         runtime.Name,
					"local_url":   fmt.Sprintf("http://%s", runtime.LocalHost),
					"public_host": runtime.PublicEndpoint.Host,
				}
				if strings.TrimSpace(runtime.PublicEndpoint.ActiveSessionID) != "" {
					fields["session_id"] = runtime.PublicEndpoint.ActiveSessionID
				}
				out.ok("App runtime started", fields)
				return
			}
			out.ok("App runtime started", map[string]any{"app": args[1]})

		case "down":
			if hasHelpToken(args[1:]) {
				printAppSubUsage(os.Stdout, "down")
				return
			}
			if len(args) != 2 {
				out.usageError("app down: provide exactly one <name>")
				os.Exit(2)
			}
			beforeApps, _ := app.ListApps()
			before, _ := findAppByInput(beforeApps, args[1])
			if err := app.AppDown(args[1]); err != nil {
				out.commandError("app down", err)
				os.Exit(1)
			}
			afterApps, _ := app.ListApps()
			after, ok := findAppByInput(afterApps, args[1])
			if ok {
				if strings.TrimSpace(before.PublicEndpoint.ActiveSessionID) == "" {
					out.warn("App tunnel already stopped", map[string]any{"app": after.Name})
					return
				}
				out.ok("App tunnel stopped", map[string]any{"app": after.Name, "public_host": after.PublicEndpoint.Host})
				return
			}
			out.ok("App tunnel stopped", map[string]any{"app": args[1]})

		case "oauth":
			if len(args) < 2 || isHelpToken(args[1]) {
				printAppSubUsage(os.Stdout, "oauth")
				return
			}
			if args[1] != "google" {
				out.usageError("app oauth: unknown provider " + args[1])
				os.Exit(2)
			}
			if len(args) < 3 || isHelpToken(args[2]) {
				printAppSubUsage(os.Stdout, "oauth")
				return
			}
			switch args[2] {
			case "enable":
				if hasHelpToken(args[3:]) {
					printAppSubUsage(os.Stdout, "oauth-enable")
					return
				}
				fs := newFlagSet("app oauth google enable")
				callbackPath := fs.String("callback-path", "", "oauth callback path (required, must start with /)")
				pos, err := parseInterspersedFlags(fs, args[3:])
				if err != nil {
					out.usageError("app oauth google enable: " + err.Error())
					os.Exit(2)
				}
				if len(pos) != 1 {
					out.usageError("app oauth google enable: provide exactly one <name>")
					os.Exit(2)
				}
				if strings.TrimSpace(*callbackPath) == "" {
					out.usageError("app oauth google enable: --callback-path is required")
					os.Exit(2)
				}
				if err := app.OAuthGoogleEnable(pos[0], *callbackPath); err != nil {
					out.commandError("app oauth google enable", err)
					os.Exit(1)
				}
				apps, _ := app.ListApps()
				current, ok := findAppByInput(apps, pos[0])
				if ok {
					out.ok("Google OAuth enabled", map[string]any{
						"app":           current.Name,
						"callback_path": current.OAuth.Google.CallbackPath,
						"redirect_uri":  current.OAuth.Google.RedirectURI,
					})
					return
				}
				out.ok("Google OAuth enabled", map[string]any{"app": pos[0], "callback_path": *callbackPath})

			case "print":
				if hasHelpToken(args[3:]) {
					printAppSubUsage(os.Stdout, "oauth-print")
					return
				}
				if len(args) != 4 {
					out.usageError("app oauth google print: provide exactly one <name>")
					os.Exit(2)
				}
				block, err := app.OAuthGooglePrint(args[3])
				if err != nil {
					out.commandError("app oauth google print", err)
					os.Exit(1)
				}
				if out.opts.JSON {
					out.jsonOut(os.Stdout, map[string]any{"app": args[3], "output": block})
					return
				}
				fmt.Println(block)

			default:
				out.usageError("app oauth google: unknown subcommand " + args[2])
				os.Exit(2)
			}

		default:
			out.usageError("app: unknown subcommand " + sub)
			os.Exit(2)
		}

	case "tunnel":
		if len(args) < 1 || isHelpToken(args[0]) {
			printTunnelUsage(os.Stdout)
			return
		}
		sub := args[0]
		switch sub {
		case "providers":
			if hasHelpToken(args[1:]) {
				printTunnelSubUsage(os.Stdout, "providers")
				return
			}
			providers := app.TunnelProviders()
			if out.opts.JSON {
				out.jsonOut(os.Stdout, map[string]any{"providers": providers, "count": len(providers)})
				return
			}
			rows := make([][]string, 0, len(providers))
			for _, p := range providers {
				rows = append(rows, []string{p})
			}
			out.printTable([]string{"PROVIDER"}, rows)

		case "init":
			if hasHelpToken(args[1:]) {
				printTunnelSubUsage(os.Stdout, "init")
				return
			}
			fs := newFlagSet("tunnel init")
			provider := fs.String("provider", "", "provider name (required)")
			mode := fs.String("mode", "", "cloudflare mode: cli or api")
			setup := fs.Bool("setup", false, "attempt provider bootstrap if preflight fails (cloudflare: runs `cloudflared tunnel login`)")
			nonInteractive := fs.Bool("non-interactive", false, "disable interactive setup flow")
			originCert := fs.String("origincert", "", "cloudflare origin cert path override")
			accountID := fs.String("account-id", "", "cloudflare account id (for mode=api)")
			zoneID := fs.String("zone-id", "", "cloudflare zone id (for mode=api)")
			baseDomain := fs.String("base-domain", "", "default public tunnel base domain, e.g. tnl.example.com")
			apiTokenEnv := fs.String("api-token-env", "", "env var holding cloudflare api token (default: SWITCHD_CF_API_TOKEN)")
			if err := fs.Parse(args[1:]); err != nil {
				out.usageError("tunnel init: " + err.Error())
				os.Exit(2)
			}
			if strings.TrimSpace(*provider) == "" {
				out.usageError("tunnel init: --provider is required")
				os.Exit(2)
			}
			if err := app.TunnelInitWithOptions(*provider, app.TunnelInitOptions{
				Setup:          *setup,
				NonInteractive: *nonInteractive,
				OriginCert:     *originCert,
				Mode:           *mode,
				AccountID:      *accountID,
				ZoneID:         *zoneID,
				BaseDomain:     *baseDomain,
				APITokenEnv:    *apiTokenEnv,
			}); err != nil {
				out.commandError("tunnel init", err)
				os.Exit(1)
			}
			out.ok("Tunnel provider initialized", map[string]any{"provider": *provider, "mode": *mode, "base_domain": *baseDomain})

		case "status":
			if hasHelpToken(args[1:]) {
				printTunnelSubUsage(os.Stdout, "status")
				return
			}
			fs := newFlagSet("tunnel status")
			provider := fs.String("provider", "", "provider name (optional)")
			if err := fs.Parse(args[1:]); err != nil {
				out.usageError("tunnel status: " + err.Error())
				os.Exit(2)
			}
			statuses, err := app.TunnelStatus(*provider)
			if err != nil {
				out.commandError("tunnel status", err)
				os.Exit(1)
			}
			if out.opts.JSON {
				out.jsonOut(os.Stdout, map[string]any{"providers": statuses, "count": len(statuses)})
				return
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
			out.printTable([]string{"PROVIDER", "ENABLED", "OAUTH", "STATUS", "DETAIL"}, rows)
			for _, st := range statuses {
				for _, note := range st.Notes {
					note = strings.TrimSpace(note)
					if note == "" {
						continue
					}
					out.info("provider note", map[string]any{"provider": st.Name, "note": note})
				}
			}

		default:
			out.usageError("tunnel: unknown subcommand " + sub)
			os.Exit(2)
		}

	case "add":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd add <name-or-host> --port <port>")
			return
		}
		fs := newFlagSet("add")
		port := fs.Int("port", 0, "local port to proxy to (required)")
		host := fs.String("host", "", "explicit host (overrides positional argument)")
		pos, err := parseInterspersedFlags(fs, args)
		if err != nil {
			out.usageError("add: " + err.Error())
			os.Exit(2)
		}
		if *port <= 0 || *port > 65535 {
			out.usageError("add: --port is required and must be 1..65535")
			os.Exit(2)
		}
		if len(pos) < 1 && *host == "" {
			out.usageError("add: provide <name-or-host> or --host")
			os.Exit(2)
		}
		if *host != "" && len(pos) > 0 {
			out.usageError("add: use either positional <name-or-host> or --host, not both")
			os.Exit(2)
		}
		if *host == "" && len(pos) != 1 {
			out.usageError("add: provide exactly one <name-or-host>")
			os.Exit(2)
		}
		nameOrHost := *host
		if nameOrHost == "" {
			nameOrHost = pos[0]
		}
		if err := app.AddRoute(nameOrHost, *port); err != nil {
			out.commandError("add", err)
			os.Exit(1)
		}
		out.ok("Route added", map[string]any{"host": nameOrHost, "port": *port})

	case "rm":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd rm <name-or-host>")
			return
		}
		if len(args) != 1 {
			out.usageError("rm: provide exactly one <name-or-host>")
			os.Exit(2)
		}
		target := strings.TrimSpace(args[0])
		if target == "" {
			out.usageError("rm: empty target")
			os.Exit(2)
		}
		if err := app.RemoveRoute(target); err != nil {
			out.commandError("rm", err)
			os.Exit(1)
		}
		out.ok("Route removed", map[string]any{"host": target})

	case "ls":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd ls")
			return
		}
		if out.opts.JSON {
			out.usageError("ls: JSON output is not supported for this command yet")
			os.Exit(2)
		}
		if err := app.ListRoutes(); err != nil {
			out.commandError("ls", err)
			os.Exit(1)
		}

	case "apply":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd apply")
			return
		}
		if err := app.Apply(); err != nil {
			out.commandError("apply", err)
			os.Exit(1)
		}
		out.ok("Configuration applied to Caddy", nil)

	case "tls":
		if len(args) < 1 || isHelpToken(args[0]) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd tls mkcert [--install] [--cert-file <path>] [--key-file <path>]")
			return
		}
		sub := args[0]
		switch sub {
		case "mkcert":
			if hasHelpToken(args[1:]) {
				fmt.Fprintln(os.Stdout, "Usage:\n  switchd tls mkcert [--install] [--cert-file <path>] [--key-file <path>]")
				return
			}
			fs := newFlagSet("tls mkcert")
			install := fs.Bool("install", false, "run mkcert -install before generating certs")
			certFile := fs.String("cert-file", "", "output certificate file path (default: ~/.config/switchboard-hub/tls-cert.pem)")
			keyFile := fs.String("key-file", "", "output key file path (default: ~/.config/switchboard-hub/tls-key.pem)")
			if err := fs.Parse(args[1:]); err != nil {
				out.usageError("tls mkcert: " + err.Error())
				os.Exit(2)
			}
			if err := app.TLSMkcert(*certFile, *keyFile, *install); err != nil {
				out.commandError("tls mkcert", err)
				os.Exit(1)
			}
		default:
			out.usageError("tls: unknown subcommand " + sub)
			os.Exit(2)
		}

	case "open":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd open <name-or-host> [--scheme http|https]")
			return
		}
		fs := newFlagSet("open")
		scheme := fs.String("scheme", "", "URL scheme (http or https; defaults to https when TLS is enabled)")
		pos, err := parseInterspersedFlags(fs, args)
		if err != nil {
			out.usageError("open: " + err.Error())
			os.Exit(2)
		}
		if len(pos) != 1 {
			out.usageError("open: provide exactly one <name-or-host>")
			os.Exit(2)
		}
		if err := app.Open(pos[0], *scheme); err != nil {
			out.commandError("open", err)
			os.Exit(1)
		}

	case "uninstall":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd uninstall")
			return
		}
		if err := app.Uninstall(); err != nil {
			out.commandError("uninstall", err)
			os.Exit(1)
		}
		out.ok("Uninstall complete", nil)

	case "status":
		if hasHelpToken(args) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd status")
			return
		}
		if out.opts.JSON {
			out.usageError("status: JSON output is not supported for this command yet")
			os.Exit(2)
		}
		if err := app.Status(); err != nil {
			out.commandError("status", err)
			os.Exit(1)
		}

	case "caddy":
		if len(args) < 1 || isHelpToken(args[0]) {
			fmt.Fprintln(os.Stdout, "Usage:\n  switchd caddy run")
			return
		}
		sub := args[0]
		switch sub {
		case "run":
			if err := app.CaddyRun(); err != nil {
				out.commandError("caddy run", err)
				os.Exit(1)
			}
		default:
			out.usageError("caddy: unknown subcommand " + sub)
			os.Exit(2)
		}

	default:
		out.usageError("unknown command: " + cmd)
		usage()
		os.Exit(2)
	}
}
