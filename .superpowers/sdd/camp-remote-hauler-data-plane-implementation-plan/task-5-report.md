# Task 5 report: remote open data plane

## Result

- New non-local opens persist `haulerKitV1` plus one stable attempt ID before
  preparing the data plane.
- Production composition uses the lock-verified Hauler executable and version,
  the confinement-resolved pasta executable and observed version, the exact
  running Camp executable, the root archive adapter, a fresh Hauler store, the
  Kit v1 builder/verifier, immutable image resolution, and the Task 4 bootstrap
  renderer.
- Build, independent verification, and render complete before exactly one
  bootstrap-source `DevPod.Up`.
- Workspace reconciliation records and observes the bootstrap source root.
  Unknown `DevPod.Up` outcomes reuse the recorded completed kit attempt and do
  not construct another logical kit or call `Up` again.
- Local providers keep capsule-source behavior. Existing schema-v1 snapshots
  with no remote-data-plane record remain on the legacy lifecycle without an
  in-place schema change.

## TDD evidence

- RED: the remote-open test failed because the preparer received no stable
  attempt ID.
- RED: preparer tests initially failed to compile because the concrete
  production preparer did not exist.
- GREEN: `rtk go test ./internal/app ./internal/cli ./internal/domain
  ./internal/haulkit -count=1` passed 573 tests.
- GREEN: `rtk go vet ./internal/app ./internal/cli ./internal/domain
  ./internal/haulkit` passed.
- GREEN: `rtk git diff --check` passed.
- GREEN: `rtk go test ./... -count=1` passed 1,672 tests across 45
  packages.
- GREEN: `rtk go vet ./...` and `rtk go build ./cmd/camp` passed.

## Remaining uncertainty

- The unit, composition, and repository-wide suites do not prove a real remote
  provider completed user hydration with the new kit path; that remains an
  installed pinned-tool lifecycle gate.

## Review fix round 1

- Reentry now verifies the complete bootstrap source before it can reach
  `DevPod.Up`: exact tree and file types, kit bytes, helper bytes and executable
  mode, immutable image, generated lifecycle configuration, and all three
  coherent remote-worker requests.
- Bootstrap sources enforce at most 16 regular files and at most 1 MiB combined
  devcontainer/request metadata. The exact Camp helper is independently
  identity- and size-bound and excluded from the metadata budget; the reviewed
  helper was 20.1 MiB. The kit is the bulk capsule payload, not the only
  potentially large regular file.
- Attempt creation publishes an owner marker with the directory. A completion
  marker is fsynced and published last. Recovery reuses verified complete
  attempts; exact Camp-owned partial attempts are descriptor/inode checked,
  quarantined, and removed before rebuilding the same attempt ID. Unowned
  directories are preserved.
- Production-seam coverage now runs the real root archiver, Kit v1
  builder/verifier, bootstrap renderer/verifier, and DevPod command builder
  using deterministic runtime/store/command seams. Tampered complete sources,
  owned partial recovery, unowned preservation, and nonzero-exit diagnostics
  have dedicated regressions.
- RED evidence: complete-bootstrap tests initially failed because no reusable
  verifier existed; the production preparer rejected fake incomplete renders
  once verification was wired; and reentry coverage required the application
  to call the preparer again for a recorded complete attempt.
- GREEN evidence: the affected-package gate passed 672 tests across
  `internal/app`, `internal/capsule`, `internal/cli`, `internal/domain`, and
  `internal/haulkit`. `rtk go test ./... -count=1` passed 1,687 tests across 45
  packages; `rtk go vet ./...`, `rtk go build ./cmd/camp`, and
  `rtk git diff --check` passed.

## Review fix round 2

- `RemoteDataPlaneRecord` and the completion marker now persist the exact
  remote-worker schema, session, workspace root, runtime root, manifest path,
  architecture, and generated `devcontainer.json` SHA-256 and size.
- Initial preparation derives those values from the rendered source and records
  them only after complete verification. Reentry reconstructs its verification
  request from the persisted record rather than trusting mutually coherent
  files in the bootstrap directory.
- Bootstrap verification now hashes the full config bytes and requires every
  decoded request to match the persisted scope. Regressions replace all three
  requests with a coherent alternate scope and alter lifecycle command
  semantics while retaining the recognizable helper boundary; both fail.
- Kit builder runtime-probe failures and identity mismatches now use separate
  diagnostics. A mismatch no longer formats a nil error with `%w`.
- RED evidence: the new verifier request did not compile until persisted scope
  and config expectations existed, and the builder mismatch regression exposed
  `probe hauler runtime identity: %!w(<nil>)`.
- GREEN evidence: the affected-package gate passed 674 tests across five
  packages. `rtk go test ./... -count=1` passed 1,689 tests across 45 packages;
  `rtk go vet ./...`, `rtk go build ./cmd/camp`, and `rtk git diff --check`
  passed.
