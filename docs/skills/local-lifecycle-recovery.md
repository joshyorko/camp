# Local Lifecycle and Recovery

## Current boundary

The application packages contain typed open, checkpoint, sync, close, attach, and supervision use cases, but `cmd/camp/main.go` currently exposes only the root Cobra command. Package tests do not prove a runnable local `camp open` to `camp close` vertical.

Local workspace return is accepted only when the provider is marked local and the canonical staging root equals the canonical workspace-local folder. `internal/workspace/local.go` returns `MirrorLocalNoop`; it does not copy data. For a non-local provider, the checkpoint publisher accepts only `MirrorDevPodSSH`, builds from the returned nonempty staging root, and revalidates the writer lease after the mirror before recording the fact.

`app.NewCheckpointPublisher` accepts separate local and remote transports and always composes them through `workspace.NewSelector`. The selector uses the persisted `WorkspaceRecord.LocalProvider` value carried in `MirrorRequest`; it never probes or guesses locality. An open request that defaults the provider to Docker also persists `LocalProvider=true`, while an explicitly composed remote provider remains non-local. A missing matching transport fails closed. The public CLI still has no lifecycle commands, so application and close-path tests prove publication composition but not an executable CLI lifecycle.

## Recovery rules

- Treat a pending journal intent as reconciliation work, not permission to repeat a side effect.
- A pending `WorkspaceMirrored` intent blocks another checkpoint because its side-effect outcome is unknown. An ambiguous mirror fact does not block a later checkpoint: preserve its returned attempt ID/root as evidence, then allocate the next logical attempt and a fresh attempt-specific staging destination. Do not stop/delete the provider workspace or remove controller staging when mirroring or pointer CAS fails.
- `WorkspaceRecord.mirror` is a required value object in serialized snapshots; an empty `{}` truthfully means no mirror attempt has been recorded. Completed and ambiguous facts carry the durable logical-attempt number used to allocate the next unique checkpoint attempt after restart.
- Validate that the persisted workspace type has a matching composed transport before recording `WorkspaceMirrored`. For remote transports, validate the persisted workspace ID and DevPod context against the composed transport at the same pre-intent boundary. Missing composition or identity mismatch is an effect-free configuration error and must not create false pending reconciliation work.
- Remote close order is final publication, provider stop/delete, forwarders/services/supervisor, conditional lease release, then owned staging/materialization cleanup. `--keep-workspace` changes the provider action but does not make controller staging user-owned.
- Do not clean up adopted content. Camp-created materializations require matching canonical path, ownership marker, device, and inode evidence.
- After a successful checkpoint build, the application recovery command is `camp recover <session-id>`; that command is not yet wired into the public CLI.
- Ownership-marker tests that exercise the named temporary-file substitution path must inject `createNamedOwnershipMarkerTemporary`. Linux filesystems may allow the production `O_TMPFILE` path and therefore produce no temporary filename.
- Service status must call `ServiceSupervisor.Observe`; journaled `ready` state is not live evidence. Observation reconstructs the exact recorded Pasta/Hauler command, inspects the recorded helper and child identities, and reruns listener/readiness checks. PID reuse and listeners remaining after a stopped helper fail closed. The stopped check is read-only; PID-file removal remains exclusive to the explicit absence/cleanup path.
- Service restart records `ServiceRestart` before stopping anything, stops through the identity-checked child-first supervisor path, and reuses only the recorded command and endpoint contract with a new launch token. Application restart holds the session operation lock and runs the ownership/session/lease safety guard before calling the supervisor.
- Service logs are addressed by a journal-owned service name, never a caller path. `ServiceLogReader` accepts only a regular, private, single-link file below its configured canonical log root, rejects symlinks and path escape, and returns at most the configured tail bound.
- Image list output is derived from the selected session's persisted inventory and carries the session, capsule, branch, and checkpoint-base generation/digest; image and tag arrays are sorted before presentation.
- Image capture and restore hold the session operation lock, reject unrelated pending work, revalidate ownership/session/lease state, and prove the recorded registry service live immediately before engine or registry effects. If the selected session identity changes after lock acquisition, the operation releases the lock and returns the canonical `ErrRecoveryIdentityChanged` before guard, engine, or registry effects. Capture persists only a schema-valid inventory whose entries have captured references and verified manifest digests. Restore accepts only that selected session inventory, never caller-supplied lineage.
- A retry may reconcile one exact pending `ImagesCaptured` intent. If the deterministic captured reference already exists without a journal fact, capture adopts it only when the current workspace engine proves the same local image ID exposes the registry's resolved repository digest; otherwise it remains a collision and fails closed. Restore is digest-verified and idempotent for already-correct original tags.

## Evidence

- `cmd/camp/main.go`
- `internal/app/checkpoint.go`
- `internal/workspace/local.go` and `internal/workspace/local_test.go`
- `internal/workspace/remote.go` and `internal/workspace/remote_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
- `internal/adapters/supervisor/supervisor.go`, `logs.go`, and their focused tests
- `internal/app/serve.go` and `internal/app/serve_test.go`
- `internal/images/capture.go`, `restore.go`, and their focused tests
- `internal/app/images.go` and `internal/app/images_test.go`
