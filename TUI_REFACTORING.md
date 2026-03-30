# TUI Refactoring Plan

This document is the execution tracker for the CLI TUI/UX refactor.

It is meant to be actionable. Work should proceed by phases, and within each phase by the first unchecked item that is not blocked.

If you later point to this document and say "work on the next available thing", the expected behavior is:

1. Read this file.
2. Find the first phase with unchecked tasks.
3. Within that phase, pick the first unchecked task whose dependencies are complete.
4. Implement it, update this document, and move to the next item.
5. If every checkbox in every phase is complete, explicitly say there is nothing left to work on.

## Goals

Deliver a consistent CLI UI system where:

- `service log` keeps its auto-TUI behavior
- `status` keeps its opt-in TUI behavior
- `app ls`, `stack plan|up|down|status`, and `service status` gain first-class TUI support
- plain-text output remains available and improves in consistency
- JSON output remains authoritative and machine-safe
- setup prompts behave consistently, including `--non-interactive`
- shared TUI components replace one-off screen logic

## Scope

### In scope

- Shared TUI primitives and layout system
- TUI support for:
  - `app ls`
  - `stack plan`
  - `stack up`
  - `stack down`
  - `stack status`
  - `service status`
- Plain renderer cleanup for stack and service status
- Prompt behavior cleanup for `tunnel init`, `service install`, `service start`
- TUI routing tests and data-contract tests

### Out of scope

- Full TUI coverage for every command
- Full-screen TUI for:
  - `ls`
  - `version`
  - `open`
  - `stack env`
  - `add/rm`
  - `tls mkcert`
- Major business-logic redesign beyond what is needed for rendering and interaction

## Current State

### Already implemented

- [x] Global `--ui auto|plain|tui` flag
- [x] TTY-aware UI gating infrastructure
- [x] Bubble Tea TUI for `service log`
- [x] Bubble Tea TUI for `status`
- [x] Lip Gloss styling for text-mode output
- [x] Huh-based prompts for missing `service.env` values
- [x] JSON support for `service log`
- [x] JSON support for `status`
- [x] JSON support for `ls`

### Still missing

- [x] TUI for `app ls`
- [x] Shared stack TUI for `stack plan|up|down|status`
- [x] TUI for `service status`
- [x] Shared TUI chrome/helpers to reduce duplicated layout logic
- [x] Prompt-policy cleanup so `tunnel init --non-interactive` suppresses interactive `service.env` prompting
- [x] Plain renderer cleanup for stack and service status
- [x] Better TUI-specific routing and behavior test coverage

## UI Mode Policy

This is the intended policy unless a later task explicitly changes it.

- `service log`
  `--ui=auto` should enable the TUI on interactive terminals.
- `status`
  `--ui=tui` should be required for the TUI.
- `app ls`
  `--ui=tui` should be required for the TUI.
- `stack plan|up|down|status`
  `--ui=tui` should be required for the TUI.
- `service status`
  `--ui=tui` should be required for the TUI.

Rules:

- JSON mode always disables TUI.
- Non-interactive terminals must never enter a TUI.
- `--ui=plain` must force plain output.

## Architecture Rules

These rules apply to every phase.

- Prefer data-first rendering:
  app layer returns reports or events; command layer chooses JSON, plain text, or TUI.
- Do not duplicate business logic in Bubble Tea models.
- Keep JSON contracts stable.
- Prefer adding shared helpers over growing one-off renderers.
- Keep non-interactive behavior safe and predictable.

## File Map

Primary files involved in this plan:

- [cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go)
- [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go)
- [cmd/switchd/styles.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/styles.go)
- [cmd/switchd/ui_support.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_support.go)
- [cmd/switchd/prompt.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/prompt.go)
- [internal/app/status.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/status.go)
- [internal/app/background_service.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service.go)
- [cmd/switchd/main_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main_test.go)
- [internal/app/background_service_test.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/internal/app/background_service_test.go)

Optional split targets if `cmd/switchd/tui.go` becomes too large:

- [cmd/switchd/tui_app_list.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui_app_list.go)
- [cmd/switchd/tui_stack.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui_stack.go)
- [cmd/switchd/tui_service.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui_service.go)
- [cmd/switchd/ui_models.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_models.go)

## How To Use This Plan

When working this plan:

- Mark completed tasks with `[x]`.
- If a task is intentionally skipped, replace it with a short note explaining why.
- If a task is blocked, add a `Blocked:` note directly beneath it.
- Keep tasks in order unless there is a strong reason to reorder them.
- Do not mark a phase complete until its acceptance criteria are met.

## Phase 1: Shared TUI Foundation

### Objective

Reduce duplication before adding more interactive surfaces.

### Tasks

- [x] Expand [cmd/switchd/styles.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/styles.go) with shared styles for:
  - panels/containers
  - footer/help bars
  - semantic chips/badges
  - selected rows or emphasis states
  - loading, empty, and error states
- [x] Refactor existing `service log` and `status` TUI rendering to use shared style helpers instead of inlined layout fragments.
- [x] Add reusable TUI chrome helpers in [cmd/switchd/tui.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/tui.go) or split files for:
  - titles/headers
  - footer/help text
  - viewport sizing
  - empty/loading/error panels
  - semantic status rendering
- [x] Add new UI gating helpers in [cmd/switchd/ui_support.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/ui_support.go):
  - `wantsTUIForAppList()`
  - `wantsTUIForStack()`
  - `wantsTUIForServiceStatus()`
- [x] Add or update tests covering the new gating helpers and any changed UI-mode rules.

### Acceptance Criteria

- Existing `service log` and `status` TUIs still work.
- Shared style/chrome helpers exist and are reused.
- New commands can hook into centralized UI gating instead of duplicating logic.
- `go test ./...` passes.

## Phase 2: `app ls` TUI

### Objective

Ship the next highest-value TUI using already-available command-layer data.

### Tasks

- [x] Extract app-list view-model creation from [AppLsCmd.Run in cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go).
- [x] Introduce a stable internal view model for app list rows and tunnel-health summaries.
- [x] Add `runAppListTUI(...)` in the TUI layer.
- [x] Implement app list TUI features:
  - scrollable list or table
  - clear app status / tunnel-health presentation
  - empty state
  - error state
  - filter or search by app name
  - detail view or expanded panel for selected app
- [x] Update `AppLsCmd.Run` routing:
  - JSON path unchanged
  - `--ui=tui` enters TUI when interactive
  - plain output remains the default otherwise
- [x] Add CLI tests for:
  - `--ui=tui` routing
  - JSON bypassing TUI
  - non-interactive `--ui=tui` failure
- [x] Add helper/view-model tests for app/tunnel-health rendering decisions.

### Acceptance Criteria

- `switchd app ls --ui=tui` works interactively.
- JSON output is unchanged.
- Plain output still works.
- `go test ./...` passes.

## Phase 3: Stack TUI

### Objective

Replace the current mixed plain rendering path for stack reports with a shared model that supports both plain and TUI output.

### Tasks

- [x] Refactor [renderStackReport() in cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go) into:
  - stack report view-model builder
  - plain renderer
  - TUI renderer entrypoint
- [x] Preserve the existing JSON contract for stack commands.
- [x] Implement a shared stack TUI for:
  - `stack plan`
  - `stack up`
  - `stack down`
  - `stack status`
- [x] Make the TUI show:
  - service rows
  - drift
  - actions
  - session state
  - collisions
  - orphans
  - command context (`plan`, `up`, `down`, `status`)
- [x] Add keyboard affordances for navigation and detail viewing.
- [x] Update stack command routing to use `--ui=tui`.
- [x] Add tests for:
  - stack routing
  - stack plain renderer
  - JSON regressions
  - collision/orphan/drift rendering helpers

### Acceptance Criteria

- `stack plan|up|down|status --ui=tui` all work.
- Plain and JSON paths still work.
- Stack-specific rendering no longer depends on a single mixed inline formatter.
- `go test ./...` passes.

## Phase 4: `service status` TUI

### Objective

Replace the remaining inline `fmt.Printf` service-status renderer with structured plain/TUI rendering.

### Tasks

- [x] Extract `service status` plain rendering from [ServiceStatusCmd.Run in cmd/switchd/main.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/main.go).
- [x] Introduce a reusable service-status plain renderer.
- [x] Add `runServiceStatusTUI(...)` in the TUI layer.
- [x] Implement service-status TUI sections for:
  - lifecycle status
  - launchd phase
  - pid/caddy pid
  - env readiness
  - paths
  - stale/error state
  - actionable next steps
- [x] Route `service status --ui=tui` to the TUI when interactive.
- [x] Add tests for:
  - JSON regression
  - plain rendering behavior
  - TUI routing behavior

### Acceptance Criteria

- `switchd service status --ui=tui` works.
- Plain service-status output no longer depends on one large inline formatter.
- JSON output remains unchanged.
- `go test ./...` passes.

## Phase 5: Prompt Policy Cleanup

### Objective

Make interactive prompting respect explicit non-interactive intent.

### Tasks

- [x] Extend [cmd/switchd/prompt.go](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/cmd/switchd/prompt.go) so prompt eligibility can be controlled by command policy, not just TTY and JSON state.
- [x] Update `maybeCollectMissingServiceEnv(...)` to accept or derive prompt-policy input.
- [x] Ensure `tunnel init --non-interactive` does not trigger `service.env` prompts.
- [x] Preserve prompting for `service install` and `service start` when interactive and allowed.
- [x] Improve prompt copy/grouping where useful without changing persistence semantics.
- [x] Add tests proving prompt suppression and prompt execution in the correct cases.

### Acceptance Criteria

- `tunnel init --non-interactive` never prompts for `service.env`.
- `service install` and `service start` still prompt when appropriate.
- `go test ./...` passes.

## Phase 6: Plain Output Cleanup

### Objective

Finish the UX work by aligning the plain-text layer with the new structured rendering model.

### Tasks

- [x] Improve plain stack rendering so it uses the new stack view model cleanly.
- [x] Improve plain `service status` rendering so it uses shared styles/helpers.
- [x] Evaluate the legacy plain `ls` path and, if practical, move it from `app.ListRoutes()` to command-layer rendering based on structured route data.
- [x] Reduce remaining one-off `fmt.Printf` blocks in major user-facing summary commands where shared rendering is now available.
- [x] Update [DEVELOPMENT_TUI.md](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/DEVELOPMENT_TUI.md) if the architecture or file layout changed during the refactor.

### Acceptance Criteria

- Major dashboard-like commands share the same rendering vocabulary in plain mode.
- Legacy direct-print paths are reduced where it matters most.
- Documentation matches the resulting architecture.
- `go test ./...` passes.

## Testing Matrix

At minimum, the following commands should be verified across their relevant modes:

- [x] `service log`
- [x] `status`
- [x] `app ls`
- [x] `stack plan`
- [x] `stack status`
- [x] `service status`

For each relevant command, verify:

- [x] default interactive behavior
- [x] `--ui=plain`
- [x] `--ui=tui`
- [x] `--json`

Additional checks:

- [x] non-interactive `--ui=tui` fails clearly
- [x] JSON never enters TUI
- [x] prompt suppression works for `tunnel init --non-interactive`

## Milestones

- [x] Milestone 1: Shared TUI foundation complete
- [x] Milestone 2: `app ls` TUI complete
- [x] Milestone 3: Stack TUI complete
- [x] Milestone 4: `service status` TUI complete
- [x] Milestone 5: Prompt-policy cleanup complete
- [x] Milestone 6: Plain output cleanup and docs complete

## Definition Of Done

This refactor is complete when all of the following are true:

- [x] `service log` supports its intended TUI mode
- [x] `status` supports its intended TUI mode
- [x] `app ls` supports its intended TUI mode
- [x] `stack plan|up|down|status` support their intended TUI mode
- [x] `service status` supports its intended TUI mode
- [x] JSON output remains stable and tested
- [x] non-interactive behavior is safe and predictable
- [x] `tunnel init --non-interactive` does not trigger interactive prompts
- [x] shared UI components are used instead of repeated one-off layout code
- [x] major plain renderers no longer depend on scattered inline formatting blocks
- [x] [DEVELOPMENT_TUI.md](/Users/goliatone/Development/GO/src/github.com/goliatone/switchboard-hub/DEVELOPMENT_TUI.md) matches reality
- [x] `go test ./...` passes

When every checkbox above is marked complete, there is nothing left to work on for this refactor.
