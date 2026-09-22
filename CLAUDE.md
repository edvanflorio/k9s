# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

K9s is a terminal UI (tview/tcell) for observing and managing Kubernetes clusters. It continuously
watches the cluster through shared informers and renders resources as navigable tables.

## Commands

```shell
make build                 # builds ./execs/k9s (injects version/commit/date ldflags)
make test                  # go clean --testcache && go test ./...
make lint                  # golangci-lint (auto-installs v2.13.2 if missing)
make cover                 # coverage profile + HTML report
make imgx / make pushx     # multi-arch docker buildx image

go test ./internal/render/ -run TestDpRender        # single test
go test ./internal/dao/... -run TestPod -v          # single package, verbose
go test ./internal/render/ -bench BenchmarkDpRender # renderers have benchmarks
```

Running the built binary against a real cluster: `./execs/k9s -l debug` and tail the log
(`k9s info` prints its location; `K9S_LOGS_DIR` or `--logFile` override it). The TUI takes over the
terminal, so prefer `k9s info`/`k9s version` and the log file over interactive runs when verifying.

## Architecture

The core abstraction is `*client.GVR` (`internal/client/gvr.go`) — an interned, pointer-identity
value for `group/version/resource:subresource`. GVRs are **compared and map-keyed by pointer**, so
always obtain them via `client.NewGVR(...)` (cached) or the predefined vars in
`internal/client/gvrs.go` (`client.PodGVR`, `client.DpGVR`, …). Never construct a `GVR` literal.

Data flows in layers, bottom-up:

- `internal/client` — kubeconfig/flags, REST + dynamic + metrics clients, RBAC `can` checks.
- `internal/watch` — `Factory` wrapping per-namespace `DynamicSharedInformerFactory` caches, plus
  port-forward tracking. All list/get in the hot path reads from informer caches, not the API server.
- `internal/dao` — one accessor per resource type implementing narrow capability interfaces from
  `dao/types.go` (`Accessor`, `Describer`, `Nuker`, `Scalable`, `Loggable`, `Restartable`,
  `Switchable`, `ContainsPodSpec`, …). Views feature-detect capabilities with type assertions rather
  than a fat interface. `dao.Resource` (informer-backed) and `dao.Generic`/`dao.Scaler` (dynamic
  client fallback) are the base types most accessors embed.
- `internal/model1` — pure tabular primitives: `Header`, `Row`, `Rows`, `TableData`, `RowEvent`
  (add/update/delete deltas), and the `Renderer` interface.
- `internal/render` — one renderer per resource turning a `runtime.Object` into a `model1.Row` plus
  a `Header` and a `ColorerFunc`. Resources with no dedicated renderer fall back to the server-side
  `metav1.Table` via `render.Generic`.
- `internal/model` — stateful components (`Table`, `Log`, `Tree`, `Text`, `Stack`, `Pulse`) that own
  a refresh loop, diff old vs. new rows into `RowEvent`s, and fire listener callbacks.
- `internal/ui` — generic tview widgets (`App`, `Table`, `Pages`, `Menu`, `Prompt`, `KeyActions`,
  dialogs) with no Kubernetes knowledge.
- `internal/view` — Kubernetes-aware viewers. `view.App` embeds `ui.App`; `view.Browser` embeds
  `view.Table` and is the default resource browser. Per-resource views (`view/pod.go`, `view/dp.go`,
  …) exist only to bind extra key actions and enter/context behavior.
- `internal/xray` — the tree/xray view's alternate renderers.

### Registration points for a resource

There are three separate registries; adding or changing a resource usually means touching more than one:

1. `internal/dao/accessor.go` — `accessors` map: GVR → DAO. Unregistered GVRs get `dao.Scaler`.
2. `internal/model/registry.go` — `model.Registry`: GVR → `ResourceMeta{DAO, Renderer, TreeRenderer}`.
   Split between "Custom" pseudo-resources (workloads, contexts, aliases, pulses, helm, rbac, scans,
   port-forwards, dirs) and real discovered API resources.
3. `internal/view/registrar.go` — `loadCustomViewers()`: GVR → `MetaViewer{viewerFn, enterFn}`.
   Absent here, `view.NewBrowser` is used.

`dao.MetaAccess` (`internal/dao/registry.go`) is the runtime discovery cache that maps user-typed
commands/short names/kinds to GVRs and records namespaced-ness and verbs.

### Command routing

`internal/view/cmd` parses what the user types in the prompt (`Interpreter`: resource, namespace,
label/field filters, context switch, `xray`, contexts, dirs). `internal/view/command.go` resolves
that to a GVR via aliases + `MetaAccess`, builds the component, and pushes it on the page stack.

### Config

`internal/config` loads XDG-based config (`internal/config/files.go` defines `AppConfigDir`,
`clusters/<context>` overrides, `skins/`, `plugins.yaml`, `aliases.yaml`, `hotkeys.yaml`,
`views.yaml`, `jumps.yaml`). Every YAML config is validated against an embedded JSON schema in
`internal/config/json/schemas/` — **when you change a config struct, update the matching schema**
and its `testdata` fixtures. `internal/config/templates/` holds the shipped defaults.

Top-level `plugins/`, `skins/`, and `jumps/` are community-contributed assets, not code; they are
documented in `README.md` and `plugins/README.md`. Plugin shortcuts must not collide with built-in
table keys unless the plugin sets `override: true` — see recent commits touching `plugins/`.

## Conventions

- Every Go file starts with the SPDX header:
  ```go
  // SPDX-License-Identifier: Apache-2.0
  // Copyright Authors of K9s
  ```
- Logging is `log/slog` only, enforced by `sloglint`: key-value pairs only, **no raw string keys** —
  use the constants in `internal/slogs/keys.go` (add one there if missing), camelCase naming.
- `depguard` bans `sirupsen/logrus` and `pkg/errors` (use stdlib `errors`).
- `gofmt` rewrite rule replaces `interface{}` with `any`.
- Tests use `stretchr/testify` (`require` for fatal, `assert` for checks) and live in `_test.go`
  files, mostly in external `package foo_test`. Fixtures are JSON/YAML under each package's
  `testdata/`; renderer tests load an object from `testdata/<name>.json` and assert on `Row.Fields`.
- Lint is strict (`funlen` 60 statements, `gocyclo` 35, `lll` 170, `gocritic` with all tags,
  `revive`, `gosec`); run `make lint` before considering a change done.
- `godox` flags `FIXME` (but not `TODO`/`BOZO`, which appear throughout as maintainer notes).
