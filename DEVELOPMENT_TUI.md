# TUI Development Guide

This document explains how the `switchd` CLI UI layer works today, where the Bubble Tea and Charmbracelet integration lives, and how to extend it without breaking JSON output, non-interactive usage, or command semantics.

The intent is practical:

- help you find the right files quickly
- explain the current architecture and control flow
- show how to add or improve TUI/UX for more commands
- call out the constraints that matter for CLI compatibility

## Goals

The current CLI UI model has three modes:

- `--json` or `--output json`: machine-readable output only
- text mode: stable human-readable output for pipes, scripts, CI, and non-interactive terminals
- TUI mode: richer interactive UX for supported commands on interactive terminals

The preferred design principle is:

- internal packages produce data and events
- the command layer decides whether to render as JSON, plain text, or TUI

That separation is what makes it possible to improve the look and feel without duplicating business logic.

Important reality check:

- the main dashboard-like commands now follow this pattern end to end
- a few lower-value commands still use simple direct printing because they do not justify a full shared renderer yet

Examples of still-legacy paths include:

- `version` still uses direct line-by-line `fmt.Printf`
- `stack env` still prints raw `KEY=value` lines directly
- `oauth print` still emits the provider block directly
- `internal/app/status.go` still contains the older `Status()` text renderer, although the CLI now uses `StatusReportInfo()` plus command-layer rendering

## Files To Know

Start here:

- [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
  Main command definitions, global flags, output helpers, and routing from command execution into JSON, text, or TUI.
- [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go)
  Bubble Tea models and rendering for the current interactive UIs.
- [cmd/switchd/ui_models.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_models.go)
  Shared view-model builders for app, stack, and other data-first TUI/plain renderers.
- [cmd/switchd/text_output.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/text_output.go)
  Shared plain-text rendering helpers and the command-layer summary renderers.
- [cmd/switchd/styles.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/styles.go)
  Lip Gloss style palette used by text output and TUI views.
- [cmd/switchd/ui_support.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_support.go)
  TTY detection and UI mode gating.
- [cmd/switchd/prompt.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/prompt.go)
  Interactive prompt flows using `huh`.
- [internal/app/status.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/status.go)
  Structured status report used by both JSON and human rendering.
- [internal/app/background_service.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service.go)
  Structured service log streaming, NDJSON output, event sinks, and `service.env` write helpers.
- [cmd/switchd/main_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main_test.go)
  CLI-level behavior tests, including JSON mode and UI flag normalization.
- [internal/app/background_service_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service_test.go)
  App-layer tests for log streaming, NDJSON, follow mode, file recreation, and env persistence.

## Current Dependencies

The UI stack is currently:

- `bubbletea` for event-driven TUIs
- `bubbles/viewport` for scrollable content
- `lipgloss` for styling and layout
- `huh` for interactive forms

See [go.mod](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/go.mod).

## Command-Layer Architecture

### Global UI Flags

Global output mode is defined in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go):

- `--json`
- `--output text|json`
- `--ui auto|plain|tui`

Important rules:

- JSON mode wins. If `--json` or `--output json` is enabled, no TUI should run.
- `--ui=tui` requires an interactive terminal.
- `--ui=auto` lets supported commands choose a TUI when stdin and stdout are TTYs.
- `--ui=plain` forces normal text output even on interactive terminals.

The normalization logic is in:

- [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- [cmd/switchd/ui_support.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_support.go)

### `runContext` And `cliOutput`

The command layer centers on `runContext` and `cliOutput` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go).

`runContext` carries:

- the Kong parser
- the configured output mode
- the API client

`cliOutput` is the shared rendering surface for non-TUI output:

- `jsonOut`
- `ok`
- `info`
- `warn`
- `commandError`
- `printTable`

If you want to improve default CLI polish without going full-screen, this is the main place to work.

### Decision Pattern For Commands

Supported commands follow this pattern:

1. Build or fetch structured data.
2. If JSON mode is enabled, emit JSON and return.
3. If a TUI is supported and allowed, run the Bubble Tea program.
4. Otherwise render plain text.

Good examples:

- `status` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- `service log` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- `app ls` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- `stack plan|up|down|status` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- `service status` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)

This is the model to copy for future commands.

## Current Interactive Surfaces

### `switchd service log`

The interactive log viewer is implemented in [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go).

Behavior:

- auto-opens on interactive terminals in `--ui=auto`
- disabled in JSON mode
- reads live log events from the app layer through `EventSink`
- uses a `viewport` for scrolling
- runs in the alt screen

Current keys:

- `q` or `ctrl+c`: quit
- `p`: pause / resume auto-scroll
- `f`: toggle follow mode
- `s`: cycle `stdout -> stderr -> all`
- `c`: clear visible buffer

Important implementation detail:

- the TUI does not parse log files itself
- it calls `app.ServiceLogWithContext(...)` and receives structured `ServiceLogEvent` values

That split is deliberate. Keep the file watching, log ordering, and NDJSON contract in the app layer.

### `switchd status`

The interactive status dashboard is also in [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go).

Behavior:

- opt-in only with `--ui=tui`
- disabled in JSON mode
- refreshes every 5 seconds
- uses `StatusReportInfo()` as the data source
- uses a scrollable viewport

Current keys:

- `q` or `ctrl+c`: quit
- `r`: refresh immediately

Current rendering is intentionally simple. If you want to make it feel more like a dashboard, this is the safest place to push layout, density, section treatment, and richer status badges.

### `switchd app ls`

The app list dashboard is in [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go) and uses the shared app list view model from [cmd/switchd/ui_models.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_models.go).

Behavior:

- opt-in with `--ui=tui`
- disabled in JSON mode
- plain mode and TUI both consume the same `appListViewModel`
- includes filtering and a detail panel

Current keys:

- `q` or `ctrl+c`: quit
- `j/k` or arrows: move selection
- `/`: focus filter input
- `esc`: clear filter / blur filter

### `switchd stack plan|up|down|status`

The stack dashboards are implemented in [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go) and all consume the same `stackReportViewModel` from [cmd/switchd/ui_models.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_models.go).

Behavior:

- opt-in with `--ui=tui`
- disabled in JSON mode
- plain mode and TUI both derive from the same stack view model
- one renderer supports `plan`, `up`, `down`, and `status`

The plain summary for these commands now lives in [cmd/switchd/text_output.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/text_output.go), not inline in `main.go`.

### `switchd service status`

The service-status dashboard is also implemented in [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go), while the plain summary now lives in [cmd/switchd/text_output.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/text_output.go).

Behavior:

- opt-in with `--ui=tui`
- disabled in JSON mode
- plain mode and TUI both use the same `LaunchdServiceStatus` data source

### Interactive Prompts

Missing background-service environment values are collected through [cmd/switchd/prompt.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/prompt.go).

Used by:

- `tunnel init`
- `service install`
- `service start`

Behavior:

- only runs in interactive, non-JSON mode
- first asks the app layer for required/missing env vars
- prompts the user
- persists values into `service.env`
- re-runs environment preparation to return the updated report
- respects explicit command policy, so commands can suppress prompting even on an interactive terminal

Important detail:

- `tunnel init --non-interactive` now suppresses the `service.env` prompt too

If you want better setup flows, this is the place to improve sequencing, wording, grouping, help text, confirmation screens, and validation behavior.

## App-Layer Data Flow

### Status

Structured status data comes from [internal/app/status.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/status.go).

Key types:

- `StatusReport`
- `StatusTLSReport`
- `StatusCheckReport`
- `StatusAppReport`
- `StatusTunnelHealthItem`

The important point is that the main `switchd status` path is data-first now. The CLI can render the same report as:

- JSON
- plain text
- TUI

If you want to improve status UX, prefer expanding `StatusReport` and then updating both renderers instead of scraping output text or duplicating status probes in the TUI.

One legacy detail still exists:

- [internal/app/status.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/status.go) still contains the older `Status()` text renderer
- the CLI command uses `StatusReportInfo()` plus `renderStatusReport(...)` / `runStatusTUI(...)`, which is the path new work should follow

### Service Logs

Structured log streaming comes from [internal/app/background_service.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service.go).

Key types and functions:

- `ServiceLogOptions`
- `ServiceLogEvent`
- `ServiceLog(...)`
- `ServiceLogWithContext(...)`

Important capabilities:

- snapshot mode
- follow mode
- `stdout|stderr|all`
- compact NDJSON in JSON mode
- per-line event streaming via `EventSink`

That makes the app layer the correct place to add:

- timestamps
- richer event metadata
- log severity classification
- filtering hooks
- future search/indexing support

Do not re-implement file polling inside Bubble Tea unless you intentionally want a second, separate log source.

### Background Service Environment

The `service.env` workflow also lives in [internal/app/background_service.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service.go).

Key functions:

- `PrepareServiceEnvironment()`
- `SaveServiceEnvValues(...)`

Responsibilities:

- discover required env vars
- create or update template files
- persist entered values safely
- preserve existing comments and unrelated entries

If you want to build multi-step setup wizards, keep business rules here and keep prompt screens in `cmd/switchd`.

## Styling System

### Shared Style Palette

All current human-facing polish comes through `cliStyles` in [cmd/switchd/styles.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/styles.go).

This is the current palette contract:

- `title`
- `section`
- `muted`
- `key`
- `helpKey`
- `helpLabel`
- `panel`
- `panelTitle`
- `footer`
- `selected`
- `loading`
- `empty`
- `errorPanel`
- `tableHeader`
- `tableBorder`
- `badgeOK`
- `badgeInfo`
- `badgeWarn`
- `badgeErr`
- `chipOK`
- `chipInfo`
- `chipWarn`
- `chipErr`
- `chipDim`
- `statusOK`
- `statusWarn`
- `statusErr`
- `statusDim`

There are two style modes:

- interactive terminals: colorized styles
- non-interactive terminals: plain/bold fallbacks

That fallback matters. Do not assume ANSI color is always available.

### Plain Text Output

The non-TUI CLI uses Lip Gloss too. See:

- `textEvent(...)` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- `printTable(...)` in [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- `printSection(...)`, `printFields(...)`, `printStatusLine(...)`, and the shared summary renderers in [cmd/switchd/text_output.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/text_output.go)

This is a good place to improve:

- table spacing and density
- badge treatment
- section separation
- line wrapping behavior
- semantic coloring

If a command does not need interactivity, prefer improving this layer before introducing a full-screen TUI.

## How To Add A New TUI Command

Use this sequence.

### 1. Make The Command Data-First

Before you build UI:

- move direct `fmt.Printf` style output out of internal packages
- return a structured report or event stream from `internal/app` or `pkg/switchboard`
- keep JSON support first-class

Good targets for this pattern are any commands that currently emit long plain-text summaries or still call into direct-print helpers.

### 2. Add UI Gating In The Command

In the command `Run` method:

1. fetch structured data
2. handle JSON mode
3. check `wantsTUIFor...()`
4. run a Bubble Tea program if allowed
5. otherwise use plain rendering

If the command supports auto-TUI only on interactive terminals, follow `service log`.

If the command should require explicit opt-in, follow `status`.

### 3. Put The Bubble Tea Model In `cmd/switchd/tui.go`

Current convention:

- one model struct per interactive surface
- `Init`, `Update`, `View`
- small helper commands for async work

Add new model types and a `runXxxTUI(...)` helper there unless the file becomes too large. If it grows too much, split by feature while keeping all TUI code under `cmd/switchd`.

### 4. Keep The App Layer As The Source Of Truth

The TUI should consume:

- reports
- commands
- event streams

The TUI should not own:

- config mutation rules
- probe logic
- log tailing logic
- environment discovery logic

That boundary keeps JSON, text, and TUI behavior aligned.

### 5. Keep JSON Behavior Stable

Any new TUI-capable command must still work correctly with:

- `--json`
- `--output json`
- non-interactive stdout

If the command is stream-oriented, prefer NDJSON instead of pretty multi-line JSON envelopes.

## How To Improve Existing TUI UX

### `service log`

Best improvement areas:

- separate visual treatment for `stdout` vs `stderr`
- sticky header with current stream and follow state
- search
- filtering by substring or severity
- copy/export shortcuts
- status line with line counts and log file paths
- better empty and error states
- soft wrapping vs horizontal truncation strategy

If you add richer log metadata, do it by extending `ServiceLogEvent`.

### `status`

Best improvement areas:

- multi-column layout on wide terminals
- stronger section hierarchy
- semantic badges for `ok`, `warning`, `error`, `ready`, `not-ready`
- denser tunnel-health rendering
- better surfacing of actionable next steps
- panels for apps, service health, DNS, Caddy, and TLS
- keyboard navigation between sections

If you add new data points, add them to `StatusReport` first.

### Setup / Prompt Flows

Best improvement areas:

- group related env vars into logical sections
- provide examples and validation hints
- confirm before writing secrets
- show a final summary with next steps
- allow skipping optional values cleanly
- add provider-specific descriptions for Cloudflare or other providers

This should happen in [cmd/switchd/prompt.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/prompt.go) while keeping persistence in the app layer.

## Recommended Next Targets

The current high-value interactive surfaces are already in place. The next improvements are mostly quality and consistency work:

- richer search/filter/export behavior for `service log`
- denser multi-column layout and better hierarchy for `status`
- sorting, filter chips, and faster detail navigation for `app ls`
- more explicit action/drift/collision affordances for stack dashboards
- better prompt copy, validation, and completion summaries for setup flows

For new commands, first make sure the app layer exposes a report shape suitable for JSON and plain rendering.

## Testing Strategy

### CLI-Level Tests

Use [cmd/switchd/main_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main_test.go) for:

- global flag normalization
- JSON vs text routing
- command-level wiring
- output contracts
- fallback behavior when UI is disabled

`runCLIForTest(...)` is the main helper there.

### App-Layer Tests

Use [internal/app/background_service_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service_test.go) and related app tests for:

- structured report generation
- NDJSON framing
- follow-mode behavior
- log file recreation / rotation behavior
- env-file persistence semantics

### What To Test When Changing UI

When you change or add a TUI-capable command, test at least:

- JSON mode still works
- non-interactive text mode still works
- TUI gating logic is correct
- invalid flag combinations fail clearly
- the app-layer report or event source has deterministic tests

For styling-only changes, targeted CLI output tests are usually enough.

Today there is stronger automated coverage for:

- JSON routing and CLI wiring
- service log streaming and NDJSON behavior
- `service.env` persistence
- command-level TUI routing for `service log`, `status`, `app ls`, stack dashboards, and `service status`

There is currently less direct automated coverage for:

- full TUI rendering behavior
- detailed TUI interaction state transitions
- prompt UX copy and flow details

## Common Gotchas

### 1. Don’t Break JSON For Human UX

This is the biggest rule.

- JSON output must remain machine-readable.
- TUI should never activate in JSON mode.
- stream commands should keep one event per line in NDJSON mode if they run continuously.

### 2. Don’t Put Business Logic In Bubble Tea

Keep the TUI thin. The more stateful or domain-specific logic you put into `Update`, the harder it becomes to maintain feature parity across JSON, text, and TUI.

### 3. Be Careful With Interactive Detection

UI and prompts must respect non-interactive execution. The helpers in [cmd/switchd/ui_support.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_support.go) exist to keep this centralized.

### 4. Keep Long-Running Streams Cancelable

For streaming TUIs, always use context-aware app-layer functions like `ServiceLogWithContext(...)` so quitting the TUI stops background work cleanly.

### 5. Don’t Assume ANSI Everywhere

The style layer already has interactive and non-interactive branches. Preserve that pattern when adding colors or layout tricks.

### 6. Keep Tests Focused On Contracts

Avoid snapshotting full TUI screens unless the rendering is intentionally stable. It is better to test:

- command routing
- event flow
- structured outputs
- helper functions

than to lock down every line of terminal layout.

## Suggested Workflow For UI Changes

1. Start from the command entrypoint in `cmd/switchd/main.go`.
2. Identify whether the app layer already returns enough structure.
3. If not, add a report or event type in `internal/app`.
4. Preserve or add JSON output.
5. Improve plain text rendering through `cliOutput` if that is enough.
6. If interaction is valuable, add a Bubble Tea model in `cmd/switchd/tui.go`.
7. Add tests for routing and data contracts.
8. Run `go test ./...`.

## Quick Reference

Where to change colors and badges:

- [cmd/switchd/styles.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/styles.go)

Where to change text-mode tables and events:

- [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- [cmd/switchd/text_output.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/text_output.go)

Where to change TUI layouts:

- [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go)
- [cmd/switchd/ui_models.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_models.go)

Where to change TUI enablement rules:

- [cmd/switchd/ui_support.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_support.go)

Where to change prompt UX:

- [cmd/switchd/prompt.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/prompt.go)

Where to change status data:

- [internal/app/status.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/status.go)

Where to change log event streaming:

- [internal/app/background_service.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service.go)

Where to verify CLI behavior:

- [cmd/switchd/main_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main_test.go)

Where to verify log/event/env behavior:

- [internal/app/background_service_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service_test.go)
