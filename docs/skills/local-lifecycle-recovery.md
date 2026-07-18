# Local Lifecycle and Recovery

## Current boundary

The application packages contain typed open, checkpoint, sync, close, attach, and supervision use cases, but `cmd/camp/main.go` currently exposes only the root Cobra command. Package tests do not prove a runnable local `camp open` to `camp close` vertical.

Local workspace return is accepted only when the provider is marked local and the canonical staging root equals the canonical workspace-local folder. `internal/workspace/local.go` returns `MirrorLocalNoop`; it does not copy data. The checkpoint publisher currently rejects every other mirror mode in `internal/app/checkpoint.go`, even though `internal/workspace/remote.go` can produce a `MirrorDevPodSSH` staging root.

## Recovery rules

- Treat a pending journal intent as reconciliation work, not permission to repeat a side effect.
- Do not clean up adopted content. Camp-created materializations require matching canonical path, ownership marker, device, and inode evidence.
- After a successful checkpoint build, the application recovery command is `camp recover <session-id>`; that command is not yet wired into the public CLI.
- Ownership-marker tests that exercise the named temporary-file substitution path must inject `createNamedOwnershipMarkerTemporary`. Linux filesystems may allow the production `O_TMPFILE` path and therefore produce no temporary filename.

## Evidence

- `cmd/camp/main.go`
- `internal/app/checkpoint.go`
- `internal/workspace/local.go` and `internal/workspace/local_test.go`
- `internal/workspace/remote.go` and `internal/workspace/remote_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
