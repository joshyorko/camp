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
