# Task 7 report

Status: DONE_WITH_CONCERNS

## Result

- `startServices` is now an implemented remote-worker operation. The generated
  `postStartCommand` reaches it through the existing request/response protocol;
  no listener or second DevPod operation was added.
- Every invocation first proves the descriptor-pinned completed hydration
  receipt, exact manifest and ready store, and SHA-256/size identity of the
  installed workspace Hauler and pasta executables.
- The exact installed workspace Hauler serves:
  - the ready Kit store plus writable `.camp/runtime/registry` through registry
    guest port `15000`, mapped only to `127.0.0.1:5000`;
  - the ready Kit store plus `.camp/transfer` through fileserver guest port
    `18080`, mapped only to `127.0.0.1:8080`.
- Both services use the existing exact Hauler v2 service-definition adapter and
  `PastaLoopback` supervisor. UDP and namespace-to-host forwarding remain
  disabled by that adapter; readiness requires exact IPv4 loopback listener
  shape, no IPv6/wildcard or host guest-port listener, an exact Hauler-owned
  guest listener, a distinct child network namespace, and HTTP 200 at `/v2/`
  or `/`.
- A mode-0600 invocation lock serializes overlapping `postStartCommand`
  invocations. The durable journal records start intent before launch and
  records worker, pasta helper, and Hauler child PID/boot/start, executable,
  argv digest, PGID, SID, and network-namespace evidence.
- Reentry compares the complete recorded command, confinement, endpoint,
  log/pid paths, and stable launch identity before observation. A live unit is
  returned without another launch; a stopped exact unit is restored with a
  PID/start-tick-derived restart attempt; drift fails closed. Existing
  supervisor recovery adopts the exact pending pidfile/process after an
  unknown outcome and never substitutes a PATH tool.
- The registry is writable, so workspace builds can push to
  `$CAMP_REGISTRY`; the fileserver includes the transfer directory, so
  workspace tools can fetch from `$CAMP_FILESERVER`. Both services are
  setsid-detached from the short-lived worker and require no workstation or
  DevPod tunnel after readiness.

## TDD evidence

- RED: new service orchestration tests failed to compile because
  `startServices`, its receipt/evidence contract, and remote service constants
  did not exist. GREEN: verification-before-ensure ordering and exact
  registry/fileserver evidence now pass.
- RED: exact-spec tests failed because no remote Hauler service definitions or
  supervisor bridge existed. GREEN: literal argv assertions prove the exact
  installed Hauler path, ready store, writable registry overlay, transfer
  directory, guest ports, timeout, and readiness paths.
- RED: the existing worker dispatch test required `startServices` to remain
  unsupported. GREEN: the operation now dispatches through the injected
  interface while `checkpoint` remains the unsupported mutation regression.
- RED: duplicate-suppression tests failed because no recorded-unit observation
  path existed. GREEN: an exact live record is observed and returned with zero
  calls to `Ensure` or `Restart`.
- RED: stopped-unit recovery initially reused the base token and then lacked a
  durable process-specific restart identity. GREEN: the restart token binds
  the recorded child PID and start ticks, remains stable across the same
  unknown attempt, and changes for a later recorded process.
- RED: the service receipt lacked Camp worker/supervisor process evidence.
  GREEN: incomplete supervisor evidence is rejected and production records the
  current exact worker process alongside both service units.

## Verification

- `rtk go test ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app -count=1`
  — 438 passed.
- `rtk go test -race ./internal/remoteworker -count=1`
  — 44 passed.
- `rtk go vet ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app`
  — passed.
- `rtk go build ./cmd/camp`
  — passed.
- `rtk git diff --check`
  — passed.

## Self-review

- Checked retry behavior for live facts, stopped facts, and pending
  unknown-outcome starts; no second `Ensure` occurs for an exact live record.
- Checked that production paths derive only from `WorkspaceRoot` and
  `RuntimeRoot`, use exact installed binaries, fixed loopback host ports, and
  private guest ports; no `LookPath` fallback was introduced.
- Checked that manifest reads use no-follow, bounded exact-size reads, stable
  inode observation, and exact SHA-256 equality.
- Checked that changed generated-hook tests still preserve the existing
  lifecycle composition and user-hook ordering rather than replacing it.

## Remaining uncertainty

- A fresh pinned-provider run has not yet executed the real post-start worker,
  exact uploaded pasta, and exact uploaded Hauler together. Therefore real
  push/fetch, pod-IP refusal, disconnect survival, and crash-cut adoption remain
  Task 12 release gates even though their structural contracts and unit tests
  pass.
- The remote image used by that gate must contain the Linux observation
  utilities required by the reused supervisor inspector. Missing observation
  capability fails closed before readiness; it does not weaken confinement or
  fall back to an unrecorded launch.

## Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: completed `haulerKitV1` workspaces replace persistent workstation tunnels with exact installed, invocation-locked, journaled pasta/Hauler units; the guide records fixed host/guest endpoints, store/overlay roots, readiness, identity evidence, retry adoption, restoration, and fail-closed drift behavior.
- Evidence: `internal/remoteworker/services.go`, `internal/remoteworker/supervisor.go`, `TestStartServicesVerifiesHydrationBeforeStartingExactUnits`, `TestStartServicesRejectsIncompleteOrUnconfinedEvidence`, `TestRemoteServiceSpecsUseExactInstalledToolsAndPrivateLoopbackMappings`, `TestEnsureRemoteServiceObservesRecordedUnitWithoutDuplicatingIt`, `TestEnsureRemoteServiceRestartsStoppedRecordWithAttemptStableIdentity`, and the 438-test affected-package verification.
- Stale or ambiguous guidance removed: the host-only reverse-forwarding guidance is now explicitly scoped away from completed remote `haulerKitV1` workspaces; the prior text did not describe their self-contained service lifecycle.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance has not yet proven real push/fetch, pod-IP refusal, disconnect survival, or crash-cut adoption.
