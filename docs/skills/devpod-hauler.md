# DevPod and Hauler Behavior

## Locked tools

`tools.lock.yaml` is authoritative for supported assets and digests. The current contracts are `skevetter/devpod` v0.26.1 and `hauler-dev/hauler` v2.0.2. Hauler v2.0.1 must not be accepted: a real save/load probe dropped digest-addressed image descriptors from the loaded store, while v2.0.2 preserved the same named-image descriptors and digests; v2.0.2 includes the upstream digest-artifact copy fixes.

Do not use Hauler v2.0.2's live `_catalog` response as proof that all direct registry pushes were inventoried. The server rejects positive `n` pagination values and may return an empty unpaginated repository list while tag endpoints and the distribution storage tree contain pushed content. Camp derives each checkpoint's complete transported-image inventory exclusively from tagged references in the immutable registry cut.

## Implemented adapter behavior

- DevPod command construction preserves context, workspace identity, repeated public flags, environment variables, and argument boundaries through `ports.Command`.
- Bootstrap source mode is currently an adapter-only contract; production
  `app.Open` still uses capsule mode and does not construct, select, journal, or
  clean a bootstrap root. The adapter resolves both source identities before
  execution, rejects missing, non-directory, aliased, or nested
  bootstrap/capsule roots, and passes the canonical absolute bootstrap path to
  DevPod. `os.SameFile` proves exact observable filesystem identity and
  resolved paths prove lexical nesting; this does not prove isolation from
  privileged bind-mounted descendants or mount/path replacement after
  validation. Production `app.Open` must close that ownership and time-of-use
  boundary before selecting bootstrap mode. Default and explicit capsule modes
  retain the existing capsule source.
- The pinned v0.26.1 installed-tool contract proves one bounded local-folder
  upload: construct `.camp-bootstrap/devcontainer.json` and the immutable
  `camp-hauler-kit.tar.zst` completely before `devpod up`, pass only that
  disposable bootstrap root, and perform no post-`up` mutation or ownership
  repair of bootstrap contents; delete only the exact disposable root after
  workspace cleanup. The real Docker-provider gate uploaded a valid kit larger
  than 1 MiB, ran a
  `postCreateCommand` that hashed it after upload, returned the matching receipt
  through structured `devpod ssh`, independently matched the remote archive's
  SHA-256, and recorded the bootstrap root rather than the capsule root as the
  DevPod source. This proves upload and hook ordering only; selected capsule
  devcontainer activation remains a later hydration concern.
- Camp's remote bootstrap renderer publishes one new private source root with
  `.camp-bootstrap/{devcontainer.json,camp-bootstrap,initialize-request.json,hydrate-request.json,services-request.json}`
  and `camp-hauler-kit.tar.zst`; it refuses to replace an existing root. The
  helper and kit are copied through their verified regular-file descriptors,
  then the staged copies are independently reverified before publication. Input
  ancestors and the output parent are opened component-by-component without
  following symlinks; staging and no-replace publication stay anchored to
  directory descriptors. After a failed parent fsync, rollback exchanges the
  target with an owned placeholder and verifies the exchanged inode before
  cleanup. An unchanged target is removed through that verified inode; a
  replacement target is atomically restored and preserved. The generated config
  pins the supplied immutable outer image, and all three operation
  requests must share schema, session, workspace root, runtime root, manifest
  path, and expected identities. Generated
  `initializeCommand`, `onCreateCommand`, and `postStartCommand` run
  `activateImage`, `hydrate`, and `startServices` respectively before the
  corresponding user hook. String, argv-array, and named-object lifecycle values
  retain their top-level JSON form, and each original command or argv appears
  exactly once behind a fail-closed helper boundary. String hooks use a
  same-shell `helper || exit $?` prelude followed by the original text; argv
  hooks retain their argument boundaries. A named-object hook becomes one
  reserved named entry. That entry runs the worker once, then launches every
  original named string or argv as a separate concurrent child, waits for all
  children, and returns failure if any child fails. The owner process therefore
  supplies the lifecycle invocation identity: no mutable current-generation
  file, waiter process, stale receipt, timing window, or inferred PID group can
  authorize commands from another invocation. A worker failure or crash starts
  no user child. Overlapping invocations own disjoint child sets and share only
  their normal command output destinations.
  Pinned DevPod v0.26.1 commit
  `86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e` decodes lifecycle objects in
  `pkg/types/types.go` and executes decoded commands in
  `pkg/devcontainer/setup/lifecyclehooks.go`; neither boundary exposes a stable
  per-invocation token through command arguments or environment. Do not
  reintroduce a cross-process named-hook gate without a newer authoritative
  execution contract that supplies such an identity.
  Sorting object keys does not establish lifecycle ordering. Null, mixed,
  recursively duplicate-key, malformed, and build-only
  configurations fail before publication, and the original devcontainer file
  remains unchanged.
  `internal/capsule/bootstrap_test.go` executes the generated commands using
  concurrent object semantics and verifies crash isolation, stale-file
  irrelevance, overlapping invocation ownership, concurrent completion,
  aggregate failure, and command traces rather than inspecting renderer source.
- Bootstrap acceptance and reentry re-open the published source and verify its
  complete shape before `devpod up`: exactly the expected seven regular files and
  one real private directory, no links or extra entries, the copied kit digest
  and size, the exact executable helper digest and size, the immutable image,
  all three operation-specific request files with one coherent scope, and the
  generated lifecycle boundaries. The source stays below the 16-regular-file
  limit. The devcontainer and three request documents have a combined 1 MiB
  limit. The kit is the bulk capsule payload; the exact Camp helper is a
  separately accounted runtime payload, excluded from that metadata budget and
  independently identity-bound. The reviewed production helper was 20.1 MiB,
  so guidance must not claim the kit is the source's only large regular file.
  The durable data-plane record and completion marker also bind the remote
  worker protocol schema, session, workspace root, runtime root, manifest path,
  architecture, and the full generated `devcontainer.json` SHA-256 and size.
  Reentry supplies those persisted expectations to bootstrap verification.
  Three mutually coherent request files with a different scope, or any
  lifecycle/config byte change, therefore fail even when their internal
  identities and helper substrings remain plausible.
- Production remote opens now persist the `haulerKitV1` data-plane selection
  and stable attempt ID before preparation. The preparer snapshots the hydrated
  root into a fresh Hauler store, adds the resolved immutable devcontainer
  image, resolves the already-validated pasta executable through the
  confinement boundary, builds and independently verifies Camp Hauler Kit v1,
  then renders and completely verifies the disposable bootstrap source. Build,
  kit verification, render, or bootstrap verification failure occurs before
  `devpod up`. A durable completion marker is published only after every
  artifact and bootstrap check succeeds. Reentry revalidates a completed
  attempt before use. An incomplete attempt is rebuilt under the same persisted
  attempt ID only after Camp verifies its owner marker, directory descriptor,
  inode, and quarantine identity; unowned paths are preserved. The
  `WorkspaceUp` intent records the bootstrap source root, so reconciliation
  observes the exact DevPod source and never issues a second `up`.
- A missing `Recovery.RemoteDataPlane` record is the legacy routing marker.
  Existing schema-v1 sessions with no record keep the capsule-source lifecycle
  and are not upgraded in place. This compatibility marker does not change the
  shared domain schema version.
- `camp __remote-worker` is a hidden, stdin/stdout-only protocol entrypoint. It
  accepts one schema-v1 JSON request with strict unknown-field, recursively
  duplicate-key, operation,
  absolute-path, immutable-image, architecture, and helper/kit/manifest
  identity validation. `activateImage`, `hydrate`, `startServices`, and
  `checkpoint` are implemented; other unimplemented mutation operations remain
  typed `unsupported`. The bootstrap carries the
  canonical manifest as a seventh descriptor-verified file so provider-side
  activation and container-side hydration can independently verify the uploaded
  archive. `probe` verifies the running helper plus adjacent kit
  and manifest bytes before reporting typed architecture, filesystem,
  namespace, TUN, privilege, and loopback-port capabilities; an unsupported
  capability returns a failing receipt. The namespace probe actually creates a
  child user, network, and mount namespace mapped to the current user; the TUN
  device must open and answer `TUNGETFEATURES`. The privilege receipt derives
  from those operation results, not UID or blanket capability bits, so an
  operation-capable rootless user is accepted. Loopback availability uses a
  bind-only socket whose `SO_ACCEPTCONN` state remains false; the probe never
  calls `listen`. Every accepted, malformed, or identity-rejected request
  returns exactly one schema-v1 result envelope; failures use a typed receipt and
  the `rejected` result operation when no request operation could be decoded.
  Workspace-up diagnostics bound raw DevPod output before sanitization, redact
  unlabeled secret-like lines and bare credential-shaped values such as
  `ghp_...`, and escape bidi/format controls before any surfaced truncation
  note. Camp also enforces a final rendered 8 KiB cap after escaping. If
  settlement of a failed `devpod up` also fails, Camp keeps the bounded
  diagnostic evidence in the returned error alongside the settlement error.
  Readiness polling starts its timeout before the first status probe and
  reuses that context for every probe. Protocol output is capped at 64 KiB and
  never contains archive bytes.
- Remote activation distinguishes the source manifest identity from the local
  engine identity. The selected OCI manifest's config digest becomes the
  generated devcontainer image (`sha256:<config-digest>`). Before DevPod's
  Docker driver inspects that image, `initializeCommand` verifies and extracts
  the complete Kit v1, exposes its ready store through an exact pinned-Hauler
  registry behind pinned-pasta IPv4 loopback confinement, pulls the source
  manifest digest, and requires Docker inspection to return the expected local
  image ID. The temporary registry is stopped before a no-replace activation
  receipt is published. A non-Docker provider engine fails as an unsupported
  capability; there is no provider-plugin or network-pull fallback.
- Container-side `hydrate` repeats helper, kit, manifest, tool, architecture,
  store, and root verification. The persisted/bootstrap manifest digest is a
  required verifier authority at preparation, reentry, provider activation,
  and container hydration; callers must not infer it from the untrusted
  manifest being verified. Before the verifier can create runtime-root state,
  root extraction, or `.camp/runtime` installation, hydration admits the
  workspace descriptor and accepts only `.camp-bootstrap` and `.camp/runtime`;
  an ineligible workspace receives no runtime-root, root-stage, or workspace
  mutation. It then extracts the root artifact through
  the existing Hauler and archive adapters into a private stage, installs the
  exact Camp, Hauler, and pasta bytes beneath `.camp/runtime`, and promotes
  entries with descriptor-relative no-replace renames. Hydrated `.camp`
  content is merged without replacing runtime. Each rename fsyncs both
  directories, and the durable hydration receipt is published only after the
  root is complete. Reentry first performs only a descriptor-pinned, no-follow
  receipt and trusted-manifest read; it succeeds without admission or verifier
  mutation only when the completed receipt exactly binds the request session,
  workspace/runtime roots, manifest path, every expected identity, and a root
  digest equal to the root in canonical manifest bytes whose descriptor-pinned
  SHA-256 and size match `Expected.Manifest`. Digest syntax alone is never
  authority. Missing, malformed, stale, mismatched, replaced, or linked
  evidence is not adopted and falls through to admission before any mutation.
  The generated lifecycle boundary
  releases the preserved user `onCreateCommand` only after that success. It
  never calls `devpod up` again or falls back to the capsule source.
- Do not attempt to activate a devcontainer configuration created only in the
  remote workspace with a second local-folder `devpod up --recreate`. Pinned
  v0.26.1 exposes no supported community command for that remote reconfiguration
  seam, and the real second-up characterization resolved the selected config
  beneath the recorded local source instead. The first Docker-provider `up`
  changed that disposable source to `uid=0`, `gid=0`, mode `0700`; the recreate
  failed `stat .../.devcontainer/devcontainer.json: permission denied` even
  though structured remote hydration had succeeded. Treat the source as
  disposable and clean it by exact ownership scope; do not chmod or chown it to
  manufacture a recreate contract.
- A local-provider or legacy terminal `camp open` constructs this ordered argv:
  `devpod up --ide none --open-ide=false --context <context> --id <workspace-id>
  --provider <provider> --devcontainer-path
  <capsule-relative-devcontainer-path> --workspace-env
  CAMP_REGISTRY=<registry-endpoint> --workspace-env
  CAMP_FILESERVER=<fileserver-endpoint> --workspace-env CAMP_CAPSULE=<capsule>
  --workspace-env CAMP_CHECKPOINT=<opened-generation-or-empty>
  <resolved-root>`. A new non-local `haulerKitV1` open preserves the same
  identity and environment arguments but supplies
  `.camp-bootstrap/devcontainer.json` and the recorded disposable bootstrap
  root. `internal/app/open_test.go:377` proves the capsule-relative local path;
  `TestOpenRemoteUsesPreparedBootstrapSourceForExactlyOneDevPodUp` proves the
  remote source switch; `TestTask3ScopedWorkspaceEnvironmentAndArgvExecution`
  preserves ordered environment argv; and `TestExactDevPodArgv` preserves
  terminal IDE selection.
- Raw DevPod and Hauler passthrough is fail-closed. The adapters allow only exact, effect-free `version`, `help`, and `--help` invocations; known lifecycle, session, provider, store, and service commands are denied; reserved configuration, environment, identity, and store flags are conflicts; malformed and unknown argv is rejected before the runner. Passthrough accepts no environment map, so it cannot replace Camp-owned environment.
- Remote return resolves the DevPod workspace folder, attempts an rsync mirror into a fresh local staging root, and permits tar-over-SSH fallback only for classified fallback-eligible failures. Failed staging attempts are discarded.
- `sshtransfer.Executor` runs rsync without a shell and connects the SSH tar producer to the local tar consumer with an OS pipe. The tar consumer requires GNU tar options `--same-permissions` and `--delay-directory-restore`; BusyBox tar is not a valid fallback dependency.
- The real transfer gate covers exact bytes, permission modes, relative symlinks, hard-link inode identity, Unicode and spaces, `.camp/build` and `.camp/runtime` exclusions, rsync deletion, and a 2 MiB file. Forced fallback uses a missing rsync executable so fallback classification and the tar pipe are exercised rather than mocked.
- Checkpoint mirror intent IDs are the durable attempt anchor. Rsync and tar destinations derive distinct attempt IDs from that anchor, and successful facts persist the returned root, resolved remote root, method, and exclusions. A partial transfer is outcome-unknown: retain its exact staging destination, record an ambiguous mirror fact, and block publication and blind retry until recovery observes it. A controller death after intent but before fact leaves the serialized attempt pending and likewise starts no second transfer.
- A persisted `haulerKitV1` session never enters the legacy rsync or
  tar-over-SSH checkpoint mirror. Its generation-derived remote attempt ID is
  immutable across retry. Remote preparation verifies authority, observes that
  exact attempt, quiesces the recorded registry and fileserver, cuts the
  registry, inventories tagged images, archives the root, validates a fresh
  Hauler store, and builds one verified return Camp Kit. `sync` resumes the
  exact recorded services after the durable preparation receipt; `close`
  leaves them quiesced until inbound publication completes. The return
  fileserver authority is only the canonical manifest and its named 1 GiB
  chunks; the complete archive and enclosing directories are never valid
  allow-list entries. The supervised fileserver uses an otherwise empty store
  and serves only `.camp/transfer/export`; that root contains exactly one
  mode-0700 attempt directory whose descriptor-verified mode-0400 regular
  files have single-link, stable inode, size, and digest identity. Extra
  entries, symlinks, hardlinks, replacements, and directory drift fail closed.
  A prepared receipt is durable before export publication, so an
  outcome-unknown retry completes or adopts that same attempt and never builds
  or exposes a second logical Kit. Host download, permanent `hauler store save`, upload,
  pointer CAS, and acknowledgement are separate inbound-publication work and
  must not be inferred from `remotePrepared`.
- Before remote mutation the host re-reads the authoritative latest pointer
  after writer-lease revalidation. When a pointer is recorded, its revision,
  generation/current base, capsule, lineage, and complete document must exactly
  match the backend. An absent pointer requires an empty expected revision.
  Main may have no current base in that state; a new non-main lineage may
  retain its non-nil inherited source generation as `CurrentBase` without
  inventing a branch pointer. An unexpected live pointer, missing recorded
  pointer, or revision drift fails before DevPod execution. On the remote side
  the exclusive service lock is retained after
  exact service quiescence and through registry-cut creation plus tagged-image
  inventory. The lock is released only at that explicit immutable handoff and
  is released with cancellation removed on every failure path, so a concurrent
  service start or restore cannot interleave with the write barrier or cut.
- Logical mirror attempts increase durably across every sync and final close; attempt-scoped journal IDs must not repeat within a session. The remote transport requires the persisted request workspace ID and DevPod context to exactly match its composition identity, then uses the persisted values for root resolution and transfer commands. Mismatch fails before resolution or staging.
- Transfer command environment overrides replace inherited keys and remain deterministically ordered. Outcome-unknown errors are safe to format and unwrap even through a typed-nil pointer. Missing tar fallback is transport unavailability, not evidence that the persisted workspace is non-remote.
- Tar fallback streams producer stdout exclusively into the consumer pipe; archive bytes are never diagnostic output. Captured stdout/stderr diagnostics are capped at 64 KiB per process. Start the producer before the staging-mutating consumer so a producer start failure is a not-started attempt with no consumer mutation; retain staging only after evidence that the consumer may have started mutating it.
- Hauler adapters build `load`, `extract`, `sync`, `add image`, `save`, `info`, registry, and fileserver commands. Generation assembly runs save, loads the result into a fresh store, and inspects it before accepting the artifact.
- Registry and fileserver services are supervised as exact typed definitions. Registry readiness is `/v2/`; fileserver readiness is `/`. The Hauler process is not itself loopback-only, so Camp's `PastaLoopback` confinement is part of the safety boundary.
- The generated fallback Room keeps container networking enabled and mounts deterministic `iptables`, `iptables-save`, `iptables-restore`, and IPv6 compatibility entrypoints into `/usr/local/sbin`. Each entrypoint uses the legacy frontend only when its NAT table is usable and otherwise selects the image's installed nft frontend. On Bluefin, the pinned Room's legacy frontend failed because the legacy NAT table was absent; `iptables-nft` allowed dockerd 29.6.2 to start, a nested Alpine container reached the network, and the workspace built, ran, and pushed a named image through `CAMP_REGISTRY`. Do not replace this with `--iptables=false`: that would suppress the nested-engine networking contract rather than satisfy it.
- Camp captures OCI images explicitly pushed through `CAMP_REGISTRY`; it does
  not inspect the entire Docker, Podman, or containerd inventory and guess which
  images matter. A mutable engine tag is not durable image evidence: a
  fresh-controller reopen can restore the captured content without recreating
  that tag. Real acceptance records a registry-provided single-platform
  manifest digest, verifies the response body against that digest, removes the
  local tag and image ID before checkpointing, and records the evicted ID in the
  payload. After reopen it removes any restored copy of that exact ID, pulls
  `repository@sha256:...` through the reopened registry, requires that exact
  value in `RepoDigests`, and runs the digest-qualified image to its fixture
  marker.
- `camp images capture` is a compatibility command that fails before session
  mutation with stable guidance to push through `CAMP_REGISTRY` and run
  `camp sync` or `camp close`. It has no `--exclude-tag` option because engine
  tags are not capture inputs.
- Each real lifecycle scenario must give DevPod a unique test-owned
  `DEVPOD_HOME`, exact `DEVPOD_CONFIG`, non-default context, and
  `SSH_CONFIG_PATH` beneath that home. Pass the same environment and context to
  Camp and every direct `devpod list`, `ssh`, and `delete` command; Camp's XDG
  controller isolation does not isolate DevPod state or `~/.ssh/config`. Before
  the first Camp command, initialize the built-in Docker provider with
  `devpod provider add docker --context <private-context> --use --silent` under
  that same private environment and set `CAMP_DEVPOD_PROVIDER=docker`. The
  Room-of-Requirement remains the devcontainer image fixture; it is not the
  DevPod provider for these scenarios.
- Real lifecycle cleanup owns an exact workspace-ID ledger. It deletes only IDs
  returned by the scenario's opens or recovered from that scenario's private
  Camp controller journals. Enumerating a host-global context or using its
  workspace list as deletion input is prohibited. The named
  `TestPrivateDevPodContextPreservesUnrelatedWorkspace` gate remains missing
  real evidence until it creates an actual unrelated workspace and proves that
  exact-ID scenario cleanup preserves it. Private Docker-provider bootstrap is
  implemented and is no longer the blocker; ordinary test success still must
  not claim that an unrelated workspace survived cleanup.
- The RCC composite gate supplies one `CAMP_TEST_BINARY` to every executable
  lifecycle test so file, image, recovery, and cleanup evidence all describe
  the same candidate.
- On enforcing SELinux hosts, the packaged `pasta` executable transitions into `pasta_t`; an unwrapped Hauler child inherits that domain and can be denied `cgroup_t` search even though its private store is correctly owned. Camp keeps pasta in `pasta_t` but launches the exact Hauler child through the policy-authorized `/usr/bin/runcon -t unconfined_t` prefix, records that prefix in the confinement fingerprint and launch intent, and still validates the final Hauler PID, argv, namespace, guest listener, loopback mapping, and HTTP endpoint. Evidence: the Bluefin audit log recorded `comm="hauler" ... scontext=...:pasta_t ... tcontext=...:cgroup_t ... denied { search }`; the unwrapped service returned `stat .../store: permission denied`; the wrapped standalone probe returned HTTP 200 on `127.0.0.1:45999/v2/`; and the fresh real `camp open` reached and completed `devpod up` with both services live.
- A DevPod container cannot reach a host-only `127.0.0.1` registry or fileserver merely because Camp injected `CAMP_REGISTRY` and `CAMP_FILESERVER`. After `devpod up`, production composition starts one supervised `devpod ssh --reverse-forward-ports` process per endpoint, records its PID/start-time/executable/argv identity in `Recovery.Forwarding`, and probes the exact endpoint from the exact workspace before declaring it ready. Startup failure stops only the recorded forwarders; close stops them by recorded identity. Evidence: the unforwarded real workspace returned connection refused for `$CAMP_REGISTRY`; the production-composed workspace subsequently returned HTTP 200 for both `http://$CAMP_REGISTRY/v2/` and `http://$CAMP_FILESERVER/`, with two distinct forwarder process records in the session snapshot.
- A completed remote `haulerKitV1` hydration does not retain those workstation
  tunnels. Its generated `postStartCommand` invokes the descriptor-pinned Camp
  worker, which revalidates the completed hydration receipt, manifest, ready
  store, and installed Hauler/pasta bytes before service mutation. It serves
  the ready store plus a persistent writable registry overlay at
  `127.0.0.1:5000` and the ready store plus the private
  `.camp/transfer/export` allow-list root at
  `127.0.0.1:8080`. Each exact installed Hauler child runs behind its own exact
  installed pasta process in a distinct network namespace; pasta maps only
  IPv4 loopback to private guest ports `15000` and `18080`, disables UDP and
  namespace-to-host forwarding, and requires HTTP 200 at `/v2/` or `/`.
  On enforcing SELinux it opens the exact `/usr/bin/runcon` without following
  symlinks, requires an executable regular file, fingerprints the exact
  `runcon -t unconfined_t` prefix, and includes that prefix in the expected
  pasta argv. On non-enforcing systems the prefix is empty. The invocation
  worker launches one exact, one-shot Camp service-supervisor subprocess; their
  distinct full process records are validated as a parent/child pair and
  published as immutable per-invocation actor evidence, separately from the
  pasta-helper and Hauler-child service records. Actor publication opens every
  parent component descriptor-relatively with `O_NOFOLLOW`, stages bounded
  mode-0600 bytes only in an unlinked `O_TMPFILE`, fsyncs and validates that
  exact regular-file descriptor with link count zero, then publishes the exact
  open inode no-replace with `linkat(AT_EMPTY_PATH)` or the
  `/proc/self/fd/<fd>` plus `AT_SYMLINK_FOLLOW` exact-FD route. There is no
  named staging fallback: unsupported unnamed creation or unavailable exact-FD
  publication fails closed, and closing the unlinked descriptor reclaims every
  pre-publication failure without a cleanup syscall or directory entry.
  Existing evidence is idempotent only after the same bounded no-follow
  observer proves a private regular file whose open and named device, inode,
  size, mode, and link count remain stable. Confirmation retains that first
  identity, requires exact bytes with mode 0600 and link count one, fsyncs the
  parent, then requires a second stable observation of the same device and
  inode with the same exact shape and bytes. Exact-byte inode replacement on
  either side of the durability barrier and hardlinked evidence are preserved
  and rejected; the same confirmation applies after an `EEXIST` publication
  race. Fresh publication proves the canonical final and staging
  descriptor have the same device and inode, link count one, exact mode, size,
  and bytes both before and after the parent durability barrier. A parent-fsync
  failure after linking is an unknown outcome: the canonical final is left in
  place, and retry accepts it only after exact stable observation and a fresh
  parent fsync. `EEXIST`, symlinks, directories, oversized bytes, unequal
  content, or replacement races preserve the contested final and fail closed;
  actor publication never rolls back or deletes the canonical name.
  Invocation locking,
  durable start intents, and recorded PID/boot/start, executable, complete argv
  digest, PGID, SID, and network-namespace evidence let retries adopt an
  unknown-outcome start, observe an already-live unit without duplication, or
  identity-safely restore a stopped recorded unit. Reentry rebuilds the complete
  expected pasta argv, including the verified SELinux prefix and exact Hauler
  command, and compares its digest before observation or restart. Recorded
  command, confinement, endpoint, log, pidfile, worker, supervisor, argv, or
  digest drift fails closed.
- A live `devpod ssh` process is not sufficient forwarder readiness evidence: a
  fresh-controller file lifecycle run observed the process remain alive while
  its fileserver reverse tunnel never became reachable and its forwarder log
  remained empty. Camp keeps the in-workspace HTTP probe as the readiness
  boundary. If that bounded probe expires, it stops and removes evidence for
  only the exact PID/boot/start identity, launches one fresh process attempt,
  and journals only the identity whose endpoint becomes ready. Pending-start
  recovery polls the same endpoint while adopting the exact persisted process;
  it never launches a duplicate. Unit coverage proves replacement, adoption,
  retry exhaustion, and evidence cleanup. A complete real file lifecycle pass
  after this correction remains required release evidence.

Installed-tool tests in `integration/contracts_test.go` skip when the binaries are unavailable. A skip is not proof of real Hauler or DevPod behavior.

## Evidence

- `tools.lock.yaml`
- `internal/adapters/devpod/`
- `internal/adapters/devpod/passthrough.go`
- `internal/workspace/remote.go`
- `internal/adapters/sshtransfer/`
- `internal/adapters/hauler/`
- `internal/adapters/hauler/passthrough.go`
- `internal/app/open_remote_data_plane.go`
- `internal/capsule/bootstrap_verify.go`
- `internal/domain/remote_data_plane.go`
- `internal/haulkit/`
- `internal/adapters/supervisor/confinement.go`
- `internal/remoteworker/services.go`
- `internal/remoteworker/supervisor.go`
- `integration/contracts_test.go`
