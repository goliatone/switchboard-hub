package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

func usage() {
	fmt.Fprintf(os.Stderr, `switchd - manage *.test local DNS + Caddy routes (switchboard-hub)

Usage:
  switchd [--debug] <command> ...
  switchd init [--tld test] [--dns-ip 10.0.0.1] [--tls] [--tls-mode internal|file] [--tls-cert-file <path>] [--tls-key-file <path>] # sudo
  switchd app create <name-or-host> --port <port>
  switchd app rm <name>
  switchd app ls
  switchd app expose <name> [--provider <provider>] [--public-host <fqdn>]
  switchd app up <name>
  switchd app down <name>
  switchd app oauth google enable <name> --callback-path <path>
  switchd app oauth google print <name>
  switchd tunnel providers
  switchd tunnel init --provider <provider> [--mode cli|api] [--setup] [--non-interactive] [--origincert <path>] [--account-id <id>] [--zone-id <id>] [--base-domain <domain>] [--api-token-env <ENV_NAME>]
  switchd tunnel status [--provider <provider>]
  switchd add <name-or-host> --port <port>
  switchd rm <name-or-host>
  switchd ls
  switchd apply
  switchd tls mkcert [--install] [--cert-file <path>] [--key-file <path>]
  switchd open <name-or-host> [--scheme http|https]
  switchd uninstall                                       # sudo
  switchd status
  switchd caddy run                                       # sudo (binds :80/:443)

Examples:
  sudo switchd init --tld test --dns-ip 10.0.0.1
  switchd app create esign --port 3000
  switchd tunnel init --provider cloudflare --mode api --account-id <id> --zone-id <id> --base-domain tnl.example.com
  switchd app expose esign --provider cloudflare
  switchd app oauth google enable esign --callback-path /admin/esign/integrations/google/callback
  switchd app oauth google print esign
  switchd add my-local-app --port 3030
  switchd add api.my-local-app.test --port 4040
  switchd apply
  switchd open my-local-app
`)
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

func extractGlobalFlags(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	debug := false
	for _, a := range args {
		switch a {
		case "--debug", "-debug":
			debug = true
		default:
			out = append(out, a)
		}
	}
	return out, debug
}

func printActionableErr(prefix string, err error) bool {
	details, ok := tunnel.ActionableFromError(err)
	if !ok {
		return false
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "error"
	}
	if strings.TrimSpace(details.Code) != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]\n", prefix, details.Code)
	} else {
		fmt.Fprintln(os.Stderr, prefix)
	}
	if strings.TrimSpace(details.What) != "" {
		fmt.Fprintln(os.Stderr, "what:", details.What)
	}
	if strings.TrimSpace(details.Why) != "" {
		fmt.Fprintln(os.Stderr, "why:", details.Why)
	}
	for _, check := range details.Checks {
		check = strings.TrimSpace(check)
		if check == "" {
			continue
		}
		fmt.Fprintln(os.Stderr, "check:", check)
	}
	for i, step := range details.NextSteps {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		fmt.Fprintf(os.Stderr, "do %d: %s\n", i+1, step)
	}
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		fmt.Fprintln(os.Stderr, "detail:", err)
	}
	return true
}

func main() {
	rawArgs, debug := extractGlobalFlags(os.Args[1:])
	diag.SetDebug(debug)

	if len(rawArgs) < 1 {
		usage()
		os.Exit(2)
	}

	cmd := rawArgs[0]
	args := rawArgs[1:]

	switch cmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		tld := fs.String("tld", "test", "development TLD to manage (e.g. test)")
		dnsIP := fs.String("dns-ip", "10.0.0.1", "loopback alias IP for dnsmasq (e.g. 10.0.0.1)")
		tlsEnabled := fs.Bool("tls", true, "enable HTTPS listeners in Caddy")
		tlsMode := fs.String("tls-mode", "", "TLS mode: internal or file (default: internal)")
		tlsCertFile := fs.String("tls-cert-file", "", "certificate file path (PEM), required when --tls-mode file")
		tlsKeyFile := fs.String("tls-key-file", "", "key file path (PEM), required when --tls-mode file")
		_ = fs.Parse(args)

		if err := app.Init(*tld, *dnsIP, *tlsEnabled, *tlsMode, *tlsCertFile, *tlsKeyFile); err != nil {
			fmt.Fprintln(os.Stderr, "init error:", err)
			os.Exit(1)
		}
		fmt.Println("init complete")

	case "app":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "app error: expected subcommand: create|rm|ls|expose|up|down|oauth")
			os.Exit(2)
		}
		sub := args[0]
		switch sub {
		case "create":
			fs := flag.NewFlagSet("app create", flag.ExitOnError)
			port := fs.Int("port", 0, "local port to proxy to (required)")
			pos, err := parseInterspersedFlags(fs, args[1:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "app create error:", err)
				os.Exit(2)
			}
			if *port <= 0 || *port > 65535 {
				fmt.Fprintln(os.Stderr, "app create error: --port is required and must be 1..65535")
				os.Exit(2)
			}
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "app create error: provide exactly one <name-or-host>")
				os.Exit(2)
			}
			if err := app.CreateApp(pos[0], *port); err != nil {
				fmt.Fprintln(os.Stderr, "app create error:", err)
				os.Exit(1)
			}
			fmt.Println("app created")

		case "rm":
			if len(args) != 2 {
				fmt.Fprintln(os.Stderr, "app rm error: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.RemoveApp(args[1]); err != nil {
				fmt.Fprintln(os.Stderr, "app rm error:", err)
				os.Exit(1)
			}
			fmt.Println("app removed")

		case "ls":
			apps, err := app.ListApps()
			if err != nil {
				fmt.Fprintln(os.Stderr, "app ls error:", err)
				os.Exit(1)
			}
			if len(apps) == 0 {
				fmt.Println("(no apps)")
				break
			}
			for _, a := range apps {
				extra := ""
				if strings.TrimSpace(a.PublicEndpoint.Host) != "" {
					extra += " public=" + a.PublicEndpoint.Host
				}
				if a.OAuth.Google.Enabled {
					extra += " google_oauth=enabled"
				}
				if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
					extra += " session=active"
				}
				fmt.Printf("%-16s %-32s -> 127.0.0.1:%d%s\n", a.Name, a.LocalHost, a.LocalPort, extra)
			}

		case "expose":
			fs := flag.NewFlagSet("app expose", flag.ExitOnError)
			provider := fs.String("provider", "", "tunnel provider (defaults to config tunnels.default_provider)")
			publicHost := fs.String("public-host", "", "public callback host (optional for cloudflare with base_domain configured)")
			pos, err := parseInterspersedFlags(fs, args[1:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "app expose error:", err)
				os.Exit(2)
			}
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "app expose error: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.ExposeApp(pos[0], *provider, *publicHost); err != nil {
				fmt.Fprintln(os.Stderr, "app expose error:", err)
				os.Exit(1)
			}
			fmt.Println("app exposed")

		case "up":
			if len(args) != 2 {
				fmt.Fprintln(os.Stderr, "app up error: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.AppUp(args[1]); err != nil {
				fmt.Fprintln(os.Stderr, "app up error:", err)
				os.Exit(1)
			}
			fmt.Println("app up complete")

		case "down":
			if len(args) != 2 {
				fmt.Fprintln(os.Stderr, "app down error: provide exactly one <name>")
				os.Exit(2)
			}
			if err := app.AppDown(args[1]); err != nil {
				fmt.Fprintln(os.Stderr, "app down error:", err)
				os.Exit(1)
			}
			fmt.Println("app down complete")

		case "oauth":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "app oauth error: expected provider subcommand: google")
				os.Exit(2)
			}
			if args[1] != "google" {
				fmt.Fprintln(os.Stderr, "app oauth error: unknown provider:", args[1])
				os.Exit(2)
			}
			if len(args) < 3 {
				fmt.Fprintln(os.Stderr, "app oauth google error: expected subcommand: enable|print")
				os.Exit(2)
			}
			switch args[2] {
			case "enable":
				fs := flag.NewFlagSet("app oauth google enable", flag.ExitOnError)
				callbackPath := fs.String("callback-path", "", "oauth callback path (required, must start with /)")
				pos, err := parseInterspersedFlags(fs, args[3:])
				if err != nil {
					fmt.Fprintln(os.Stderr, "app oauth google enable error:", err)
					os.Exit(2)
				}
				if len(pos) != 1 {
					fmt.Fprintln(os.Stderr, "app oauth google enable error: provide exactly one <name>")
					os.Exit(2)
				}
				if strings.TrimSpace(*callbackPath) == "" {
					fmt.Fprintln(os.Stderr, "app oauth google enable error: --callback-path is required")
					os.Exit(2)
				}
				if err := app.OAuthGoogleEnable(pos[0], *callbackPath); err != nil {
					fmt.Fprintln(os.Stderr, "app oauth google enable error:", err)
					os.Exit(1)
				}
				fmt.Println("google oauth enabled")

			case "print":
				if len(args) != 4 {
					fmt.Fprintln(os.Stderr, "app oauth google print error: provide exactly one <name>")
					os.Exit(2)
				}
				block, err := app.OAuthGooglePrint(args[3])
				if err != nil {
					fmt.Fprintln(os.Stderr, "app oauth google print error:", err)
					os.Exit(1)
				}
				fmt.Println(block)

			default:
				fmt.Fprintln(os.Stderr, "app oauth google error: unknown subcommand:", args[2])
				os.Exit(2)
			}

		default:
			fmt.Fprintln(os.Stderr, "app error: unknown subcommand:", sub)
			os.Exit(2)
		}

	case "tunnel":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "tunnel error: expected subcommand: providers|init|status")
			os.Exit(2)
		}
		sub := args[0]
		switch sub {
		case "providers":
			for _, p := range app.TunnelProviders() {
				fmt.Println(p)
			}

		case "init":
			fs := flag.NewFlagSet("tunnel init", flag.ExitOnError)
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
				fmt.Fprintln(os.Stderr, "tunnel init error:", err)
				os.Exit(2)
			}
			if strings.TrimSpace(*provider) == "" {
				fmt.Fprintln(os.Stderr, "tunnel init error: --provider is required")
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
				if !printActionableErr("tunnel init error", err) {
					fmt.Fprintln(os.Stderr, "tunnel init error:", err)
				}
				os.Exit(1)
			}
			fmt.Println("tunnel provider initialized")

		case "status":
			fs := flag.NewFlagSet("tunnel status", flag.ExitOnError)
			provider := fs.String("provider", "", "provider name (optional)")
			if err := fs.Parse(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "tunnel status error:", err)
				os.Exit(2)
			}
			statuses, err := app.TunnelStatus(*provider)
			if err != nil {
				fmt.Fprintln(os.Stderr, "tunnel status error:", err)
				os.Exit(1)
			}
			for _, st := range statuses {
				msg := "ok"
				if !st.Available {
					msg = "error: " + st.Err
				}
				fmt.Printf("%-12s enabled=%t oauth=%t %s\n", st.Name, st.Enabled, st.OAuthSuitable, msg)
				for _, note := range st.Notes {
					fmt.Printf("  note: %s\n", note)
				}
			}

		default:
			fmt.Fprintln(os.Stderr, "tunnel error: unknown subcommand:", sub)
			os.Exit(2)
		}

	case "add":
		fs := flag.NewFlagSet("add", flag.ExitOnError)
		port := fs.Int("port", 0, "local port to proxy to (required)")
		host := fs.String("host", "", "explicit host (overrides positional argument)")
		pos, err := parseInterspersedFlags(fs, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "add error:", err)
			os.Exit(2)
		}

		if *port <= 0 || *port > 65535 {
			fmt.Fprintln(os.Stderr, "add error: --port is required and must be 1..65535")
			os.Exit(2)
		}

		if len(pos) < 1 && *host == "" {
			fmt.Fprintln(os.Stderr, "add error: provide <name-or-host> or --host")
			os.Exit(2)
		}
		if *host != "" && len(pos) > 0 {
			fmt.Fprintln(os.Stderr, "add error: use either positional <name-or-host> or --host, not both")
			os.Exit(2)
		}
		if *host == "" && len(pos) != 1 {
			fmt.Fprintln(os.Stderr, "add error: provide exactly one <name-or-host>")
			os.Exit(2)
		}

		nameOrHost := ""
		if *host != "" {
			nameOrHost = *host
		} else {
			nameOrHost = pos[0]
		}

		if err := app.AddRoute(nameOrHost, *port); err != nil {
			fmt.Fprintln(os.Stderr, "add error:", err)
			os.Exit(1)
		}
		fmt.Println("route added")

	case "rm":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "rm error: provide exactly one <name-or-host>")
			os.Exit(2)
		}
		target := strings.TrimSpace(args[0])
		if target == "" {
			fmt.Fprintln(os.Stderr, "rm error: empty target")
			os.Exit(2)
		}
		if err := app.RemoveRoute(target); err != nil {
			fmt.Fprintln(os.Stderr, "rm error:", err)
			os.Exit(1)
		}
		fmt.Println("route removed")

	case "ls":
		if err := app.ListRoutes(); err != nil {
			fmt.Fprintln(os.Stderr, "ls error:", err)
			os.Exit(1)
		}

	case "apply":
		if err := app.Apply(); err != nil {
			fmt.Fprintln(os.Stderr, "apply error:", err)
			os.Exit(1)
		}
		fmt.Println("applied to Caddy")

	case "tls":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "tls error: expected subcommand: mkcert")
			os.Exit(2)
		}
		sub := args[0]
		switch sub {
		case "mkcert":
			fs := flag.NewFlagSet("tls mkcert", flag.ExitOnError)
			install := fs.Bool("install", false, "run mkcert -install before generating certs")
			certFile := fs.String("cert-file", "", "output certificate file path (default: ~/.config/switchboard-hub/tls-cert.pem)")
			keyFile := fs.String("key-file", "", "output key file path (default: ~/.config/switchboard-hub/tls-key.pem)")
			if err := fs.Parse(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "tls mkcert error:", err)
				os.Exit(2)
			}
			if err := app.TLSMkcert(*certFile, *keyFile, *install); err != nil {
				fmt.Fprintln(os.Stderr, "tls mkcert error:", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "tls error: unknown subcommand:", sub)
			os.Exit(2)
		}

	case "open":
		fs := flag.NewFlagSet("open", flag.ExitOnError)
		scheme := fs.String("scheme", "", "URL scheme (http or https; defaults to https when TLS is enabled)")
		pos, err := parseInterspersedFlags(fs, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open error:", err)
			os.Exit(2)
		}
		if len(pos) != 1 {
			fmt.Fprintln(os.Stderr, "open error: provide exactly one <name-or-host>")
			os.Exit(2)
		}
		if err := app.Open(pos[0], *scheme); err != nil {
			fmt.Fprintln(os.Stderr, "open error:", err)
			os.Exit(1)
		}

	case "uninstall":
		if err := app.Uninstall(); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall error:", err)
			os.Exit(1)
		}
		fmt.Println("uninstall complete")

	case "status":
		if err := app.Status(); err != nil {
			fmt.Fprintln(os.Stderr, "status error:", err)
			os.Exit(1)
		}

	case "caddy":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "caddy error: expected subcommand: run")
			os.Exit(2)
		}
		sub := args[0]
		switch sub {
		case "run":
			if err := app.CaddyRun(); err != nil {
				fmt.Fprintln(os.Stderr, "caddy run error:", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "caddy error: unknown subcommand:", sub)
			os.Exit(2)
		}

	case "-h", "--help", "help":
		usage()

	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		usage()
		os.Exit(2)
	}
}
