> Source: Oracle browser review, GPT-5.5 Pro, session `camp-architectu-safety-current`, completed 2026-07-14.
>
> This is the verbatim architecture feedback captured before implementation.

# Architecture cross-check

## Verdict

**Do not approve the proposed architecture unchanged.** Its fundamental direction is correct, but it currently describes components rather than the safety protocol that connects them. Implemented literally, it could:

* resolve a landing target before the remote tree exists;
* fail every second `sync` because the session’s compare-and-swap baseline was never advanced;
* lose manually pushed images during a live-registry refresh;
* expose registry addresses that are unreachable from a local devcontainer;
* mistake an uncertain DevPod or pointer operation for failure and repeat it destructively;
* delete an adopted user directory based only on “publication verified,” without proving Camp owns that materialization;
* claim branch support while retaining a single global writer lease that prevents branch writers.

The following parts should be retained:

* one Go binary with Cobra;
* thin CLI handlers;
* DevPod owning runtime/provider/SSH/IDE concerns;
* Hauler owning the outer artifact and registry;
* the complete capsule root in the inner archive, excluding exactly `.camp/build/` and `.camp/runtime/`;
* explicit `sync`, with no background filesystem synchronization;
* fresh Hauler stores for checkpoints;
* typed subprocess adapters with fake-executable contract tests;
* immutable generation objects, remote verification, pointer CAS, and delayed cleanup.

---

# 1. Contradictions and unhandled safety invariants

| Finding                                                                                                                                                                                                                                                                                                          | Required correction                                                                                                                                                                                                                                                                                                                                                                                                              |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **1. The DevPod override is not fully represented.** “Managed pinned tools” is too vague. All DevPod assumptions in the prompt were verified against `v0.9.11`, but the direct override makes `skevetter/devpod v0.26.1` authoritative.                                                                          | Remove `v0.9.11` from every runtime, test, lock, bootstrap, and documentation path. Resolve and record the exact `v0.26.1` release commit, Linux amd64/arm64 checksums, and fork identity. Reinspect the listed source files and regenerate every exact-argv contract. Do not assume the old flags or IDE behavior survived unchanged. Hauler remains exactly `v2.0.1`; the Room fixture remains `v1.18.0`.                      |
| **2. `resolve` is ordered too early.** A target such as `memoryd` cannot be resolved by basename or zoxide against a remotely stored capsule until the root has been hydrated.                                                                                                                                   | Split resolution into `ResolveCapsuleSource` before fetch and `ResolveLandingTarget` after the root is committed. Target resolution should return a canonical **root-relative path**, not merely a host absolute path. It is mapped to the effective container workspace folder after DevPod is up.                                                                                                                              |
| **3. The lease order is internally incomplete.** A writer lease must contain the generation opened by the session, but the proposed flow acquires the lease before reading `latest.json`.                                                                                                                        | Read and validate the pointer metadata first, acquire the lease conditionally against that observed generation/digest, then reread the pointer and prove it did not change. Only then fetch the generation. This is a metadata read before the prompt’s expensive fetch step, not an unlocked workspace open.                                                                                                                    |
| **4. A single global writer lease conflicts with branch writers.** The prompt says a second writer may create a branch, while the shown layout has only `<capsule>/leases/writer.json`. A global lease would still block that branch.                                                                            | Make leases lineage-scoped. Preserve `<capsule>/leases/writer.json` for the main lineage and use `<capsule>/branches/<branch>/leases/writer.json` for branch writers. Branch creation must use create-if-absent on its pointer and record the source generation as its parent. No file merging is implied.                                                                                                                       |
| **5. The proposed backend interface is too weak.** “Object, conditional-write, list, and delete” does not establish the semantics needed by leases, pointer CAS, immutable generations, remote verification, or safe lease release.                                                                              | Require `Get`, `Head`, immutable create, conditional replace using an opaque revision, conditional delete, paginated list, size/hash metadata, and streamed readback. File and S3 implementations must have equivalent concurrency guarantees, not merely similarly named methods.                                                                                                                                               |
| **6. S3 ETags cannot be treated as archive SHA-256 values.** Multipart ETags are not content hashes.                                                                                                                                                                                                             | Store the expected SHA-256 and size as object metadata or use a supported checksum facility, then verify it. Where an endpoint cannot provide trustworthy remote checksums, stream the uploaded object back and hash it before pointer CAS. `doctor` must reject an S3 endpoint that cannot provide safe conditional writes.                                                                                                     |
| **7. “Remotely verifies” needs two distinct proofs.** A matching remote byte hash proves storage, but it does not prove the saved Hauler generation is loadable.                                                                                                                                                 | Before upload, load or inspect the saved outer haul with real Hauler and verify the contained inner archive. After upload, prove the remote bytes match SHA-256 and size. The pointer may move only after both facts are journaled.                                                                                                                                                                                              |
| **8. First-open adoption and deletion ownership are undefined.** `camp open ~/SecondBrain` may initialize an arbitrary existing directory. Later deleting “the local materialization” without an ownership record could remove the user’s original only copy.                                                    | Add a `Materialization` record containing canonical path, original path, device/inode identity where applicable, whether Camp created or explicitly adopted it, and whether deletion is permitted. Cleanup must verify an ownership marker and the expected canonical path. Never infer ownership merely because `.camp` exists.                                                                                                 |
| **9. Source selection is ambiguous when both local data and a remote generation exist.** Silently preferring one can overwrite or orphan the other.                                                                                                                                                              | Define a source-resolution matrix. An explicitly supplied uninitialized local root must not be overwritten by the configured remote. A preexisting nonempty managed work root must trigger recovery or an explicit branch decision. No hydrate operation may rename over an unexplained directory.                                                                                                                               |
| **10. The local operation lock and remote writer lease are different controls.** The proposal mentions leases but not the local lock required to refuse concurrent `sync` and `close`.                                                                                                                           | Use a session-scoped local operation lock with PID, process start identity, command, session ID, and acquisition time. Use the remote lease for cross-machine writers. Both must be reconciled after crashes. The remote lease heartbeat must continue while the session is open, not merely while a CLI command is running.                                                                                                     |
| **11. The heartbeat needs a persistent owner.** The `camp open` CLI may exit after launching an IDE or after the interactive shell ends, while the workspace remains open.                                                                                                                                       | A durable supervisor process must own lease heartbeats, Hauler services, tunnel processes, logs, and liveness information. It must not perform background filesystem synchronization. A hidden subcommand in the same `camp` binary is sufficient.                                                                                                                                                                               |
| **12. “Mirror remote work” must distinguish providers.** The local provider must not rsync the root onto itself. A remote provider must use the effective container workspace folder, not the host upload path or `/workspaces/<name>` assumptions.                                                              | Add a `WorkspaceTransport` capability: `LocalNoop` and `DevPodSSHMirror`. The remote implementation uses DevPod’s generated SSH host, `rsync` with delete/hardlink/symlink/mode preservation, and a process-pipeline tar fallback. It must query the effective workspace folder. DevPod export/import must never enter this pipeline.                                                                                            |
| **13. Remote mirroring can overwrite Camp’s live state.** A remote `rsync --delete` can remove or replace `.camp/build` and `.camp/runtime`, including journals, generated archives, or service state.                                                                                                           | Exclude exactly the host-owned transient paths from mirror operations. Stable capsule files such as `capsule.yaml` and `lock.yaml` may return from the workspace, but `images.json` and the Hauler manifest must be validated and regenerated after mirroring.                                                                                                                                                                   |
| **14. Checkpoint consistency is not defined.** Files can change while rsync, tar creation, or image enumeration is running. A tar warning such as “file changed as we read it” cannot be published as success.                                                                                                   | Build from staging where possible. For a live local root, detect read-time mutation and retry or fail the checkpoint. Never publish a checkpoint after an archive or mirror reports incomplete or unstable reads. Define the checkpoint cut for the mutable registry separately.                                                                                                                                                 |
| **15. Loopback service addresses may be unreachable inside a local devcontainer.** Host `127.0.0.1` normally does not refer to the host from inside a container. Merely labeling a provider “local” does not solve this.                                                                                         | Select connectivity by capability, not provider name. Remote workspaces always require supervised DevPod reverse forwards. Local workspaces need a reachability probe and either a DevPod reverse forward or an explicitly verified host-gateway path. The registry and reverse-forward listener must remain loopback-only.                                                                                                      |
| **16. Port allocation has a time-of-check/time-of-use race.** Selecting a free port and closing the probe socket before Hauler binds it is not a reservation.                                                                                                                                                    | Add a `PortAllocator` abstraction with launch-and-retry behavior, durable endpoint recording, and readiness probes. Reverse-tunnel ports must be selected before `devpod up` so the intended `CAMP_*` values can be supplied. Service restart should reuse recorded ports or fail clearly rather than silently changing unreachable endpoints.                                                                                   |
| **17. “Refresh services” is not a safe live-registry design.** The writable Hauler registry contains mutable Distribution state that is not automatically reflected in the source store. Replacing it after a checkpoint can lose pushes made after the snapshot.                                                | Separate the mutable session registry state from immutable generation stores. Establish a registry write barrier or brief quiesce, snapshot/copy the registry into the fresh generation store, resume the same mutable session registry, publish, then rotate immutable fileserver/base content. Do not replace or discard a registry overlay containing post-snapshot pushes.                                                   |
| **18. The image boundary is underspecified.** “Image capture/restore” does not cover every named tag, direct registry pushes, digest verification, private-name collision avoidance, `local: true` semantics, platform metadata, or OCI cache exports.                                                           | The image domain must distinguish engine image ID, original tag, original repo digest when present, captured registry manifest digest, platform, and creation metadata. Enumerate the quiesced registry catalog as well as the workspace engine. Only host-daemon sources may use `local: true`. Registry-exported BuildKit caches must survive as explicit registry artifacts; do not describe opaque daemon cache as captured. |
| **19. The session CAS baseline must advance after every successful `sync`.** A session opened at generation 42 that publishes 43 cannot compare the next checkpoint against 42 again.                                                                                                                            | After pointer commit, atomically update the journal’s current base generation/digest and expected pointer revision. Keep the original opened generation for audit. The next checkpoint’s parent is 43 and its CAS expectation is 43. Update lease status with the current published base if needed without erasing the original opening fact.                                                                                    |
| **20. `CAMP_CHECKPOINT` semantics after sync are unclear.** An environment variable injected during `devpod up` cannot be changed inside already-running shells.                                                                                                                                                 | Define it explicitly as the checkpoint from which the workspace was opened, or inject the current checkpoint on every later `attach`/SSH command and expose a runtime state file for the newest published checkpoint. Do not claim an existing process environment is dynamically updated.                                                                                                                                       |
| **21. Read-only and branch modes are not represented in the proposed lifecycle.** These are not small flags; they change lease, checkpoint, conflict, and cleanup behavior.                                                                                                                                      | Persist session mode. A read-only session never acquires or renews a writer lease and no recovery path may accidentally publish it. `sync` reports a deliberate checkpoint skip; `close` records deliberate discard before teardown. A branch session reads its own pointer after creation and uses its own lineage lease and CAS baseline.                                                                                      |
| **22. DevPod responsibilities are broader than an argv package.** Exact terminal behavior, attach forwarding flags, deterministic IDs, nested VS Code URI opening, provider stop/delete, workspace status reconciliation, and raw passthrough conflict handling are not present in the architecture description. | Use a typed DevPod port with `Up`, `SSH`, `Status`, `Stop`, `Delete`, `ResolveWorkspaceFolder`, and forwarding operations. Terminal mode must always emit `--ide none --open-ide=false`. Child IDE targets must be translated to the absolute path inside the container and percent-encoded in the `vscode-remote` URI. `code-insiders` is the exact local launcher.                                                             |
| **23. The archive package needs stronger content and security invariants.** “Secure tar.zst” is not enough.                                                                                                                                                                                                      | Preserve ordinary files, modes, timestamps, symlinks, and hardlinks; do not follow symlinks out of the root. Reject absolute paths, `..`, symlink-parent escapes, unsafe hardlink targets, duplicate type conflicts, and device-node creation. Any unsupported special file must cause an exact-path error, not a silent third exclusion. Extract only into a new temporary directory and atomically rename it.                  |
| **24. The manifest build order must be explicit.** The inner archive must contain the regenerated manifest, while the manifest’s `Files` document points at that inner archive.                                                                                                                                  | Capture images, write `.camp/images.json`, regenerate deterministic `Files` and `Images` YAML, then build `.camp/build/<capsule>.tar.zst`. Run Hauler from the correct capsule-root context so the relative file path resolves. Build the outer generation only from a fresh store.                                                                                                                                              |
| **25. Configuration requires a two-phase resolver.** Remote capsule configuration cannot be read before the backend and capsule have already been selected.                                                                                                                                                      | Resolve bootstrap settings from flags → environment → user config → defaults. After local adoption or hydration, load capsule config and recompute runtime settings while preserving flags and environment precedence. Capsule config may contain endpoints and policy, but never backend credentials.                                                                                                                           |
| **26. The tool locks are two different artifacts.** The architecture does not distinguish Camp’s managed-tool release lock from `.camp/lock.yaml`.                                                                                                                                                               | Keep a committed distribution tool lock containing DevPod/Hauler asset URLs, versions, forks, platforms, and checksums. Keep `.camp/lock.yaml` for capsule compatibility inputs such as the resolved Wolfi digest and applicable tool compatibility. Generate the fallback overlay in `.camp/runtime`, never by overwriting an existing `.devcontainer`.                                                                         |
| **27. The command contract is mostly absent.** The architecture names lifecycle orchestration but not first-class use cases for `attach`, `status`, `list`, `history`, `recover`, `init`, `config`, `doctor`, `serve`, `images`, `provider`, and raw DevPod/Hauler passthrough.                                  | Model each command as an application use case. `provider` delegates to DevPod rather than creating Camp providers. Raw passthrough still uses the locked executable and redaction. Define how no-argument commands select a session when several capsules are open; ambiguity must be an error.                                                                                                                                  |
| **28. Human, verbose, and JSON output need an interface boundary.** Printing directly from lifecycle code will make stable JSON and exact recovery reporting unreliable.                                                                                                                                         | Use typed progress events and typed operation results. Human and JSON presenters consume the same events. Errors carry stage, safe-state description, checkpoint publication status, session ID, and the exact recovery command. Verbose command rendering must use structured redaction.                                                                                                                                        |
| **29. `history` cannot be usefully reconstructed from the stated remote layout alone.** Overwriting `latest.json` destroys prior pointer metadata, while generation filenames only expose generation and hash.                                                                                                   | Add immutable generation metadata sidecars, for example `generations/<generation>-<sha256>.json`, containing the same publication metadata as the pointer. The `.tar.zst` remains the authoritative Hauler artifact; the JSON is lifecycle metadata for history and recovery. Without sidecars, `history` would have to download and inspect every generation.                                                                   |
| **30. Publication success and cleanup success are separate terminal facts.** The proposed statement “delete after publication is verified” is still too weak. The prompt requires stop/delete, tunnel shutdown, service shutdown, and lease release before removing materialization.                             | Journal `CheckpointPublished` permanently once CAS succeeds. Later cleanup failures cannot roll it back and must never trigger another publication. Remove a Camp-owned root only after the configured DevPod stop/delete succeeded, forwarders and services stopped, the lease was conditionally released, and path ownership was revalidated.                                                                                  |

---

# 2. Package and interface boundary corrections

The package list should be reorganized around domain invariants, application use cases, and external adapters. A single all-purpose “application service” will become a god object.

```text
cmd/camp
└── internal/cli                 Cobra wiring, flags, help, completion
    └── internal/app
        ├── open
        ├── attach
        ├── sync
        ├── close
        ├── recover
        ├── status/list/history
        ├── init/config/doctor
        ├── serve/images
        └── checkpoint           shared checkpoint publication use case

internal/domain
├── capsule
├── lineage
├── generation
├── lease
├── session
├── checkpoint
├── materialization
└── imageinventory

internal/ports
├── objectstore
├── journal
├── clock
├── hostidentity
├── runner
├── process
├── portallocator
├── workspace
├── artifactstore
├── registry
└── events

internal/adapters
├── filebackend
├── s3backend
├── devpod
├── hauler
├── sshtransfer
├── archive
├── containerengine
├── supervisor
└── tools
```

## Boundary corrections

| Boundary              | Responsibility                                                                                                                                                                                                                                                           |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **`internal/domain`** | Pure types and invariants: capsule ID, branch/lineage, immutable generation metadata, pointer parentage, lease ownership, session mode, cleanup eligibility, image mappings. No filesystem, subprocess, YAML, AWS, or Cobra imports.                                     |
| **`internal/app`**    | One use-case type per command. `Open`, `Sync`, and `Close` coordinate ports and transition the journal. A shared `CheckpointPublisher` implements the only path that can create, verify, upload, and publish a generation.                                               |
| **`config`**          | Two-phase resolution, provenance, schema-aware redaction, XDG locations, mode enforcement, capsule policy, and environment parsing. It should not download artifacts or start tools.                                                                                     |
| **`capsule`**         | Capsule-source selection, local adoption, materialization ownership, initial `.camp` creation, fallback devcontainer generation, and cleanup eligibility.                                                                                                                |
| **`target`**          | Root-relative target resolution and ambiguity reporting. It must not know Cobra or DevPod. A separate mapper converts the resolved relative target into the effective container workspace path.                                                                          |
| **`journal`**         | Durable write-ahead intents and completed facts, local operation locking, atomic snapshots, append history, mode `0600`, and crash reconciliation data. It must not invoke external commands.                                                                            |
| **`objectstore`**     | Raw strongly consistent object operations. It must not know `latest.json`, lease schemas, generation numbering, or branch rules.                                                                                                                                         |
| **`coordination`**    | `PointerRepository`, `LeaseRepository`, and `GenerationRepository`, layered over `ObjectStore`. This is where JSON schemas, CAS rules, immutable naming, branch paths, and verification live.                                                                            |
| **`archive`**         | Inner root archive build/extract, exact exclusions, path safety, metadata preservation, staging, and mutation detection. It does not invoke Hauler.                                                                                                                      |
| **`hauler`**          | Exact argv, multi-document manifest generation, fresh-store assembly, load/extract/save/copy, and registry/fileserver service specifications. It receives image sources and an inner archive; it does not enumerate Docker images itself.                                |
| **`devpod`**          | Exact pinned CLI behavior: up, SSH, attach, status, stop, delete, workspace-folder query, environment flags, IDE selection, and forwarders. No checkpoint transport.                                                                                                     |
| **`workspace`**       | Provider-independent operations needed by Camp: effective root, command execution, target mapping, local/no-op versus remote mirror, and reachability. Its remote implementation composes DevPod, `ssh`, `rsync`, and `tar`; it does not implement SSH.                  |
| **`images`**          | Engine detection, tagged-image inventory, reference encoding, registry pushes, digest validation, catalog merge, inventory persistence, restore, and original-name retagging. It depends on a workspace executor and registry abstraction, not a concrete DevPod client. |
| **`supervisor`**      | Long-lived Hauler services, reverse forwards, lease heartbeat, readiness, logs, PIDs, process identity, and controlled restart. It never scans or synchronizes the workspace.                                                                                            |
| **`tools`**           | Exact executable resolution, fork/version/checksum verification, safe download, per-platform installation, and atomic activation. Adapters receive resolved executable paths rather than searching `PATH` themselves.                                                    |
| **`presentation`**    | Human progress, stable versioned JSON, verbose logs, and errors with exact recovery commands. Application code emits typed events instead of printing.                                                                                                                   |
| **`doctor`**          | Capability probes across all ports and adapters. It reports whether the selected backend can perform safe conditional writes and remote verification, rather than only checking connectivity.                                                                            |

## The object-store contract must be stronger than CRUD

A suitable shape is:

```go
type Revision string

type ObjectMeta struct {
    Key       string
    Size      int64
    Revision  Revision // opaque ETag, version token, or file-backend revision
    SHA256    string   // populated only when remotely trustworthy
    Modified  time.Time
}

type WriteCondition struct {
    MustBeAbsent  bool
    MatchRevision Revision
}

type ObjectStore interface {
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error)
    Head(ctx context.Context, key string) (ObjectMeta, error)

    // Used for content-addressed generation objects. Existing unequal bytes fail.
    PutImmutable(
        ctx context.Context,
        key string,
        source RestartableSource,
        expectedSHA256 string,
        expectedSize int64,
    ) (ObjectMeta, error)

    // Used for latest pointers and leases.
    PutConditional(
        ctx context.Context,
        key string,
        body []byte,
        condition WriteCondition,
    ) (ObjectMeta, error)

    DeleteConditional(
        ctx context.Context,
        key string,
        expected Revision,
    ) error

    List(
        ctx context.Context,
        prefix string,
        pageToken string,
    ) (items []ObjectMeta, nextPageToken string, err error)
}
```

The coordination repositories then expose domain operations such as:

```text
PointerRepository.Read(lineage)
PointerRepository.CompareAndSwap(expectedPointer, expectedRevision, nextPointer)

LeaseRepository.Acquire(lineage, owner, observedPointer, ttl)
LeaseRepository.Renew(leaseToken)
LeaseRepository.Release(leaseToken)
LeaseRepository.Read(lineage)

GenerationRepository.PutAndVerify(generation)
GenerationRepository.ReadMetadata(generation)
GenerationRepository.List(lineage)
```

The file backend needs a per-key interprocess lock, compare-under-lock, same-filesystem temporary file, file `fsync`, atomic rename, and parent-directory `fsync`. The S3 backend needs real conditional requests. An endpoint without reliable conditional writes is not a supported writer backend.

## One-shot subprocesses and supervised services must be separate ports

A one-shot runner should support argv arrays, controlled environment additions, current directory, stdin/stdout streams, cancellation, pipelines, and redaction metadata. Long-running processes need a different interface with PID/process-group identity, readiness, logs, graceful shutdown, and reconciliation after a lost parent.

Do not make the generic runner aware of DevPod or Hauler flags. Only the relevant adapter constructs those arguments.

---

# 3. Lifecycle transitions and recovery cases that must be explicit

Every external side effect needs two journal records:

1. a durable **intent** before invocation;
2. a durable **completed fact** after the postcondition is verified.

A crash between them creates an “outcome unknown” state that is resolved by observing the external system. Blindly repeating the command is not acceptable for pointer writes, DevPod creation/deletion, uploads, or process termination.

## Open state machine

| Completed state                       | Required durable postcondition                                                                                                                                                                     |
| ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `SessionCreated`                      | Session ID, capsule request, branch, read-only mode, flags, sanitized config fingerprint, and tool-lock identities are recorded.                                                                   |
| `BootstrapConfigResolved`             | Backend, default capsule, XDG paths, and host-only credentials have been selected without reading remote capsule content.                                                                          |
| `SourceResolved`                      | Exactly one source is selected: existing managed session, explicit local adoption, or remote lineage. Conflicting local and remote candidates are rejected.                                        |
| `PointerObserved`                     | The latest pointer is absent or schema-valid, belongs to the expected capsule/branch, and references an object under the correct prefix. Pointer bytes and opaque revision are recorded.           |
| `LeaseAcquired` or `ReadOnlySelected` | A writer lease is conditionally acquired for the lineage and anchored to the observed pointer, or the session is permanently marked read-only.                                                     |
| `PointerRevalidated`                  | The pointer still matches the generation/digest and revision observed before lease acquisition. Otherwise release and restart or report conflict.                                                  |
| `GenerationFetched`                   | The selected object exists in a completed cache path; partial downloads retain a `.partial` name.                                                                                                  |
| `GenerationVerified`                  | SHA-256 and size match the pointer, and the object key’s generation/hash naming agrees with its content.                                                                                           |
| `StoreLoaded`                         | Hauler successfully loaded the generation into a session-specific store. Reuse requires proof from this journal.                                                                                   |
| `RootStaged`                          | The inner archive was extracted into a new temporary directory with all traversal and symlink checks enforced.                                                                                     |
| `RootCommitted`                       | The staged root was atomically renamed to the intended work path, and materialization ownership was recorded. No unexplained destination was replaced.                                             |
| `CapsuleConfigApplied`                | Capsule config and `.camp/lock.yaml` were loaded and runtime effective configuration was recomputed. Existing devcontainer files were detected before generating a fallback overlay.               |
| `TargetResolved`                      | The target exists, is a directory, is unambiguous, and is represented as a root-relative path.                                                                                                     |
| `EndpointsAllocated`                  | Local service ports and any reverse-forward ports are recorded before DevPod environment construction.                                                                                             |
| `ServicesReady`                       | Registry and fileserver are listening on loopback, passed readiness checks, and are owned by the session supervisor.                                                                               |
| `DevPodUpReconciled`                  | The deterministic workspace exists and is in the expected state. A timeout is reconciled with `status/list` before retrying `up`.                                                                  |
| `WorkspaceRootResolved`               | The effective container workspace folder was queried. The root-relative target was mapped to an absolute path inside that workspace.                                                               |
| `ForwardingReady`                     | Required reverse forwards are alive, loopback-bound, and supervised. Reachability was tested from the workspace.                                                                                   |
| `ImagesRestored`                      | Each compatible captured image is present at the captured digest and retagged to every original name. Repeating the step has no effect.                                                            |
| `SessionReady`                        | The workspace, services, forwarders, lease heartbeat, addresses, checkpoint base, and recovery command are all known.                                                                              |
| `EntryDispatched`                     | Terminal SSH, tmux attachment, DevPod IDE opening, or the VS Code nested-folder launcher was attempted. Failure here leaves a valid open session and should recommend `camp attach`, not teardown. |

For an uninitialized local root, `LocalRootAdopted` replaces the fetch/load/extract states. It must create stable capsule metadata, the Hauler manifest, the image inventory, and the digest lock without overwriting existing devcontainer configuration.

## Sync state machine

| Completed state              | Required durable postcondition                                                                                                                                                                                                           |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `CheckpointStarted`          | The local session operation lock is held, the intended parent generation is recorded, and the session is not read-only.                                                                                                                  |
| `LeaseValidated`             | The supervisor still owns a live lineage lease. Lease loss prevents publication even if the local archive can still be built.                                                                                                            |
| `WorkspaceMirrored`          | Remote providers were mirrored from the queried effective root. Local providers recorded a no-op. Transient Camp paths were protected.                                                                                                   |
| `RootSnapshotStable`         | The archive input is a completed staging snapshot, or the live-tree archive operation can prove no read-time mutation occurred.                                                                                                          |
| `WorkspaceImagesInventoried` | Docker, then Podman, was probed inside the workspace. Every named tag and its relevant metadata were recorded; dangling images were omitted.                                                                                             |
| `WorkspaceImagesPushed`      | Each captured image was pushed to the deterministic private registry namespace and the resulting registry digest was verified.                                                                                                           |
| `RegistrySnapshotSealed`     | Registry writes were briefly gated or quiesced, the complete catalog and tags were enumerated, and a consistent registry snapshot was copied for the generation. The mutable session registry was resumed without dropping later pushes. |
| `ManifestCommitted`          | `.camp/images.json` and deterministic multi-document `.camp/hauler-manifest.yaml` were atomically replaced and validated.                                                                                                                |
| `InnerArchiveBuilt`          | The complete capsule root was archived, excluding exactly `.camp/build/` and `.camp/runtime/`. The manifest and image inventory are inside it.                                                                                           |
| `InnerArchiveVerified`       | The archive can be listed/extracted safely and its own checksum and size are recorded.                                                                                                                                                   |
| `FreshStoreAssembled`        | A new empty Hauler store contains the inner root archive plus the sealed registry/image content. No previously served store was mutated in place.                                                                                        |
| `GenerationSaved`            | Hauler saved one outer generation archive under a temporary local name.                                                                                                                                                                  |
| `GenerationValidated`        | Real Hauler can load the saved archive and find the expected files and images. Its SHA-256, size, generation, parent, tools, session ID, and creation time are fixed.                                                                    |
| `GenerationUploaded`         | The immutable generation object exists at `<generation>-<sha256>.tar.zst`. An existing object is accepted only after equal-byte verification.                                                                                            |
| `GenerationRemoteVerified`   | The backend proved the stored remote bytes match the expected SHA-256 and size.                                                                                                                                                          |
| `PointerCommitted`           | `latest.json` was conditionally replaced against the session’s current parent generation/digest and opaque pointer revision. This is the irreversible publication boundary.                                                              |
| `BaselineAdvanced`           | The session’s current CAS base, pointer revision, checkpoint number, and history metadata are updated. The original opened generation remains available for audit.                                                                       |
| `ServingContentRefreshed`    | Immutable served content was rotated only after publication. A failure here is operational; it does not undo the checkpoint. Mutable post-snapshot registry writes remain available for the next sync.                                   |
| `CheckpointComplete`         | The operation lock is released and the result clearly distinguishes the published checkpoint from any nonfatal refresh issue.                                                                                                            |

A read-only `sync` uses a separate terminal state such as `CheckpointSkippedReadOnly`. It must not enter the publication pipeline.

## Close state machine

`close` must call the same internal checkpoint publisher while retaining the local operation lock. It must not shell out to a separate `camp sync` process.

| Completed state                                        | Required durable postcondition                                                                                                                 |
| ------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `CloseStarted`                                         | The operation lock is held, the cleanup policy is recorded, and no competing sync/close can begin.                                             |
| `FinalCheckpointComplete` or `ReadonlyDiscardRecorded` | A writer session has a newly published generation. A read-only session explicitly records that no checkpoint will be created.                  |
| `WorkspaceStoppedOrDeleted`                            | Default behavior verified DevPod deletion. `--keep-workspace` verified DevPod stop. An uncertain command result was reconciled through status. |
| `ForwardersStopped`                                    | Every recorded reverse-forward process is gone, with PID identity checked to avoid killing an unrelated reused PID.                            |
| `ServicesStopped`                                      | Registry and fileserver are gone and logs remain available.                                                                                    |
| `LeaseReleased`                                        | The exact owned lease revision/session was conditionally deleted. Never blindly delete the current lease object.                               |
| `MaterializationRemoved`                               | Only a verified Camp-owned root and session store were removed. Interrupted deletion remains recoverable.                                      |
| `SessionClosed`                                        | The terminal session record contains publication outcome, cleanup outcome, generation metadata, and no live resources.                         |

## Recovery cases that need dedicated handling

| Crash or failure point                             | Required recovery behavior                                                                                                                                                     |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Intent recorded; command outcome unknown**       | Query the external state. Never automatically rerun `devpod up`, `delete`, a pointer CAS, or an upload whose result may already have succeeded.                                |
| **Partial download**                               | Keep it under a nonfinal name, discard or resume safely, then verify from byte zero before rename.                                                                             |
| **Store load or extraction interrupted**           | Delete only the session-specific temporary store/root and repeat. Never replace a committed root.                                                                              |
| **Root committed, journal completion missing**     | Validate the ownership marker and expected archive identity, then record the completed fact rather than extracting over it.                                                    |
| **One service started, the other failed**          | Reconcile PID ownership, restart only missing processes, and preserve or safely reallocate endpoints.                                                                          |
| **DevPod `up` timed out**                          | Query the deterministic workspace ID. Continue if it is healthy; repair or fail if partially created. Do not create a second workspace under a new ID.                         |
| **Forwarding partially established**               | Restart missing forwarders independently and re-run workspace reachability probes.                                                                                             |
| **Lease heartbeat lost**                           | Stop all publication attempts. The session may remain usable, but `sync`/`close` must require reacquisition against an unchanged pointer, read-only continuation, or a branch. |
| **Remote mirror interrupted**                      | Discard the incomplete staging mirror and repeat. The prior local root and prior remote pointer remain authoritative.                                                          |
| **Some images pushed**                             | Re-enumerate and compare registry digests. Already correct content is reused; missing or mismatched content is pushed again.                                                   |
| **Local verified haul exists but is not uploaded** | Resume directly at immutable upload; do not rebuild and silently create different bytes under the same intended generation.                                                    |
| **Multipart upload interrupted**                   | Abort the multipart upload or resume only with a fully journaled part manifest. Never move the pointer.                                                                        |
| **Generation uploaded but not remotely verified**  | Verify the existing immutable object. Reupload only when it is absent; unequal existing bytes are a hard integrity failure.                                                    |
| **Generation verified but pointer not moved**      | Attempt the original CAS using the recorded parent and pointer revision.                                                                                                       |
| **Pointer CAS response was lost**                  | Reread `latest.json`. If it contains the intended object and session ID, record publication success. Otherwise treat it as a conflict or retry against the original condition. |
| **CAS conflict**                                   | Preserve the uploaded generation as an orphaned but valid checkpoint. Do not overwrite latest. Report an exact branch/recovery command.                                        |
| **Pointer moved, journal says it did not**         | Identify the published pointer by object key and session ID, record `PointerCommitted`, advance the baseline, and do not publish a duplicate generation.                       |
| **Pointer moved, service refresh failed**          | Report the successful checkpoint separately. `camp recover <session-id>` resumes only refresh or cleanup.                                                                      |
| **Pointer moved, cleanup failed**                  | Never run another final sync automatically. Continue at the first incomplete cleanup transition.                                                                               |
| **Read-only session interrupted**                  | Recovery may restore services or continue teardown but must never enter checkpoint publication.                                                                                |
| **SIGINT/SIGTERM during build/upload**             | Cancel the current temporary operation, journal interruption, retain the last known safe workspace, and print `camp recover <session-id>`.                                     |
| **SIGINT/SIGTERM after pointer commit**            | Treat publication as successful and resume only the remaining service/lease/materialization cleanup.                                                                           |

`camp recover <session-id>` should reconcile a journaled session. `camp recover <checkpoint>` should verify an existing local or remote generation and attempt the recorded CAS or publish it to an explicitly named branch. It must not force-overwrite a newer pointer.

---

# 4. Requirement-to-test risk list

`P0` means a defect can lose data, corrupt lineage, expose data, or publish a false checkpoint. `P1` means a required user-visible contract would be broken. `P2` means installation, documentation, or release completeness would be broken.

| Priority | Requirement cluster                      | Primary risk                                                                                                           | Required test evidence                                                                                                                                                                                                                                                    |
| -------- | ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **P0**   | DevPod `v0.26.1` source contract         | Old `v0.9.11` flags or behavior are silently assumed.                                                                  | Real `v0.26.1` fork binary and source inspection; exact argv goldens for every exposed `up` flag, terminal mode, status, SSH, attach, forwarders, stop, delete, and IDE mode. Verify the binary’s checksum/fork identity.                                                 |
| **P0**   | Hauler `v2.0.1` contract                 | A custom or invalid bundle is produced, or a served-registry push is omitted.                                          | Real Hauler file + images → sync → save → load → extract → serve. Push to the writable registry; prove it is absent from the original store, copy/sync it back, save/load again, and prove it is then present.                                                            |
| **P0**   | Whole-root archive                       | Files outside a hand-selected component graph are omitted.                                                             | Fixture containing nested repositories, hidden files, virtual environments, Rust `target`, symlinks, hardlinks, generated files, patches, and nested projects. Verify byte and metadata continuity. Assert the only excluded trees are `.camp/build` and `.camp/runtime`. |
| **P0**   | Archive extraction security              | A malicious generation writes outside the work root or follows a symlink escape.                                       | Absolute paths, `..`, symlink-then-child, hardlink escape, duplicate type conflict, special node, overlong name, interrupted extraction, and preexisting destination tests.                                                                                               |
| **P0**   | Manifest correctness                     | The inner archive is missing, the manifest is outside the root, or image source semantics are false.                   | Parse deterministic multi-document YAML; real `hauler store sync`; assert the `Files` path is exact; assert remote workspace images never receive false `local: true`.                                                                                                    |
| **P0**   | File-backend CAS                         | Two writers both believe they updated latest, especially on a mounted PVC.                                             | Multiprocess contention tests using per-key locking, temp write, file and directory fsync, conditional update/delete, stale revision, create-if-absent, and crash between each filesystem operation. Include a mounted-filesystem job.                                    |
| **P0**   | S3/MinIO CAS and verification            | Multipart ETag is mistaken for SHA-256 or an unsafe endpoint accepts lost updates.                                     | MinIO integration for `If-None-Match`, `If-Match`, stale revision conflicts, path-style endpoint, TLS modes, interrupted multipart upload, remote checksum/readback, and conditional lease deletion.                                                                      |
| **P0**   | Writer lease and branches                | A live lease is stolen, an expired session publishes, or a branch blocks on the main lease.                            | Two-machine/process tests: live refusal, read-only fallback, branch acquisition, expiry, heartbeat renewal, stale owner takeover using CAS, sleep/wake lease loss, and simultaneous pointer attempts from the same parent.                                                |
| **P0**   | Pointer publication                      | Local cleanup occurs after upload but before a successful CAS, or an ambiguous CAS causes duplicate publication.       | Failure injection before upload, after upload, after verification, during CAS response loss, after pointer commit, and before baseline journal update. Assert local materialization remains until all required cleanup states.                                            |
| **P0**   | Baseline advancement                     | A second sync compares against the originally opened generation.                                                       | Open at 42, sync to 43, sync to 44, close to 45; verify each parent and CAS expectation. Repeat on a branch.                                                                                                                                                              |
| **P0**   | Journal durability                       | Crash recovery repeats non-idempotent actions or loses the publication boundary.                                       | Crash injection after every intent and every completion record. Kill the process during fsync, rename, DevPod creation, upload, CAS, service rotation, lease release, and recursive cleanup.                                                                              |
| **P0**   | First-use adoption and ownership         | Camp deletes an arbitrary existing directory or overwrites local work with remote content.                             | Open an uninitialized explicit root, root through a symlink, conflicting local/remote capsule, unexplained XDG work directory, failed first publication, and successful close. Assert deletion only occurs with a matching ownership record.                              |
| **P0**   | Remote workspace return                  | DevPod’s uploaded remote copy changes but staging remains stale.                                                       | SSH fixture with a nonstandard effective workspace folder; edit, delete, chmod, symlink, and hardlink remotely; rsync back; disable rsync and repeat through tar-over-SSH. Assert no DevPod export/import invocation.                                                     |
| **P0**   | Live service reachability                | `$CAMP_REGISTRY` works on the host but not inside the devcontainer, or a reverse tunnel binds publicly.                | Reachability probes from local Docker and remote SSH fixtures; inspect listener addresses; kill and restart forwarders; ensure registry/fileserver are loopback-only on both ends.                                                                                        |
| **P0**   | Registry checkpoint cut                  | Direct pushes made near `sync` are lost when serving state rotates.                                                    | Push one image before the snapshot barrier and another after it. The first must be in the current generation; the second must remain in the live overlay and enter the next generation. Inject failure during snapshot and refresh.                                       |
| **P0**   | Named-image inventory and restore        | Some tags, manually pushed images, platforms, or digests are lost or incorrectly retagged.                             | Multiple tags per image, same tag text from different registries, long references, private ports, digest-only metadata, Docker then Podman detection, dangling exclusion, catalog pagination, direct push, restore idempotency, and digest mismatch rejection.            |
| **P0**   | Read-only close                          | A read-only session accidentally publishes edits or silently changes latest during recovery.                           | Open read-only, modify files/build images, run sync, interrupt close, recover, and finish teardown. Assert no lease, generation, pointer, or branch object was created.                                                                                                   |
| **P0**   | Cleanup safety                           | A successful checkpoint is reported as failed, or publication is retried because DevPod deletion failed.               | Inject failure at delete/stop, each forwarder stop, each Hauler service stop, lease release, and local deletion. Assert checkpoint result remains successful and recovery resumes cleanup only.                                                                           |
| **P1**   | Target resolution                        | Basename ambiguity or a zoxide path escapes the capsule.                                                               | Absolute, relative, unique basename, ambiguous basename with candidate list, symlink escape, zoxide outside root, zoxide inside root, no target, and target only present after remote hydration.                                                                          |
| **P1**   | Terminal and tmux                        | DevPod defaults to VS Code or tmux becomes mandatory.                                                                  | Assert `--ide none --open-ide=false`; test tmux present and absent inside the workspace; verify deterministic tmux session and shell fallback.                                                                                                                            |
| **P1**   | VS Code Insiders nested target           | Camp opens the root, launches the wrong binary, or starts a redundant shell.                                           | Real DevPod setup plus fake `code-insiders`; assert `up --ide vscode-insiders --open-ide=false`, exact encoded `vscode-remote://ssh-remote+<id>.devpod/<container-target>` URI, and no interactive SSH.                                                                   |
| **P1**   | Attach SSH flags                         | Port, user, environment, agent, GPG, or terminal flags are dropped.                                                    | Exact argv goldens for every supported SSH option, repeated forwards, conflict rejection, target mapping, IDE reopen, and tmux behavior.                                                                                                                                  |
| **P1**   | DevPod passthrough                       | Known flags are duplicated or raw arguments override Camp safety choices unexpectedly.                                 | Table-driven tests for every directly exposed flag, repeated flags, `--insiders`, `--devpod-arg`, `--`, duplicate/conflicting values, and raw `camp devpod --` execution.                                                                                                 |
| **P1**   | Existing devcontainer and fallback image | Camp overwrites user configuration or silently drifts from the locked Wolfi digest.                                    | Existing root/per-image devcontainer fixtures, no-devcontainer fallback in `.camp/runtime`, `secure` alias, explicit `--image`, explicit devcontainer path/ID, lock update, and offline reopen using the recorded digest.                                                 |
| **P1**   | Configuration and redaction              | Backend credentials enter the capsule, logs, journal, or JSON.                                                         | Full precedence matrix; two-phase remote config; mode checks; schema-aware redaction for environment, URLs, provider options, AWS fields, and subprocess output. Scan the built haul for credential fixtures.                                                             |
| **P1**   | Session selection                        | `camp sync` or `close` operates on the wrong open capsule.                                                             | Zero, one, and multiple active-session tests using cwd, configured default, explicit flags/environment, stale journals, and ambiguity errors.                                                                                                                             |
| **P1**   | Status/list/history/recover              | Commands report stale PIDs, omit orphaned generations, or cannot distinguish published versus cleanup-failed sessions. | Real active/stopped/dead services, PID reuse, branch history, orphaned uploaded generation, immutable metadata sidecars, pointer conflicts, and cleanup-only recovery.                                                                                                    |
| **P1**   | Human/JSON error contract                | Errors omit what remains safe or produce unstable JSON.                                                                | Golden human and versioned JSON output for every lifecycle failure class. Assert an exact recovery command and separately represented publication/cleanup outcomes.                                                                                                       |
| **P1**   | Explicit OCI cache exports               | Documentation implies opaque BuildKit caches are portable.                                                             | Build using an explicit registry cache export under `$CAMP_REGISTRY`, checkpoint, reopen clean, and prove the cache reference is retained. Assert dangling layers and unexported daemon cache are not claimed.                                                            |
| **P1**   | Room of Requirement matrix               | Camp passes simple images but fails Wolfi privileged nested Docker or a published variant.                             | Run every listed supported tag. Wolfi must edit a source file, build/tag a nested image, close, reopen clean, restore/run the image, and verify the source file. Compile and reopen a Rust crate with its existing `target`.                                              |
| **P1**   | No background filesystem synchronization | The supervisor accidentally evolves into a sync daemon.                                                                | Process/trace test showing the persistent supervisor performs only heartbeats, process supervision, log handling, and tunnels. No filesystem watcher or transfer occurs without explicit `sync`/`close`.                                                                  |
| **P2**   | Managed tools                            | Wrong fork/version is accepted or downloads are activated before checksum verification.                                | Linux amd64/arm64 fixtures; PATH acceptance/rejection; wrong checksum; interrupted download; concurrent bootstrap; atomic install; no shell pipeline; exact v0.26.1 DevPod and v2.0.1 Hauler.                                                                             |
| **P2**   | Doctor                                   | Doctor says “healthy” while the backend cannot perform CAS or the workspace cannot reach the registry.                 | Capability probes for Linux/arch, storage semantics, tool identity, SSH, rsync/tar, optional tmux/zoxide, engine/provider, ports, credentials, reverse-tunnel reachability, and Room image availability.                                                                  |
| **P2**   | Packaging and release                    | `brew install camp` leaves the user with an unusable command or missing completions/SBOM.                              | Clean Linux install from each package/archive, Homebrew tap fixture, first-use managed-tool bootstrap, bash/zsh/fish completion smoke tests, checksum verification, SBOM generation, vulnerability scan, race tests, and release binary smoke tests.                      |
| **P2**   | Documentation                            | Documented commands drift from the actual CLI or hide recovery limitations.                                            | Execute every transcript and command reference through docs/golden tests. Include file/Longhorn and MinIO examples, security model, explicit cache limits, failure guarantees, and the real generated command tree.                                                       |

---

# 5. Smallest safe implementation order with complete vertical behavior

The proposed order is too horizontal. Building all CLI/config/runner packages, then all archive packages, then all storage packages creates a long period of disconnected abstractions and delays discovery of integration failures. The first implementation increment should already close a real open–checkpoint–clean–reopen loop.

## 1. Freeze external contracts and safety schemas

Before lifecycle code:

* lock `skevetter/devpod v0.26.1` exactly and remove the old DevPod pin;
* lock Hauler `v2.0.1`;
* pin the Room fixture `v1.18.0`;
* execute small probe tests against the real binaries;
* define versioned schemas for `latest.json`, generation metadata, lease JSON, session journal, `.camp/images.json`, `.camp/capsule.yaml`, and `.camp/lock.yaml`;
* write ADRs for lineage-scoped leases, materialization ownership, registry overlay/snapshotting, two-phase configuration, immutable generation metadata, and the supervisor;
* implement only the minimum exact-binary resolver needed to run installed locked tools.

**Exit gate:** exact terminal DevPod argv and a real Hauler save/load round trip pass before broader package construction.

## 2. Build the first complete vertical: local DevPod + file backend + terminal

This increment should include, end to end:

* `camp init`, automatic local adoption, and first-open source selection;
* XDG paths, precedence, redaction, strict modes, and stable human/JSON result types;
* file-backend immutable objects, conditional pointers, lineage leases, branch pointers, history metadata, and remote readback verification;
* durable session journal, local operation lock, supervisor, lease heartbeat, and crash reconciliation;
* secure inner archive and deterministic Hauler manifest;
* real Hauler fresh-store creation, registry/fileserver startup, and safe registry snapshotting;
* real DevPod local provider with `--ide none --open-ide=false`;
* root-relative target resolution and terminal entry;
* local workspace no-op transport;
* Docker/Podman tagged-image capture, direct-registry catalog merge, checkpoint, restore, and retagging;
* `camp open`, `status`, `sync`, `close`, and `recover`;
* read-only mode, branch mode, default DevPod deletion, and `--keep-workspace`;
* failure injection after every transition.

**Exit gate:** on a clean fixture, `camp open ./SecondBrain`, edit a file, build and tag a nested image, `camp sync`, edit again, `camp close`, verify the Camp-owned materialization was removed only after publication, then reopen on another clean fixture and verify both file versions and the image. Repeat with a crash after every state transition.

Nothing smaller is a safe vertical slice because archive, pointer CAS, recovery, image retention, and cleanup ownership jointly determine whether `close` can destroy local state.

## 3. Complete the local command and IDE surface

Add around the proven session model:

* `camp attach [target]`;
* exact VS Code and VS Code Insiders behavior, including the nested-folder URI adapter;
* every directly exposed DevPod flag, repeated flags, aliases, `--devpod-arg`, and `--`;
* attach SSH forwarding, user, environment, agent, GPG, and terminal flags;
* `camp list`, `history`, full `recover`, `serve status/logs/restart`, and `images list/capture/restore`;
* `camp config show --effective --redact` and the explicit Wolfi lock update command;
* `camp provider …`, `camp devpod -- …`, and `camp hauler -- …`;
* stable help, completions, verbose rendering, and exact recovery errors;
* deterministic session selection and ambiguity handling.

**Exit gate:** every documented local-provider command executes in a golden or integration test, and the fake `code-insiders` test proves the exact remote URI without a redundant SSH shell.

## 4. Add the remote-provider vertical without changing checkpoint semantics

Implement:

* effective remote workspace-folder discovery;
* DevPod-generated SSH host use;
* supervised reverse forwards and in-workspace reachability checks;
* `rsync` mirror with deletes, modes, hardlinks, symlinks, and dotfiles;
* tar-over-SSH process pipeline fallback;
* protection of Camp transient paths;
* remote engine image capture through the tunneled registry;
* remote attach, sync, close, and recovery.

**Exit gate:** the same edit/build/sync/close/reopen scenario passes against an SSH-based remote fixture, including a nonstandard workspace folder, disabled rsync fallback, killed tunnel recovery, and proof that DevPod export/import was not used.

## 5. Add S3/MinIO and multi-writer backend hardening

Implement:

* endpoint, region, bucket, prefix, path-style, TLS, and default credential-chain support;
* conditional pointer and lease operations;
* immutable multipart generation upload;
* reliable remote verification that does not treat multipart ETag as SHA-256;
* interrupted multipart cleanup;
* pointer conflicts and branch recovery across independent Camp processes;
* Longhorn-mounted `file://` integration behavior.

**Exit gate:** two independent writers race from the same parent; exactly one pointer update succeeds, the losing generation remains recoverable, and the loser can publish to a branch. The complete open–close–reopen vertical passes through MinIO.

## 6. Finish managed tools, operational validation, packaging, and release evidence

Complete:

* automatic exact-version DevPod/Hauler bootstrap for Linux amd64/arm64;
* full `camp setup` and `camp doctor`;
* no curl-pipe-shell path;
* release archives/packages, Homebrew formula, checksums, SBOM, shell completions, and install smoke tests;
* the complete Room of Requirement image matrix;
* Rust `target` continuity;
* explicit registry cache-export documentation and test;
* race tests, vulnerability scans, release-build smoke tests, and credential-gated CI separation;
* README, architecture document, ADRs, troubleshooting, backend setup, release/install docs, contribution guide, compact architecture diagram, and tested transcripts;
* an independent requirement-by-requirement audit against the build prompt.

**Exit gate:** from a clean supported Linux machine, the packaged Camp binary provisions its exact tools, opens through file and MinIO backends, supports terminal and VS Code Insiders nested targets, captures and restores named images, safely publishes and cleans up, and recovers every injected crash state.

---

The corrected architecture remains a small Go lifecycle CLI around DevPod and Hauler. The crucial change is that it becomes a **recoverable publication protocol with explicit ownership, lineage, and durable postconditions**, rather than a collection of wrappers connected by an optimistic sequence of calls. Without those corrections, the implementation would be most fragile precisely where the product promises to be safest: final checkpoint, remote conflict, image retention, and destructive cleanup.
