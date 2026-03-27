# switchboard-hub (`switchd`)

`switchd` is a local development CLI for macOS.

It supports both the original imperative app workflow and a declarative stack workflow backed by the same reusable Go API.

It sets up:

- wildcard local DNS for `*.test`
- Caddy reverse proxy routes to your local apps
- optional public tunnel endpoints for OAuth callbacks

## What it does

You can map local apps to stable hostnames.

Examples:

- `https://my-app.test` -> `http://127.0.0.1:3000`
- `https://api.my-app.test` -> `http://127.0.0.1:4000`

You can also expose an app through a tunnel provider:

- Cloudflare named tunnel
- Tailscale Funnel

## How it works

`switchd init` writes local machine config.

It configures:

- `dnsmasq` wildcard rules
- `/etc/resolver/test`
- loopback alias (default `10.0.0.1`)
- a local app config file at `~/.config/switchboard-hub/config.yaml`

`switchd apply` pushes routes from config to the Caddy admin API.

`switchd service install` installs a root `launchd` service that starts `switchd` in the background on boot.

`switchd caddy run` is still available as a foreground/manual debug path.

The background `launchd` service reads provider secrets from `~/.config/switchboard-hub/service.env`. It does not inherit variables you exported only in your interactive shell.

## Requirements

- macOS
- Homebrew
- `dnsmasq`
- `caddy`

Optional:

- `mkcert` for local cert files
- `cloudflared` for Cloudflare tunnel CLI mode
- `tailscale` for Tailscale tunnel mode

Install base tools:

```bash
brew install dnsmasq caddy
```

## Build

Build with embedded version metadata from `.version`:

```bash
./taskfile go:build ./build/switchd
```

Or plain Go build:

```bash
go build -o switchd ./cmd/switchd
```

## Release installs

Published releases install `switchd` as the canonical command.

Packaged installs also provide `sbd` as a short alias to the same binary:

```bash
switchd version
sbd version
```

Homebrew release installs come from the `goliatone/homebrew-tap` tap:

```bash
brew tap goliatone/homebrew-tap
brew install switchd
```

Linux release packages (`deb` and `rpm`) install `switchd` and add the `sbd` symlink automatically.

## First time setup (machine)

Run once with `sudo`:

```bash
sudo ./build/switchd init --tld test --dns-ip 10.0.0.1 --tls --tls-mode internal
```

Then run:

```bash
sudo brew services restart dnsmasq
sudo dscacheutil -flushcache
sudo killall -HUP mDNSResponder
```

If you use internal TLS mode, trust Caddy local CA once:

```bash
sudo caddy trust
```

Install and start the background service:

```bash
sudo ./build/switchd service install
```

If you use provider credentials for background resume, add them to `service.env` first:

```bash
cat >> ~/.config/switchboard-hub/service.env <<'EOF'
SWITCHD_CF_API_TOKEN=<cloudflare_api_token>
EOF
sudo ./build/switchd service install
```

Check service state:

```bash
./build/switchd service status
./build/switchd status
```

Manual/foreground alternative:

```bash
sudo ./build/switchd caddy run
```

## Daily use (local routes only)

Add a route:

```bash
./build/switchd add my-app --port 3000
```

List routes:

```bash
./build/switchd ls
```

Apply routes to Caddy:

```bash
./build/switchd apply
```

Open in browser:

```bash
./build/switchd open my-app
```

Check health:

```bash
./build/switchd status
./build/switchd service status
./build/switchd service log
./build/switchd service log --stream stderr
./build/switchd service log --no-follow --lines 200
```

## Daily use (apps + tunnels for OAuth)

Create app:

```bash
./build/switchd app create esign --port 3000
```

Initialize provider (Cloudflare API mode example):

```bash
cat >> ~/.config/switchboard-hub/service.env <<'EOF'
SWITCHD_CF_API_TOKEN=<cloudflare_api_token>
EOF
./build/switchd tunnel init \
  --provider cloudflare \
  --mode api \
  --account-id <account-id> \
  --zone-id <zone-id> \
  --base-domain tnl.example.com
```

Expose app publicly:

```bash
./build/switchd app expose esign --provider cloudflare
```

Configure Google callback path:

```bash
./build/switchd app oauth enable esign --provider google --callback-path /oauth/callback
./build/switchd app oauth print esign --provider google
```

Start tunnel runtime for the app:

```bash
./build/switchd app up esign
```

Stop tunnel runtime:

```bash
./build/switchd app down esign
```

List apps and provider state:

```bash
./build/switchd app ls
./build/switchd tunnel status
```

## Daily use (declarative stacks)

Stacks are a declarative layer over the existing app/tunnel model. Each stack service reconciles to a real app in `~/.config/switchboard-hub/config.yaml`.

Example stack file:

```yaml
version: 1
name: carina

defaults:
  provider: cloudflare
  expose: true
  up: true

services:
  - name: app
    local_port: 8383
    public_host: app.carina.getctx.com

  - name: simulator
    local_port: 8090
    public_host: carina.getctx.com

outputs:
  APP_HTTP__BASE_URL: "https://{{ service \"app\" \"public_host\" }}"
  APP_SHIM__BASE_URL: "https://{{ service \"simulator\" \"public_host\" }}"
  APP_SHIM__APP_TARGET_BASE_URL: "https://{{ service \"app\" \"public_host\" }}"
  APP_HTTP__TRUST_FORWARDED_HEADERS: "true"
  APP_ADMIN_AUTH__COOKIE_DOMAIN: "{{ parent_domain (service \"simulator\" \"public_host\") }}"
```

Preview and reconcile it:

```bash
./build/switchd stack plan -f ./stack.yaml
./build/switchd stack up -f ./stack.yaml
./build/switchd stack status -f ./stack.yaml
./build/switchd stack env -f ./stack.yaml
./build/switchd stack down -f ./stack.yaml
```

`stack env` is deterministic and side-effect free. It renders output variables from desired stack data only:

```bash
APP_HTTP__BASE_URL=https://app.carina.getctx.com
APP_SHIM__BASE_URL=https://carina.getctx.com
APP_SHIM__APP_TARGET_BASE_URL=https://app.carina.getctx.com
APP_HTTP__TRUST_FORWARDED_HEADERS=true
APP_ADMIN_AUTH__COOKIE_DOMAIN=getctx.com
```

Managed stack apps are identified with metadata (`managed_by=stack`, `stack`, `service`). `switchd` will not silently adopt unrelated existing apps if names or public hosts collide.

## Go API

Another Go module can import `github.com/goliatone/switchboard-hub/pkg/switchboard` instead of shelling out to `switchd`.

```go
package main

import (
	"log"

	"github.com/goliatone/switchboard-hub/pkg/switchboard"
)

func main() {
	client := switchboard.New(switchboard.Options{
		ConfigPath: "/tmp/switchboard/config.yaml",
	})

	if _, err := client.LoadOrCreateDefaultConfig(); err != nil {
		log.Fatal(err)
	}
	if err := client.CreateApp("demo", 3000); err != nil {
		log.Fatal(err)
	}

	report, err := client.StackPlan("./stack.yaml")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("planned services: %d", len(report.Services))
}
```

## Command reference

Show top-level help:

```bash
./build/switchd
./build/switchd --help
```

Main commands:

- `init`
- `add`, `rm`, `ls`, `apply`, `open`
- `app create|rm|ls|expose|up|down`
- `stack plan|up|down|status|env -f <file>`
- `app oauth enable|print --provider <provider>`
- `tunnel providers|init|status`
- `service install|log|start|stop|status|uninstall`
- `tls mkcert`
- `status`
- `version`
- `uninstall`

Per-command help:

```bash
./build/switchd help app
./build/switchd help tunnel init
```

## Output modes

Global flags:

- `--json`
- `--output text|json`
- `--quiet`
- `--verbose` (alias for `--debug`)

Example:

```bash
./build/switchd --json app ls
./build/switchd --json version
```

## Config file

Default path:

- `~/.config/switchboard-hub/config.yaml`

Override path with:

- `SWITCHD_CONFIG_PATH=/absolute/path/config.yaml`

## TLS notes

`init` does not rewrite TLS settings if config already exists.

Use one of these for later TLS changes:

- `./build/switchd tls mkcert --install`
- edit `~/.config/switchboard-hub/config.yaml` directly

## Troubleshooting

If `apply` fails:

- Make sure the background service is running: `sudo switchd service start`
- Or run the manual foreground path: `sudo switchd caddy run`
- Confirm Caddy admin is reachable at `http://127.0.0.1:2019`.

If `*.test` does not resolve:

- restart `dnsmasq`
- flush macOS DNS cache
- verify `/etc/resolver/test` exists

If tunnel commands fail:

- run `switchd tunnel providers`
- run `switchd tunnel status`
- if background resume needs credentials, add them to `~/.config/switchboard-hub/service.env` and re-run `sudo switchd service start`
- re-run `switchd tunnel init --provider <name>`

## Uninstall

Remove local DNS/resolver setup:

```bash
sudo ./build/switchd service uninstall
sudo ./build/switchd uninstall
sudo brew services restart dnsmasq
sudo dscacheutil -flushcache
sudo killall -HUP mDNSResponder
```

## Release

Validate release config:

```bash
go run github.com/goreleaser/goreleaser/v2@latest check
```

Build local release snapshot:

```bash
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```
