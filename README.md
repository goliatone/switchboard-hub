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

When an app does not set `dial_host`, `switchd` defaults to auto loopback resolution:

- prefer `127.0.0.1` when reachable
- otherwise try `::1`
- fall back to `127.0.0.1` if neither is reachable yet

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

Homebrew release installs come from the `goliatone/homebrew-tap` tap:

```bash
brew tap goliatone/homebrew-tap
brew install --cask switchd
switchd version
```

Homebrew installs provide the canonical `switchd` command only.

Linux release packages (`deb` and `rpm`) install `switchd` and add the `sbd` symlink automatically:

```bash
switchd version
sbd version
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

Use an explicit upstream host when your local server only binds to IPv6 or a non-default interface:

```bash
./build/switchd add my-app --port 3000 --dial-host ::1
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

Set an explicit dial host when needed:

```bash
./build/switchd app create esign --port 3000 --dial-host ::1
```

If `--dial-host` is omitted, `switchd` stores `dial_host: auto` behavior implicitly and resolves the best loopback host at runtime.

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

`app ls` shows tunnel state in the `TUNNEL` column:

- `none`: no public endpoint configured
- `idle KO`: endpoint exists but no active runtime session
- `active OK`: endpoint exists and provider reports ready
- `active KO`: session metadata exists but provider health is failing
- `active ?` / `idle ?`: provider health could not be queried

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
	if err := client.CreateApp("demo", 3000, &switchboard.CreateAppOptions{}); err != nil {
		log.Fatal(err)
	}

	report, err := client.StackPlan("./stack.yaml")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("planned services: %d", len(report.Services))
}
```

### Isolated ingress library contracts

`Options.Ingress` opts into tunnel-only composition with explicit absolute
`ConfigPath`, `StateDir`, and `RuntimeDir` paths. It requires no Switchboard CLI,
Switchboard service, Caddy, or dnsmasq. Nil retains legacy composition; unsafe
unowned cleanup now returns a recovery error instead of guessing ownership.

The default isolated store supplies `CoordinatedIngressStore`; an injected store
must implement that optional capability. Providers implement `OwnedIngressProvider`.
`CredentialRef` stores an opaque reference; supplying one also requires
`IngressOptions.Credentials` and `PrivateIngressInitializer`. Resolver errors and
provider errors do not expose credential values, and supplied credential buffers
are cleared after each operation. Provider implementations must not retain them.

`IngressSpec.Binding` selects the HTTPS port/path and optionally asserts an owned
endpoint ID and expected origin. An endpoint ID alone cannot adopt a resource.
`IngressOptions.Helpers` and `HelperResolver` configure/discover provider helpers
without running them during discovery. Providers validate their supported binding
and helper capabilities before mutation.

The default coordinator holds a cancellable cross-process lock across each
mutation, records private durable progress, and atomically replaces config with
file/directory sync. An enrollment marker binds the canonical config to its state
directory and permanently excludes legacy config paths. Use dedicated quiescent
state; old binaries/manual writers cannot be fenced by advisory locks. Back up
config, enrollment marker and journals together. Do not remove markers to bypass
recovery or silently move the state directory.

Retry an interrupted operation with the same owner, intent and original expected
revision. `IngressRecoveryProvider` inspects the exact recorded attempt before
retrying uncertain effects and verifies recovered session identity. Ambiguity or
conflicting edits fail safely. A recovered start settles to stopped, requiring a
fresh explicit start; recovery never restores exposure automatically.

The built-in Tailscale owned adapter requires explicit provider settings
`local_api_socket` (an accessible Unix socket) and `coordination_dir` (one shared
private directory per daemon across stores). Select an unused HTTPS port 443,
8443 or 10000 and root path `/`. It uses a foreground watch and conditional
LocalAPI updates; Stop/Release preserve other routes and refuse changed bindings.
For an existing macOS GUI backend, explicitly set `local_api_transport: macos-gui`,
omit `local_api_socket`, and supply an absolute trusted Tailscale CLI through
`IngressOptions.Helpers["tailscale"]`. The initial tested pair is Homebrew CLI
1.104.7 with GUI backend 1.104.4; other versions fail with setup guidance. The
Unix mode remains the default (`local_api_transport: unix` is also accepted).
GUI discovery captures `debug local-creds` through a bounded private pipe; it
never executes the printed curl command, persists its token, starts a daemon or
falls back to persistent CLI exposure. Requests use authenticated IPv4 loopback
without proxies or redirects. Every store for the same GUI user must share one
private coordination directory; the lock is independent of helper path and port.
Authentication/identity changes fail safely; create a fresh client for explicit
recovery, never automatically restore exposure.

A read-only installation probe is available (no Serve/Funnel mutations):

```sh
SWITCHBOARD_TAILSCALE_GUI_PROBE=/opt/homebrew/bin/tailscale \
  go test -tags=provider_live ./internal/tunnel/providers/tailscale \
    -run '^TestLiveGUIReadOnlyTransport$' -count=1 -v
```

Passing this probe does not prove live A/B exposure or cleanup; the external
observer and dedicated-route T08 checks remain required. Legacy unowned Tailscale
Start/Stop refuse mutations; migrate explicitly instead of using global reset.

Cloudflare owned lifecycle requires dedicated resources and an explicitly enrolled
single writer. Configure provider `values` with `ownership_mode: single-writer`,
`authority_id`, `account_id`, `zone_id`, a shared private `coordination_dir`, and an
absolute `supervisor_path`. All stores on the authority host must share that
coordination directory. Exclude dashboard, Terraform, legacy and other-host
writers for enrolled resources; a scoped API token alone does not enforce this.
Cloudflare does not document atomic conditional deletion. Observed drift refuses
cleanup, but concurrent out-of-band changes during read/delete are unsupported.

Build the per-session supervisor with
`go build -o /absolute/path/switchboard-connector ./cmd/switchboard-connector`.
Ship/pin it alongside cloudflared (2025.4.0+ with token-file support). This helper
owns one child, has no provider API credential, and exits when its host pipe
closes. Recovered callers contact its private socket; they never signal a PID.
Keep the private runtime path short enough for a Unix socket (103 bytes maximum
including the generated socket filename). Missing/mismatched supervisors or
completion evidence preserve ambiguous processes and artifacts for recovery.
Killing the supervisor itself with SIGKILL cannot prove child exit; do not delete
its state or token file to bypass that conflict.

Supply the API credential through `CredentialRef` and the host resolver, never
provider values or process arguments. The connector gets an exclusive 0600 token
file in a 0700 runtime directory and a minimal environment. Stop retains the DNS
record/tunnel; Release verifies creation provenance and current snapshots, deletes
owned DNS then tunnel without force, and retains progress on partial failure.
Lost create replies reconcile the recorded single-writer attempt; ambiguous
absence never triggers a blind second creation. Unowned collisions are refused.
File deletion is not token revocation. Legacy API startup is outside this secure
owned path, and legacy PID-only cleanup remains refused.

Real-provider validation and an immutable adoption pin remain required. Offline
contract and recovery checks are:

```sh
go test ./pkg/switchboard -run 'Test(OwnedIngressLibraryLifecycleWithoutCLIOrCaddy|DefaultIsolatedStoreRecoversThroughFacade)'
go test ./internal/app -run TestT05
go test ./internal/config -run TestIngressStore
go test -race ./internal/tunnel/providers/tailscale -run TestOwned
go test -race ./internal/tunnel/providers/cloudflare -run 'Test(CredentialFile|SecureConnectorCommand|Stop)'
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
- `caddy run`
- `status`
- `version`
- `help [path...]`
- `uninstall`

Top-level global flags:

- `--debug`
- `--verbose`
- `--quiet`
- `--json`
- `--output text|json`

Per-command help:

```bash
./build/switchd help app
./build/switchd help tunnel init
```

Command options:

- `switchd init [--tld test] [--dns-ip 10.0.0.1] [--tls] [--tls-mode internal|file] [--tls-cert-file <pem>] [--tls-key-file <pem>]`
- `switchd add --port <port> [--host <fqdn>] [--dial-host <host>] <name-or-host>`
- `switchd rm <name-or-host>`
- `switchd ls`
- `switchd apply`
- `switchd open [--scheme http|https] <name-or-host>`
- `switchd app create --port <port> [--dial-host <host>] <name-or-host>`
- `switchd app rm <name>`
- `switchd app ls`
- `switchd app expose [--provider <provider>] [--public-host <fqdn>] <name>`
- `switchd app up <name>`
- `switchd app down <name>`
- `switchd app oauth enable --provider google --callback-path <path> <name>`
- `switchd app oauth print --provider google <name>`
- `switchd stack plan -f <file>`
- `switchd stack up -f <file>`
- `switchd stack down -f <file>`
- `switchd stack status -f <file>`
- `switchd stack env -f <file>`
- `switchd tunnel providers`
- `switchd tunnel init --provider <provider> [--mode api|cli] [--setup] [--non-interactive] [--origincert <path>] [--account-id <id>] [--zone-id <id>] [--base-domain <fqdn>] [--api-token-env <env-var>]`
- `switchd tunnel status [--provider <provider>]`
- `switchd service install`
- `switchd service start`
- `switchd service stop`
- `switchd service status`
- `switchd service log [--lines <n>] [--follow|--no-follow] [--stream stdout|stderr|all]`
- `switchd service uninstall`
- `switchd tls mkcert [--install] [--cert-file <path>] [--key-file <path>]`
- `switchd caddy run`
- `switchd status`
- `switchd version`
- `switchd help [path...]`
- `switchd uninstall`

`--dial-host` is the upstream bind host that Caddy and tunnel connectors should target. Use it for servers that bind to `::1`, `localhost`, a Docker bridge IP, or another non-default interface.

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

App entries support `dial_host`:

```yaml
apps:
  - name: esign
    local_host: esign.test
    local_port: 3000
    dial_host: ::1
```

If `dial_host` is omitted, `switchd` auto-resolves loopback reachability at runtime and writes the effective dial target into generated Caddy routes.

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

### Dedicated provider validation (T08)

Run the offline fault and observer checks from the repository root. They use
controlled provider fixtures, temporary state, and loopback HTTPS. They cover
ownership and revision conflicts, owner loss and restart recovery, drift,
partial release, credential cleanup, and observer failure classification.

```sh
go test -race -count=1 ./tools/validation/... ./pkg/switchboard ./internal/app ./internal/tunnel/providers/...
```

The opt-in harness runs three normal A/B lifecycle repetitions, continuously
checks B, and tests wrong-owner, stale-revision and canceled-operation refusal.
It uses a tiny loopback HTTP test origin; no camera or media service is involved.
It does not establish the remaining fault/restart or immutable adoption gates.

For the live proof, reserve the following:

| Fixture | Setup |
| --- | --- |
| Cloudflare A/B | Two different unused public names in one Cloudflare-managed zone, both on port 443 and covered by an edge certificate. Leave their DNS records and tunnels unprovisioned so the harness can prove creation and release. Supply the Cloudflare account and zone IDs and a token scoped to that account's Cloudflare Tunnel Edit and that zone's DNS Edit. Install pinned cloudflared with `--token-file` support and build the candidate's `switchboard-connector`. Select one writer and one shared coordination directory for both scopes. [Cloudflare API setup](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel-api/) |
| Tailscale A/B | One dedicated running node with its exact `.ts.net` FQDN, MagicDNS, HTTPS certificates and the Funnel node attribute. Reserve two unused HTTPS ports from 443, 8443 and 10000. Keep the third port available when a separate private Serve control is needed. Use the accessible Unix LocalAPI socket or the supported macOS GUI setup above. [Tailscale Funnel requirements](https://tailscale.com/docs/features/tailscale-funnel) |
| Observers | Run the probe from an Internet-connected machine outside the tailnet and test-host network. If using SSH, verify the dedicated alias and host key during setup. For the private Serve visibility case, also verify reachability from a tailnet-authorized client. |
| Fault and restart fixture | Use an isolated VM/test machine or an approved maintenance window where Tailscale daemon restart and host reboot are allowed. Keep state across reboots. Reserve disposable routes for operator drift/collision and a controlled permission-denial/retry case. Unrelated production routes stay outside the test. |

The isolated host can run both providers. The external probe needs a separate
network vantage. Record provider-side ownership and cleanup alongside public
observations. For Cloudflare, an authenticated provider view can confirm zero
connector sessions after Stop and removal of the exact owned DNS record and
tunnel after Release. Cloudflare error 1033 indicates no healthy connector.
For Tailscale, capture the effective route and public/private visibility before
and after the case. Keep B reachable while observing A. Correlate provider and
external evidence when the generic probe reports inconclusive.
[Cloudflare Tunnel troubleshooting](https://developers.cloudflare.com/tunnel/troubleshooting/)

Create an owner-only (0600) JSON file containing `provider`, a fresh absolute
`run_dir` below a private existing parent, `helper_path`, `probe_path`,
`probe_network`, `host_a`, `host_b`, `port_a`, `port_b`,
`dedicated_resources: true`, and provider `values` from the setup above.
Cloudflare also requires `credential_env` naming the environment variable that
contains the private API credential. Never put its value in JSON. Use distinct
unused Cloudflare hostnames on port 443. Tailscale uses the test node's same
hostname with two distinct otherwise unused supported ports. Do not use existing
application endpoints or run restart cases on a shared production host.

`probe_path` is an explicitly selected executable that performs the request from
outside the tailnet/local network. It receives URL and expected response text as
two arguments. Exit 0 means exact expected response, 3 means independently
confirmed absence of that response, and all other exits mean inconclusive/error.
The observer must distinguish service absence from observer/network failure.
Probe output is discarded; its executable hash and declared network are recorded.
The harness never manufactures external reachability from a local HTTP check.

The bundled `ingress-observer` makes a direct request, verifies TLS, and
does not follow redirects. It reports exact HTTP 200 responses as present and
verified HTTP 404 or 410 responses without the fixture marker as absent.
DNS, TLS, transport, redirect, unexpected status/body and timeout failures
are inconclusive. An endpoint that disappears without a verified HTTP response
requires additional observation before a live absence result can pass.

Build the probe for the external machine's OS and architecture:

```sh
go build -o ingress-observer ./tools/validation/cmd/ingress-observer
```

For an already configured SSH observer, the local `probe_path` wrapper can
send the two values on stdin to a fixed remote executable. Replace the host
alias and installed path for the dedicated observer. The values are not part
of the remote command. Record the installed observer binary's SHA-256 with
the run evidence when using a wrapper.

```sh
#!/bin/sh
printf '%s\n%s\n' "$1" "$2" |
  /usr/bin/ssh -T -o BatchMode=yes -o StrictHostKeyChecking=yes \
    t08-observer /absolute/path/ingress-observer --stdin
```

Run the local preflight while arranging the live credentials and observer. It
validates the JSON, helper file metadata, fresh run path and applicable local
transport files. It never starts a helper, reads the API token, connects to the
observer or contacts a provider.

```sh
SWITCHBOARD_INGRESS_VALIDATION=/absolute/private/validation.json \
  go test -tags=provider_live ./tools/validation -run '^TestLiveOwnedIngressPreflight$' -count=1 -v
```

Use short private paths for Cloudflare, such as a fresh run below
`/private/tmp/sb-t08` on macOS, to leave room for the connector's Unix socket.
Once the fixtures are authorized for live use and the Cloudflare token is
available via the named environment variable, run the provider check.

```sh
SWITCHBOARD_INGRESS_VALIDATION=/absolute/private/validation.json \
  go test -tags=provider_live ./tools/validation -run '^TestLiveOwnedIngress$' -count=1 -v
```

Missing setup fails before provider contact. Reused run directories are refused;
failed attempts, journals and cleanup responsibility remain intact. Inspect the
private `run_dir/evidence.json` and use the recorded owner/intent/revision for
recovery rather than deleting state or blindly rerunning. The report binds the
working-source fingerprints and helper/probe hashes and always leaves
`t08_complete` false: real crash/drift/permission/visibility/restart observations,
actual ctx checks and a maintainer-selected immutable module pin must be reconciled
separately. Passing this subset does not authorize Camstream T01.
