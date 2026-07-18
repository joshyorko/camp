# CLI Composition

## Current boundary

The executable is intentionally a root-only Cobra command. There is no production command composition for the target `open`, `sync`, `close`, `attach`, `status`, `list`, `doctor`, or `recover` experience yet. Do not use package-level application tests as evidence that those commands exist.

When adding commands, compose existing application use cases and adapters instead of moving lifecycle logic into Cobra handlers. Preserve exact argument arrays through typed ports; do not rebuild shell command strings.

Production composition is not only command registration. The `internal/adapters/lifecycle` package now supplies the production `app.CloseEffects`, live `SessionEvidence` observer, and post-publication serving refresher seams. Command composition must still provide those adapters and typed propagation of DevPod and IDE options through `app.OpenRequest`. Target entry must preserve the canonical `target.Resolver` → effective DevPod workspace root → `workspace.MapTarget` chain. A Cobra command that calls only package fakes or journal state is not a usable command.

`lifecycle.CloseEffects` acts only on identities recorded in the session snapshot. Workspace cleanup distinguishes delete from `--keep-workspace` stop, forwarders and the supervisor use full PID/boot/start identities, and PID reuse means the recorded process is already absent rather than authorizing a signal to the new occupant. Services delegate to `supervisor.ServiceSupervisor.Stop`, which stops each recorded child before its helper and then proves absence; lease release uses the exact recorded revision, and materialization cleanup delegates to the ownership marker/device/inode guard. The production-composition regression in `internal/adapters/lifecycle/lifecycle_test.go` wires those concrete types and asserts child → helper → absence for both services. `lifecycle.SessionObserver` reports PID reuse explicitly and accepts live service evidence only after the existing supervisor inspector validates process topology and listeners; stopped active sessions still require listener-absence validation, while closed historical sessions with absent recorded processes do not claim ports legitimately reused later. `lifecycle.ServingRefresher` requires exactly one request-matching `ServingContentRefreshed` intent when pending work exists, tolerates unrelated concurrent intents without consuming them, validates both complete production service specs before stopping either service, and requires the recorded absolute Hauler command prefix `store --store <absolute-store> serve <service-name>` for `registry` or `fileserver`. Registry refresh retains the recorded mutable overlay so post-seal pushes remain available to the next checkpoint; fileserver refresh moves to the published haul directory. Journal fact commits serialize per session: lease renewal updates only lease state, checkpoint facts retain the latest durable lease, and serving refresh updates only service records. `CheckpointPublisher` reloads that composed snapshot and records only its matching refresh fact, so lease renewal and refreshed identities cannot replace each other while unrelated pending intents remain owned by their writers. Refresh remains post-publication and an error remains an operational refresh failure rather than a publication rollback.

Unknown commands and arbitrary arguments must return a nonzero error. The current root-only command accepts `camp open` and an unknown token as positional arguments and exits successfully; tests for the real tree must lock down arity, stderr, and stable exit codes.

## Configuration and provider boundary

Camp-owned user configuration persists only the typed non-secret fields in `config.Persistent`. Updates take an adjacent exclusive lock, write a mode-0600 temporary file, fsync it, rename it over the destination, and fsync the parent directory. URL userinfo and credential-shaped query parameters are rejected before effects. `CAMP_ACCESS_TOKEN` remains runtime-only; the legacy `accessToken` YAML field is rejected.

Provider reads must redact values using DevPod's option schema: any option marked `password: true` is redacted even when its name is innocuous. A missing provider reader is a composition error and must return a clear error instead of panicking. Provider mutation currently fails unsupported before reader or persistence effects. The pinned DevPod `pkg/config.SaveConfig` implementation at commit `86b6f9f5` writes the live file with `os.WriteFile` and provides neither locking nor temp-file/fsync/rename publication, so delegating `provider set-options` would not satisfy Camp's atomic durability contract.

## Proof commands

```bash
go build ./cmd/camp
go run ./cmd/camp --help
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
