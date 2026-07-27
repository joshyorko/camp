# DevPod and Hauler Behavior

## Locked tools

`tools.lock.yaml` is authoritative for supported assets and digests. The current contracts are `skevetter/devpod` v0.26.1 and `hauler-dev/hauler` v2.0.2. Hauler v2.0.1 must not be accepted: a real save/load probe dropped digest-addressed image descriptors from the loaded store, while v2.0.2 preserved the same named-image descriptors and digests; v2.0.2 includes the upstream digest-artifact copy fixes.

Do not use Hauler v2.0.2's live `_catalog` response as proof that all direct registry pushes were inventoried. The server rejects positive `n` pagination values and may return an empty unpaginated repository list while tag endpoints and the distribution storage tree contain pushed content. Camp derives each checkpoint's complete transported-image inventory exclusively from tagged references in the immutable registry cut.

## Implemented adapter behavior

- DevPod command construction preserves context, workspace identity, repeated public flags, environment variables, and argument boundaries through `ports.Command`.
- The pinned v0.26.1 installed-tool contract supports one bounded local-folder
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
- A terminal `camp open` constructs this ordered argv: `devpod up --ide none --open-ide=false --context <context> --id <workspace-id> --provider <provider> --devcontainer-path <capsule-relative-devcontainer-path> --workspace-env CAMP_REGISTRY=<registry-endpoint> --workspace-env CAMP_FILESERVER=<fileserver-endpoint> --workspace-env CAMP_CAPSULE=<capsule> --workspace-env CAMP_CHECKPOINT=<opened-generation-or-empty> <resolved-root>`. `internal/app/open_test.go:377` proves that the application supplies the capsule-relative devcontainer path; `TestTask3ScopedWorkspaceEnvironmentAndArgvExecution` preserves the adapter's ordered environment argv, and `TestExactDevPodArgv` preserves terminal IDE selection.
- Raw DevPod and Hauler passthrough is fail-closed. The adapters allow only exact, effect-free `version`, `help`, and `--help` invocations; known lifecycle, session, provider, store, and service commands are denied; reserved configuration, environment, identity, and store flags are conflicts; malformed and unknown argv is rejected before the runner. Passthrough accepts no environment map, so it cannot replace Camp-owned environment.
- Remote return resolves the DevPod workspace folder, attempts an rsync mirror into a fresh local staging root, and permits tar-over-SSH fallback only for classified fallback-eligible failures. Failed staging attempts are discarded.
- `sshtransfer.Executor` runs rsync without a shell and connects the SSH tar producer to the local tar consumer with an OS pipe. The tar consumer requires GNU tar options `--same-permissions` and `--delay-directory-restore`; BusyBox tar is not a valid fallback dependency.
- The real transfer gate covers exact bytes, permission modes, relative symlinks, hard-link inode identity, Unicode and spaces, `.camp/build` and `.camp/runtime` exclusions, rsync deletion, and a 2 MiB file. Forced fallback uses a missing rsync executable so fallback classification and the tar pipe are exercised rather than mocked.
- Checkpoint mirror intent IDs are the durable attempt anchor. Rsync and tar destinations derive distinct attempt IDs from that anchor, and successful facts persist the returned root, resolved remote root, method, and exclusions. A partial transfer is outcome-unknown: retain its exact staging destination, record an ambiguous mirror fact, and block publication and blind retry until recovery observes it. A controller death after intent but before fact leaves the serialized attempt pending and likewise starts no second transfer.
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
- `internal/adapters/supervisor/confinement.go`
- `integration/contracts_test.go`
