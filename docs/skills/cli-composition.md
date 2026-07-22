# CLI Composition

## Current boundary

The executable delegates process I/O and exit status to `internal/cli`. The production tree exposes `setup`, `init`, `open`, `attach`, `sync`, `close`, `reopen`, `recover`, and the read-only `doctor` command through concrete application and adapter composition, alongside deterministic help, global `--json`, and completion generation. `setup` composes the locked installer but does not execute a lifecycle; no command is real-tool lifecycle proof until the pinned tools complete it without skips. Issue #6 remains open while its other documented command groups are absent; registering only implemented production handlers keeps help and generated completions truthful.

`camp doctor` emits one versioned capability model in deterministic human or JSON form. Its stable statuses are `healthy`, `degraded`, `blocked`, and `skipped-not-configured`; `blocked` dominates the overall result, followed by `degraded`. A blocked aggregate is rendered exactly once and exits nonzero; it is not followed by a second generic failure envelope. Each probe is bounded independently. DevPod and Hauler are healthy only when the read-only managed-tool inspector proves the installed executable against the compiled lock; doctor never downloads or repairs them. Kernel evidence removes control bytes before its 256-byte display bound, and probe causes or credentials are never result evidence.

The functional backend probe creates one random `camp-doctor/` object, proves exact readback, conditional replacement, duplicate and stale-revision conflicts, and conditional cleanup, then verifies absence. Cleanup uses the recorded opaque revision; an identity conflict or cleanup failure blocks health. The functional pasta probe starts the Camp binary's private token listener behind a unique pasta loopback mapping, records exact helper and child process identities, requires a child network namespace distinct from `/proc/self/ns/net`, reaches the token through the host listener, stops only the recorded identities, proves listener/process absence, and removes the temporary directory only when its device and inode still match. `/proc/self/fd`, `/dev/net/tun`, user namespace creation, LSM context, and host/container boundary are independent results. Provider, workspace, forwarding, and service probes are `skipped-not-configured` when no corresponding configuration or live journal record exists; configured checks use read-only provider listing, exact workspace status, durable forwarder/process plus in-workspace HTTP evidence, and service process/namespace/listener/HTTP observation.

A doctor report is capability evidence, not installation, full DevPod/Room-of-Requirement lifecycle, checkpoint publication, Kubernetes, release, or deployment evidence. A `skipped-not-configured` result is intentionally not proof of the configured path.

When adding commands, compose existing application use cases and adapters instead of moving lifecycle logic into Cobra handlers. Preserve exact argument arrays through typed ports; do not rebuild shell command strings.

Production composition is not only command registration. The `internal/adapters/lifecycle` package now supplies the production `app.CloseEffects`, live `SessionEvidence` observer, and post-publication serving refresher seams. Command composition must still provide those adapters and typed propagation of DevPod and IDE options through `app.OpenRequest`. Target entry must preserve the canonical `target.Resolver` → effective DevPod workspace root → `workspace.MapTarget` chain. A Cobra command that calls only package fakes or journal state is not a usable command.

`camp attach [target]` composes the existing ownership-safe `app.Attach` use case with the production journal, ownership revalidator, canonical target resolver, and DevPod adapter. The Cobra boundary owns only typed flag parsing: the VS Code Insiders alias and DevPod SSH port, user, environment, agent, GPG, stdio, keepalive, signing-key, terminal, terminfo, and raw passthrough options. Typed/raw conflict detection remains in the DevPod adapter before process execution. Terminal attach must pass the process stdin, stdout, and stderr through `ports.Command`; capturing subprocess output without those destinations would make a registered interactive command unusable even if its argv were correct.

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
  --devpod-provider room-of-requirement \
  --devpod-context ror
```

The source, backend, capsule, and provider flags are one contract: all four must be present and nonempty, and a positional root cannot be combined with `--source`. `--devpod-context` is an optional typed part of that contract and defaults to `default`; it cannot be used by itself. Camp resolves the requested backend through the same strict `config.ResolveBackend` contract used by production open before creating Camp state, resolving DevPod, initializing the capsule, or publishing configuration. An S3 backend therefore requires effective endpoint, region, path-style, and insecure-policy values that pass the normal credential-free S3 checks.

After capsule initialization succeeds, one `config.Store.Modify` transaction holds the adjacent exclusive lock across read, mutation, validation, mode-0600 temporary-file publication, fsync, rename, and parent-directory fsync. It writes `source`, `backend`, `defaultCapsule`, `devpodProvider`, `devpodContext`, and the effective non-secret S3 settings while retaining existing registry and fileserver port choices; concurrent writers cannot overwrite fields read before another writer commits. Human and JSON success output name the exact config path, provider, and context. A later `camp open` passes the persisted context and provider through the typed `app.OpenRequest`, with `CAMP_DEVPOD_CONTEXT` and `CAMP_DEVPOD_PROVIDER` remaining explicit runtime overrides. S3 endpoint userinfo, credential-shaped URL queries, access tokens, provider credentials, and provider option values are never persisted.

Provider reads must redact values using DevPod's option schema: any option marked `password: true` is redacted even when its name is innocuous. A missing provider reader is a composition error and must return a clear error instead of panicking. Provider mutation currently fails unsupported before reader or persistence effects. The pinned DevPod `pkg/config.SaveConfig` implementation at commit `86b6f9f5` writes the live file with `os.WriteFile` and provides neither locking nor temp-file/fsync/rename publication, so delegating `provider set-options` would not satisfy Camp's atomic durability contract.

Before recording `WorkspaceUp`, production open lists providers in the requested DevPod context. An absent supported local `docker` provider is added noninteractively with `provider add docker --context <context> --use`; an existing non-default `docker` provider is configured with `provider use docker --context <context> --reconfigure`. Camp then re-lists and requires the exact provider identity to be default. DevPod's `--devcontainer-path` is capsule-relative even though Camp retains the validated canonical absolute path in recovery state; passing the absolute path makes DevPod join the workspace root twice.

## Proof commands

```bash
go build ./cmd/camp
go run ./cmd/camp --help
go run ./cmd/camp completion bash
go run ./cmd/camp doctor --json
go test ./cmd/camp ./internal/cli -count=1
```

To isolate the functional file-backend and pasta probes from user configuration, build Camp and run doctor with temporary XDG roots. This still reports managed tools blocked unless their locked identities are installed on the supplied PATH:

```bash
go build -o /tmp/camp-doctor ./cmd/camp
HOME=/tmp/camp-doctor-home \
XDG_CONFIG_HOME=/tmp/camp-doctor-config \
XDG_DATA_HOME=/tmp/camp-doctor-data \
XDG_CACHE_HOME=/tmp/camp-doctor-cache \
/tmp/camp-doctor doctor --json
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
