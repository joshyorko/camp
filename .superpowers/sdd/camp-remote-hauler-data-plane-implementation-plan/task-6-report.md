# Task 6 report

Status: DONE_WITH_CONCERNS

## Result

- `activateImage` now verifies the uploaded helper, manifest, Kit v1 archive,
  architecture, exact tools, ready Hauler store, and root identity before
  provider image activation.
- The remote-open data plane records both the source digest-qualified image and
  the OCI config-digest local image ID. The generated devcontainer uses the
  local immutable ID.
- Provider activation starts the exact kit pasta and Hauler runtimes, confines
  a temporary readonly registry to IPv4 loopback, pulls the source manifest
  through Docker, verifies the resulting local image ID, stops the registry,
  and publishes an idempotent durable receipt.
- `hydrate` repeats Kit verification in the container, extracts the root through
  the Hauler and archive adapters, installs exact runtimes below
  `.camp/runtime`, promotes with descriptor-relative no-replace renames while
  preserving only bootstrap/runtime, and publishes completion last.
- The existing generated lifecycle boundary releases the preserved user hook
  only after worker success. Task 7 operations remain unsupported. No second
  `DevPod.Up` or capsule-source fallback was added.

## TDD evidence

- RED: bootstrap verification lacked a manifest payload and
  `BootstrapVerification.Manifest`; GREEN: the seventh descriptor-pinned file
  is copied and independently verified.
- RED: secure root promotion did not exist; GREEN: preservation, `.camp` merge,
  unexpected-entry rejection, and no-replace promotion tests pass.
- RED: Task 6 worker dispatch did not exist; GREEN: activation/hydration route
  through injected operations while Task 7 remains unsupported.
- RED: the Docker manifest resolver could not derive the local image ID; GREEN:
  it validates and returns the OCI config digest without a shell.
- RED: activation orchestration types did not exist; GREEN: tests prove
  verify-before-registry ordering, exact pull/inspect identity, stop-before-
  receipt ordering, and no success receipt on mismatch.
- RED: hydration orchestration did not exist; GREEN: tests prove
  verify/extract/install/promote/receipt ordering.

## Verification

- `rtk go test ./internal/remoteworker ./internal/capsule ./internal/app ./internal/domain -count=1`
  — 369 passed.
- `rtk go vet ./internal/remoteworker ./internal/capsule ./internal/app ./internal/domain`
  — passed.
- `rtk git diff --check`
  — passed.

## Remaining uncertainty

- A fresh installed pinned-tool provider run has not exercised the temporary
  Hauler registry, pasta mapping, Docker pull, and `sha256:<config-digest>`
  DevPod inspection together. That remains the Task 12 release gate.
- Promotion fsync failures are reported as unknown outcomes and never publish
  completion. The next retry observes the durable receipt or fails closed on
  unexplained partial workspace entries; it does not blindly overwrite them.

## Documentation improvement

- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: the seventh manifest payload, source-image versus
  local-image identity, provider-host activation sequence, container hydration
  sequence, exact preservation set, user-hook ordering, and Task 7 boundary.
- Evidence: focused executing tests in `internal/remoteworker`,
  `internal/capsule`, `internal/app`, and `internal/domain`.
- Stale or ambiguous guidance removed: the six-file bootstrap count and the
  claim that every mutation operation is unsupported.
- Remaining uncertainty: real pinned-provider activation and hydration remain
  Task 12 release evidence.
