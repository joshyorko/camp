# CLI Composition

## Current boundary

The executable delegates process I/O and exit status to `internal/cli`. The production tree exposes `init`, `open`, `sync`, `close`, `reopen`, and `recover` through concrete application and adapter composition, alongside deterministic help, global `--json`, and completion generation. A command is not real-tool lifecycle proof until the pinned tools complete it without skips.

When adding commands, compose existing application use cases and adapters instead of moving lifecycle logic into Cobra handlers. Preserve exact argument arrays through typed ports; do not rebuild shell command strings.

Production composition is not only command registration. The `internal/adapters/lifecycle` package now supplies the production `app.CloseEffects`, live `SessionEvidence` observer, and post-publication serving refresher seams. Command composition must still provide those adapters and typed propagation of DevPod and IDE options through `app.OpenRequest`. Target entry must preserve the canonical `target.Resolver` → effective DevPod workspace root → `workspace.MapTarget` chain. A Cobra command that calls only package fakes or journal state is not a usable command.

`lifecycle.CloseEffects` acts only on identities recorded in the session snapshot. Workspace cleanup distinguishes delete from `--keep-workspace` stop, forwarders and the supervisor use full PID/boot/start identities, and PID reuse means the recorded process is already absent rather than authorizing a signal to the new occupant. Services delegate to `supervisor.ServiceSupervisor.Stop`, which stops each recorded child before its helper and then proves absence; lease release uses the exact recorded revision, and materialization cleanup delegates to the ownership marker/device/inode guard. The production-composition regression in `internal/adapters/lifecycle/lifecycle_test.go` wires those concrete types and asserts child → helper → absence for both services. `lifecycle.SessionObserver` reports PID reuse explicitly and accepts live service evidence only after the existing supervisor inspector validates process topology and listeners; stopped active sessions still require listener-absence validation, while closed historical sessions with absent recorded processes do not claim ports legitimately reused later. `lifecycle.ServingRefresher` requires exactly one request-matching `ServingContentRefreshed` intent when pending work exists, tolerates unrelated concurrent intents without consuming them, validates both complete production service specs before stopping either service, and requires the recorded absolute Hauler command prefix `store --store <absolute-store> serve <service-name>` for `registry` or `fileserver`. Registry refresh retains the recorded mutable overlay so post-seal pushes remain available to the next checkpoint; fileserver refresh moves to the published haul directory. Journal fact commits serialize per session: lease renewal updates only lease state, checkpoint facts retain the latest durable lease, and service-changing `ServiceStart`, `ServiceRestart`, and `ServingContentRefreshed` facts update service records atop the latest snapshot. Recovery facts that reuse a service transition without changing service records retain their state transition. `CheckpointPublisher` reloads that composed snapshot and records only its matching refresh fact, so lease renewal and every intermediate refreshed identity fact cannot replace each other while unrelated pending intents remain owned by their writers. Refresh remains post-publication and an error remains an operational refresh failure rather than a publication rollback.

Unknown commands and arbitrary arguments return the stable usage exit code 2. Human failures use stderr; JSON failures use stdout and the versioned presentation envelope. Command handlers must obtain the inherited mode through `cli.OutputModeFrom` so success and failure presentation do not invent separate flag plumbing.

Cobra's help flag and generated `help` command bypass normal positional validation. Keep strict help-path validation at the `cli.Execute` boundary: unavailable or unknown help topics, extra topic components, and garbage combined with `--help` must exit 2 in both human and JSON modes without emitting successful help. Valid root and completion help must remain available.

Help preflight must normalize explicit values for both `--json` and `--help`/`-h` using every boolean spelling accepted by `strconv.ParseBool`, preserve final-flag-wins behavior, preserve explicit false, and stop interpreting flags after `--`; raw flag presence is not parsed state. Wrap Cobra flag-parse failures as typed usage errors at `FlagErrorFunc`, and classify only typed exit errors afterward. Never infer usage from message fragments such as `requires` or `accepts`, because application failures legitimately use those words.

The regression test must keep `camp open` nonzero until a real `open` handler is registered with production dependencies. Adding a command name to help or completion without that composition would turn a truthful rejection into a fake lifecycle surface.

## Configuration and provider boundary

Camp-owned user configuration persists only the typed non-secret fields in `config.Persistent`. Updates take an adjacent exclusive lock, write a mode-0600 temporary file, fsync it, rename it over the destination, and fsync the parent directory. URL userinfo and credential-shaped query parameters are rejected before effects. `CAMP_ACCESS_TOKEN` remains runtime-only; the legacy `accessToken` YAML field is rejected.

The first-run persistent path is one explicit command:

```bash
camp init \
  --source /absolute/capsule/root \
  --backend file:///absolute/camp/backend \
  --capsule capsule-name \
  --devpod-provider docker
```

These four flags are one contract: all must be present and nonempty, and a positional root cannot be combined with `--source`. After capsule initialization succeeds, Camp writes `source`, `backend`, `defaultCapsule`, and `devpodProvider` to the canonical `XDG_CONFIG_HOME/camp/config.yaml` path using `config.Store`; existing registry and fileserver port choices are retained. Human and JSON success output name that exact path and all four values. A later `camp open` resolves the persisted provider, with `CAMP_DEVPOD_PROVIDER` remaining an explicit runtime override. Provider credentials and provider option values are not part of this file.

Provider reads must redact values using DevPod's option schema: any option marked `password: true` is redacted even when its name is innocuous. A missing provider reader is a composition error and must return a clear error instead of panicking. Provider mutation currently fails unsupported before reader or persistence effects. The pinned DevPod `pkg/config.SaveConfig` implementation at commit `86b6f9f5` writes the live file with `os.WriteFile` and provides neither locking nor temp-file/fsync/rename publication, so delegating `provider set-options` would not satisfy Camp's atomic durability contract.

Before recording `WorkspaceUp`, production open lists providers in the requested DevPod context. An absent supported local `docker` provider is added noninteractively with `provider add docker --context <context> --use`; an existing non-default `docker` provider is configured with `provider use docker --context <context> --reconfigure`. Camp then re-lists and requires the exact provider identity to be default. DevPod's `--devcontainer-path` is capsule-relative even though Camp retains the validated canonical absolute path in recovery state; passing the absolute path makes DevPod join the workspace root twice.

## Proof commands

```bash
go build ./cmd/camp
go run ./cmd/camp --help
go run ./cmd/camp completion bash
go test ./cmd/camp ./internal/cli -count=1
```

A command is usable only when its handler is wired to production dependencies and a focused test proves its output/error contract. A help entry alone is not lifecycle proof.

## Evidence

- `cmd/camp/main.go`
- `internal/app/open.go`, `close.go`, `checkpoint.go`, and `operations.go`
- `internal/adapters/lifecycle/`
- `internal/target/` and `internal/workspace/`
- `internal/adapters/devpod/client_test.go`
- `internal/adapters/hauler/client_test.go`
- `internal/config/store.go`, `bootstrap.go`, and their focused tests
- pinned DevPod `cmd/provider/options.go`, `cmd/provider/set_options.go`, and `pkg/config/config.go` at `86b6f9f5`
