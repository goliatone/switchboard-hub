# switchboard-hub (`switchd`)

`switchd` is a local development CLI for macOS.

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

`switchd caddy run` starts Caddy in the foreground. Keep it running in a terminal.

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

Start Caddy:

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
```

## Daily use (apps + tunnels for OAuth)

Create app:

```bash
./build/switchd app create esign --port 3000
```

Initialize provider (Cloudflare API mode example):

```bash
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
- `app oauth enable|print --provider <provider>`
- `tunnel providers|init|status`
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

- Make sure `switchd caddy run` is running.
- Confirm Caddy admin is reachable at `http://127.0.0.1:2019`.

If `*.test` does not resolve:

- restart `dnsmasq`
- flush macOS DNS cache
- verify `/etc/resolver/test` exists

If tunnel commands fail:

- run `switchd tunnel providers`
- run `switchd tunnel status`
- re-run `switchd tunnel init --provider <name>`

## Uninstall

Remove local DNS/resolver setup:

```bash
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
