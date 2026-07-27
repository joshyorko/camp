# CLI Composition

## Current boundary

The executable delegates process I/O and exit status to `internal/cli`. The production tree exposes `setup`, `init`, `open`, `attach`, `sync`, `close`, `reopen`, `recover`, `status`, `images list|capture|restore`, `serve status|logs|restart`, and the read-only `doctor` and `provider list` commands through concrete application and adapter composition, alongside deterministic help, global `--json`, and completion generation. Human `setup` composes the locked installer and then delegates camp creation to the same production `init` boundary; JSON setup remains noninteractive and machine-scoped. No command is real-tool lifecycle proof until the pinned tools complete it without skips. Registering only implemented production handlers keeps help and generated completions truthful.

`camp doctor` emits one versioned capability model in deterministic human or JSON form. Its stable statuses are `healthy`, `degraded`, `blocked`, and `skipped-not-configured`; `blocked` dominates the overall result, followed by `degraded`. A blocked aggregate is rendered exactly once and exits nonzero; it is not followed by a second generic failure envelope. Each probe is bounded independently. DevPod and Hauler are healthy only when the read-only managed-tool inspector proves the installed executable against the compiled lock; doctor never downloads or repairs them. Kernel evidence removes control bytes before its 256-byte display bound, and probe causes or credentials are never result evidence.

The functional backend probe creates one random `camp-doctor/` object, proves exact readback, conditional replacement, duplicate and stale-revision conflicts, and conditional cleanup, then verifies absence. Cleanup uses the recorded opaque revision; an identity conflict or cleanup failure blocks health. The functional pasta probe starts the Camp binary's private token listener behind a unique pasta loopback mapping, records exact helper and child process identities, requires a child network namespace distinct from `/proc/self/ns/net`, reaches the token through the host listener, stops only the recorded identities, proves listener/process absence, and removes the temporary directory only when its device and inode still match. `/proc/self/fd`, `/dev/net/tun`, user namespace creation, LSM context, and host/container boundary are independent results. Provider, workspace, forwarding, and service probes are `skipped-not-configured` when no corresponding configuration or live journal record exists; configured checks use read-only provider listing, exact workspace status, durable forwarder/process plus in-workspace HTTP evidence, and service process/namespace/listener/HTTP observation.

A doctor report is capability evidence, not installation, full DevPod/Room-of-Requirement lifecycle, checkpoint publication, Kubernetes, release, or deployment evidence. A `skipped-not-configured` result is intentionally not proof of the configured path.

The production reachability composition keeps provider, workspace, forwarding, and service probes present in every report. Each is gated by its effective provider setting or an active journal record; closed sessions do not activate probes. The regression coverage in `internal/cli/doctor_reachability_test.go` protects both the gated and configured branches, while configured checks remain read-only and may report `blocked` when their recorded identity is unavailable.

When adding commands, compose existing application use cases and adapters instead of moving lifecycle logic into Cobra handlers. Preserve exact argument arrays through typed ports; do not rebuild shell command strings.

Copyable POSIX-shell recipes are a presentation boundary, not an execution boundary. Render every dynamic argument through `shellQuoteArgument`; domain validation for paths, provider names, contexts, or session identities is not shell escaping. PATH setup quotes the literal managed-directory prefix while leaving only the intentional existing `$PATH` expansion dynamic. The execution-based regressions in `internal/cli/shell_recipe_test.go` paste hostile-but-accepted values into `sh` and prove that rich-init next/recovery commands, doctor remediation, campsite next commands, and PATH export preserve literal values without extra effects.

Production composition is not only command registration. The `internal/adapters/lifecycle` package now supplies the production `app.CloseEffects`, live `SessionEvidence` observer, and post-publication serving refresher seams. Command composition must still provide those adapters and typed propagation of DevPod and IDE options through `app.OpenRequest`. Target entry must preserve the canonical `target.Resolver` → effective DevPod workspace root → `workspace.MapTarget` chain. A Cobra command that calls only package fakes or journal state is not a usable command.

`camp attach [target]` composes the existing ownership-safe `app.Attach` use case with the production journal, ownership revalidator, canonical target resolver, and DevPod adapter. The Cobra boundary owns only typed flag parsing: the VS Code Insiders alias and DevPod SSH port, user, environment, agent, GPG, stdio, keepalive, signing-key, terminal, terminfo, and raw passthrough options. Typed/raw conflict detection remains in the DevPod adapter before process execution. Terminal attach must pass the process stdin, stdout, and stderr through `ports.Command`; capturing subprocess output without those destinations would make a registered interactive command unusable even if its argv were correct.

Nested VS Code entry derives its host only from a DevPod workspace ID that is a
single DNS-label-shaped value of at most 63 ASCII characters. The adapter then
constructs the direct `vscode-remote://ssh-remote+<workspace>.devpod/<target>`
URI and invokes `code` or `code-insiders` without a DevPod SSH shell.
`integration/ide_lifecycle_test.go` binds this public command surface to the
orchestrator-provided `CAMP_TEST_BINARY`: it checks the exact attach help,
stable JSON usage rejection for a conflicting `--insiders`/`--ide` pair, and
that the matching Insiders pair crosses parsing into production composition
under isolated XDG roots and an empty `PATH`. That credential-free contract is
not evidence that a real editor connected to a live DevPod workspace.

The subprocess runner keeps a command with all three terminal streams in Camp's foreground process group and passes those descriptors directly, without capture pipes, so DevPod SSH can detect, read, and write the controlling terminal. Non-interactive commands retain captured output and an isolated process group so cancellation can terminate their full subprocess tree.

`lifecycle.CloseEffects` acts only on identities recorded in the session snapshot. Workspace cleanup distinguishes delete from `--keep-workspace` stop, forwarders and the supervisor use full PID/boot/start identities, and PID reuse means the recorded process is already absent rather than authorizing a signal to the new occupant. Services delegate to `supervisor.ServiceSupervisor.Stop`, which stops each recorded child before its helper and then proves absence; lease release uses the exact recorded revision, and materialization cleanup delegates to the ownership marker/device/inode guard. The production-composition regression in `internal/adapters/lifecycle/lifecycle_test.go` wires those concrete types and asserts child → helper → absence for both services. `lifecycle.SessionObserver` reports PID reuse explicitly and accepts live service evidence only after the existing supervisor inspector validates process topology and listeners; stopped active sessions still require listener-absence validation, while closed historical sessions with absent recorded processes do not claim ports legitimately reused later. `lifecycle.ServingRefresher` requires exactly one request-matching `ServingContentRefreshed` intent when pending work exists, tolerates unrelated concurrent intents without consuming them, validates both complete production service specs before stopping either service, and requires the recorded absolute Hauler command prefix `store --store <absolute-store> serve <service-name>` for `registry` or `fileserver`. Registry refresh retains the recorded mutable overlay so post-seal pushes remain available to the next checkpoint; fileserver refresh moves to the published haul directory. Journal fact commits serialize per session: lease renewal updates only lease state, checkpoint facts retain the latest durable lease, and service-changing `ServiceStart`, `ServiceRestart`, and `ServingContentRefreshed` facts update service records atop the latest snapshot. Recovery facts that reuse a service transition without changing service records retain their state transition. `CheckpointPublisher` reloads that composed snapshot and records only its matching refresh fact, so lease renewal and every intermediate refreshed identity fact cannot replace each other while unrelated pending intents remain owned by their writers. Refresh remains post-publication and an error remains an operational refresh failure rather than a publication rollback.

Unknown commands and arbitrary arguments return the stable usage exit code 2. Human failures use stderr; JSON failures use stdout and the versioned presentation envelope. Command handlers must obtain the inherited mode through `cli.OutputModeFrom` so success and failure presentation do not invent separate flag plumbing.

## Deferred controller and profile command composition

The implemented domain/application slice validates controller identities,
blueprints, blueprint references, execution bindings, provenance, and the
closed profile schema. Blueprint identity accepts only the typed DevPod/Hauler
tool-version fields using strict v-prefixed SemVer 2.0.0, exact supported schema
versions, portable identifiers, and canonical lowercase SHA-256 references.
Every standalone controller, blueprint, blueprint-reference, binding, and
provenance JSON decoder rejects unknown fields and trailing JSON values.
Profiles currently allow only `workspaceEngine: devpod`; there is no arbitrary
setting map. Their decoder also rejects nested unknown fields before digest
validation. Store-facing profile reads are revalidated, and timeline marks
absent, zero, malformed, or unsupported bindings `unknown-blueprint`.

The durable profile adapter implements import, deterministic list, show,
current, activate, and deactivate through one strictly decoded versioned JSON
document. Updates hold an adjacent exclusive lock across validation and
mode-0600 temporary-file publication, fsync, rename, and parent-directory
fsync. The journal owns the optional execution binding: it accepts the first
binding only while the journal has no effects, permits an exact idempotent
repeat, rejects retargeting, and leaves legacy snapshots unbound.

There is still no Cobra or production lifecycle composition. Therefore no
controller, timeline, or profile command is implemented or advertised. A
future surface may add `camp inspect`, `camp timeline`, and `camp profile
import|list|show|current|activate|deactivate` only after production composition
selects the profile-store path, derives the blueprint, and routes lifecycle
entry through the execution guard; each command must use the existing JSON
envelope and must not expose a profile that fails validation.

Before registering `profile activate`, composition must prove that open calls
`ExecutionGuard.BeforeEffects` with the selected profile and blueprint digests,
and that attach, sync, close, and recover call `ExecutionGuard.Require` before
their effects.
Timeline must show legacy sessions without such a binding as
`unknown-blueprint`, never synthesize a compatibility claim. Until those
production dependencies exist, command help must not advertise this surface.

Cobra's help flag and generated `help` command bypass normal positional validation. Keep strict help-path validation at the `cli.Execute` boundary: unavailable or unknown help topics, extra topic components, and garbage combined with `--help` must exit 2 in both human and JSON modes without emitting successful help. Valid root and completion help must remain available.

Help preflight must normalize explicit values for both `--json` and `--help`/`-h` using every boolean spelling accepted by `strconv.ParseBool`, preserve final-flag-wins behavior, preserve explicit false, and stop interpreting flags after `--`; raw flag presence is not parsed state. Wrap Cobra flag-parse failures as typed usage errors at `FlagErrorFunc`, and classify only typed exit errors afterward. Never infer usage from message fragments such as `requires` or `accepts`, because application failures legitimately use those words.

The regression test must keep `camp open` nonzero until a real `open` handler is registered with production dependencies. Adding a command name to help or completion without that composition would turn a truthful rejection into a fake lifecycle surface.

## Configuration and provider boundary

Camp-owned user configuration persists only the typed non-secret fields in `config.Persistent`. Updates take an adjacent exclusive lock, write a mode-0600 temporary file, fsync it, rename it over the destination, and fsync the parent directory. URL userinfo and credential-shaped query parameters are rejected before effects. `CAMP_ACCESS_TOKEN` remains runtime-only; the legacy `accessToken` YAML field is rejected.

Machine defaults and camp manifests remain separate, but first-run human setup is one interaction:

```bash
camp setup
# setup asks for the root and name, then initializes the camp

# direct command for additional camps and scripts
camp init /absolute/camp/root \
  --name capsule-name \
  --backend file:///absolute/camp/backend \
  --workspace-provider room-of-requirement \
  --devpod-context ror
```

The positional `root` is the camp/project directory. `--devpod-context` selects a named DevPod configuration context; it is not a project path. The hidden `--workspace-context` compatibility spelling maps to the same value but cannot be combined with the canonical flag. `--name` is required outside migration. Backend and workspace settings default from machine setup and become explicit in the new manifest, so later machine-default changes cannot silently retarget an existing camp. Camp resolves the requested backend through the same strict `config.ResolveBackend` contract used by production open before creating Camp state or initializing the capsule. An S3 backend therefore requires effective endpoint, region, path-style, and insecure-policy values that pass the normal credential-free S3 checks.

`camp setup` validates the complete root/name/backend/provider/context request before effects, then uses one `config.Store.Modify` transaction that holds the adjacent exclusive lock across read, mutation, validation, mode-0600 temporary-file publication, fsync, rename, and parent-directory fsync. It writes only machine defaults: backend, workspace provider/context, ports, and effective non-secret S3 compatibility settings. The same setup pipeline then calls production init, which writes camp identity and source atomically to `.camp/camp.yaml`; they are never persisted as an active selection in machine config. A missing or non-directory camp root fails before the config transaction or tool setup. `camp open` resolves the selected manifest or journal into the typed `app.OpenRequest`, while credential-bearing environment values remain runtime-only. S3 endpoint userinfo, credential-shaped URL queries, access tokens, provider credentials, and provider option values are never persisted.

Provider reads redact values using DevPod's option schema: any option marked `password: true` is redacted even when its name is innocuous. A missing provider reader is a composition error and returns a clear error instead of panicking. Provider add/use delegates persistence to DevPod, but Camp permits values only for the explicit non-secret Docker allowlist: `DOCKER_PATH` must be absolute and `HELPER` must be boolean. Unknown, password-shaped, and named-provider options fail before subprocess execution; rejected values must not appear in argv, errors, JSON, receipts, or transcripts. DevPod's provider file does not inherit Camp's atomic durability contract.

The shipped provider command lists identities and exposes typed `add`/`use` operations without raw passthrough. The `camp config` subtree exposes the narrow application-owned configuration mutation boundary: `config show --effective` resolves defaults, the user file, and `CAMP_*` overrides through `config.ResolveBootstrap`, and always redacts secrets and credential-bearing URLs. `config set KEY VALUE` persists only machine-scoped backend and DevPod provider/context defaults through `config.Store.Modify`; capsule identity and source remain manifest-owned and are rejected as unsupported keys. Unsupported keys and credential-bearing values fail before publication. Config output is not lifecycle proof; it reports resolved configuration only.

Before recording `WorkspaceUp`, production open lists providers in the requested DevPod context. An absent supported local `docker` provider is added noninteractively with `provider add docker --context <context> --use`; an existing non-default `docker` provider is configured with `provider use docker --context <context> --reconfigure`. Camp then re-lists and requires the exact provider identity to be default. DevPod's `--devcontainer-path` is capsule-relative even though Camp retains the validated canonical absolute path in recovery state; passing the absolute path makes DevPod join the workspace root twice.

The pinned DevPod v0.26.1 provider mutation surface accepts repeated `--option KEY=VALUE` arguments. Camp keeps context and provider identity separately typed, validates the narrow non-secret Docker allowlist before argv construction, and verifies selection through a subsequent context-scoped provider list. Raw passthrough and secret-bearing options remain denied; DevPod owns provider configuration and credentials.

When provider reachability fails, doctor emits `camp provider add docker --context <context>` for the only provider Camp can add, and `camp provider use <provider> --context <context>` for an existing named provider. It never suggests a literal secret. The docs generator covers both commands through effect-free lifecycle handlers; regenerate checked-in artifacts with `go run ./cmd/camp-docs`.

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
- `internal/cli/shell_recipe.go` and `shell_recipe_test.go`
- `internal/config/store.go`, `bootstrap.go`, and their focused tests
- `internal/domain/controller.go`, `blueprint.go`, `validation.go`, and `controller_test.go`
- `internal/app/profile.go` and `profile_test.go`
- `internal/adapters/profilestore/store.go` and `store_test.go`
- `internal/app/execution_binding.go` and `execution_binding_test.go`
- `internal/journal/store.go` and `store_test.go`
- pinned DevPod `cmd/provider/options.go`, `cmd/provider/set_options.go`, and `pkg/config/config.go` at `86b6f9f5`
