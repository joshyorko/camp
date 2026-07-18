# DevPod and Hauler Behavior

## Locked tools

`tools.lock.yaml` is authoritative for supported assets and digests. The current contracts are `skevetter/devpod` v0.26.1 and `hauler-dev/hauler` v2.0.1.

## Implemented adapter behavior

- DevPod command construction preserves context, workspace identity, repeated public flags, environment variables, and argument boundaries through `ports.Command`.
- Raw DevPod and Hauler passthrough is fail-closed. The adapters allow only exact, effect-free `version`, `help`, and `--help` invocations; known lifecycle, session, provider, store, and service commands are denied; reserved configuration, environment, identity, and store flags are conflicts; malformed and unknown argv is rejected before the runner. Passthrough accepts no environment map, so it cannot replace Camp-owned environment.
- Remote return resolves the DevPod workspace folder, attempts an rsync mirror into a fresh local staging root, and permits tar-over-SSH fallback only for classified fallback-eligible failures. Failed staging attempts are discarded.
- `sshtransfer.Executor` runs rsync without a shell and connects the SSH tar producer to the local tar consumer with an OS pipe. The tar consumer requires GNU tar options `--same-permissions` and `--delay-directory-restore`; BusyBox tar is not a valid fallback dependency.
- The real transfer gate covers exact bytes, permission modes, relative symlinks, hard-link inode identity, Unicode and spaces, `.camp/build` and `.camp/runtime` exclusions, rsync deletion, and a 2 MiB file. Forced fallback uses a missing rsync executable so fallback classification and the tar pipe are exercised rather than mocked.
- Checkpoint mirror intent IDs are the durable attempt anchor. Rsync and tar destinations derive distinct attempt IDs from that anchor, and successful facts persist the returned root, resolved remote root, method, and exclusions. A partial transfer is outcome-unknown: retain its exact staging destination, record an ambiguous mirror fact, and block publication and blind retry until recovery observes it. A controller death after intent but before fact leaves the serialized attempt pending and likewise starts no second transfer.
- Logical mirror attempts increase durably across every sync and final close; attempt-scoped journal IDs must not repeat within a session. The remote transport requires the persisted request workspace ID and DevPod context to exactly match its composition identity, then uses the persisted values for root resolution and transfer commands. Mismatch fails before resolution or staging.
- Transfer command environment overrides replace inherited keys and remain deterministically ordered. Outcome-unknown errors are safe to format and unwrap even through a typed-nil pointer. Missing tar fallback is transport unavailability, not evidence that the persisted workspace is non-remote.
- Hauler adapters build `load`, `extract`, `sync`, `add image`, `save`, `info`, registry, and fileserver commands. Generation assembly runs save, loads the result into a fresh store, and inspects it before accepting the artifact.
- Registry and fileserver services are supervised as exact typed definitions. Registry readiness is `/v2/`; fileserver readiness is `/`. The Hauler process is not itself loopback-only, so Camp's `PastaLoopback` confinement is part of the safety boundary.

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
