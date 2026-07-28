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

## Fix round 1

Status: DONE_WITH_CONCERNS

### Result

- Enforcing-SELinux service composition now derives only the exact
  `/usr/bin/runcon -t unconfined_t` child-context prefix. The executable is
  opened without following symlinks and must be an executable regular file.
  Non-enforcing or absent SELinux state produces no prefix.
- The exact child-context prefix participates in the confinement fingerprint
  and in `PastaLoopback` construction. Reentry rebuilds the complete expected
  pasta argv and digest from the exact capability, mapping, private paths, and
  Hauler child command before any observation or restart. Altered prefix,
  helper argv, helper digest, or Hauler tail fails closed before the controller
  can act.
- The generated worker now launches the exact current Camp executable as a
  hidden, one-shot `__remote-service-supervisor` subprocess rather than naming
  its own PID as the supervisor. The supervisor authenticates the complete
  still-live parent-worker record and parent PID before service work.
- Receipts contain separate full worker and supervisor process records in
  addition to each pasta-helper and Hauler-child record. The worker/supervisor
  pair must have distinct identities, the same boot ID, and an exact
  parent/child relationship.
- Before returning readiness, the supervisor publishes and reads back immutable
  per-worker actor evidence under `.camp/runtime/services/actors/`. Evidence
  names bind the worker PID and start ticks; changed worker or supervisor
  identity/argv evidence is rejected. Service facts and the existing
  unknown-outcome recovery path remain independently durable.
- No PATH resolution, second DevPod operation, persistent workstation tunnel,
  alternate Hauler/pasta binary, or unconfined raw launch was introduced.

### TDD evidence

- RED: SELinux tests failed to compile because no remote child-context resolver
  existed. GREEN: enforcing fixtures require the exact verified runcon prefix
  and reject a non-executable command; absent and non-enforcing fixtures return
  no prefix.
- RED: helper-drift tests reached the fake observation/restart seam because
  reentry compared only child argv and confinement metadata. GREEN: production
  reconciliation builds the real `PastaLoopback` process spec and compares the
  entire helper argv plus its independently derived SHA-256 before calling
  `Observe` or `Restart`.
- RED: service tests failed because `ServiceReceipt` had no worker field and no
  durable actor-evidence contract. GREEN: separate worker/supervisor records
  are relationship-validated, round-trip through immutable evidence, and
  reject worker start-tick or supervisor argv-digest mismatch.
- RED: the CLI test reached the normal Cobra path because
  `__remote-service-supervisor` was not registered. GREEN: the exact hidden
  process boundary accepts only the strict internal envelope and remains absent
  from help.

### Verification

- `rtk go test ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/capsule ./internal/app ./internal/cli ./cmd/camp -count=1`
  — 610 passed.
- `rtk go test -race ./internal/remoteworker
  ./internal/adapters/supervisor -count=1`
  — 86 passed.
- `rtk go vet ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/capsule ./internal/app ./internal/cli ./cmd/camp`
  — passed.
- `rtk go build ./cmd/camp`
  — passed.
- `rtk git diff --check`
  — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: remote `haulerKitV1` startup derives and fingerprints the exact enforcing-SELinux runcon prefix, verifies the complete pasta helper argv/digest before reentry, separates the worker from its one-shot supervisor, and publishes immutable actor evidence separately from service-unit evidence.
- Evidence: `TestRemoteChildContextPrefixUsesOnlyExactVerifiedRunconWhenSELinuxEnforces`, `TestRemoteChildContextPrefixIsEmptyWhenSELinuxIsNotEnforcing`, `TestEnsureRemoteServiceRejectsCompleteHelperArgvOrDigestDriftBeforeRestart`, `TestServiceActorEvidenceRoundTripsAndRejectsEitherIdentityMismatch`, `TestServiceActorEvidenceRejectsConflatedOrWrongRoleCommands`, and `TestRunRegistersHiddenRemoteServiceSupervisorCommand`.
- Stale or ambiguous guidance removed: the prior guide implied the current worker record was supervisor evidence and did not state that the SELinux prefix or complete helper argv/digest was part of reentry authority.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance still must exercise enforcing SELinux, real service restart after worker exit, push/fetch, pod-IP refusal, disconnect survival, and crash-cut adoption.

## Fix round 2

Status: DONE_WITH_CONCERNS

### Result

- Actor evidence no longer uses `os.ReadFile` or the shared receipt helper.
  Its dedicated boundary resolves every absolute parent component through
  descriptor-relative `openat(O_DIRECTORY|O_NOFOLLOW)` calls.
- Existing evidence is accepted only when `openat(O_NOFOLLOW)` yields a
  mode-0600 regular file within the diagnostic bound. The observer performs a
  bounded read, repeats `fstat`, then compares device, inode, and size against a
  no-follow `fstatat` of the still-named entry.
- Publication writes a random private partial with `openat(O_EXCL|O_NOFOLLOW)`,
  enforces mode 0600, fsyncs the file and parent, and commits with
  descriptor-relative `renameat2(RENAME_NOREPLACE)`. A concurrent existing path
  is compared only through the same safe observer.
- Successful publication performs readback through that observer. Exact
  existing bytes remain idempotent; symlinked files or parents, non-regular or
  oversized evidence, replacement races, and unequal content fail closed.
- Service journaling, pending-start adoption, tool identity, endpoints, and
  process supervision were not changed.

### TDD evidence

- RED: a symlink to matching JSON was accepted by both publication and
  observation because they followed `os.ReadFile`; a symlinked parent was also
  traversed.
- RED: the observer had no open-file identity seam, so a named entry replacement
  during the read could not be detected.
- GREEN: hostile tests now reject symlinked actor files, a symlinked parent,
  directories, oversized evidence, and a descriptor-stable open file whose
  named path is replaced before acceptance.
- GREEN: exact publication followed by both a second publication and readback
  remains idempotent.

### Verification

- `rtk go test ./internal/remoteworker -count=1`
  — 56 passed.
- `rtk go test -race ./internal/remoteworker -count=1`
  — 56 passed.
- `rtk go vet ./internal/remoteworker`
  — passed.
- `rtk git diff --check`
  — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: immutable actor evidence requires descriptor-relative no-follow parent traversal, private bounded regular-file observation, stable open-and-named device/inode/size, fsynced private staging, and no-replace publication; byte equality alone is not authority.
- Evidence: `TestServiceActorEvidenceRejectsSymlinkedFileAndParent`, `TestServiceActorEvidenceRejectsNonRegularAndExcessiveExistingFiles`, `TestServiceActorObserverRejectsNamedFileReplacementDuringRead`, and the idempotent replay in `TestServiceActorEvidenceRoundTripsAndRejectsEitherIdentityMismatch`.
- Stale or ambiguous guidance removed: the earlier word “immutable” did not disclose that existing evidence was followed by pathname and could authorize a replaceable symlink target.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains unchanged.

## Fix round 4

Status: DONE_WITH_CONCERNS

### Result

- Actor-evidence publication now captures the exclusively created partial's
  device, inode, regular-file type, private mode, and initial zero size
  immediately after `openat(O_CREAT|O_EXCL|O_NOFOLLOW)`, before write, chmod,
  or file-fsync can fail.
- Deferred cleanup retains that stable creation identity across every later
  file boundary. It unlinks only when `fstatat(AT_SYMLINK_NOFOLLOW)` proves the
  still-named entry is the same device/inode, remains a private regular file,
  and has a size from zero through the bounded evidence length.
- Early exact partials are removed after injected write, chmod, and file-fsync
  failures. An early same-name replacement is left untouched, and the returned
  publication error preserves both the primary boundary failure and cleanup
  identity mismatch.
- Short writes now fail explicitly and use the same identity-bound cleanup.
  No blind unlink, service journal, retry durability, stable observation,
  no-follow traversal, or no-replace publication behavior changed.

### TDD evidence

- RED: the new boundary tests failed to compile because publication operations
  had no injectable write, chmod, or file-fsync seams; the production cleanup
  identity was still populated only after those operations.
- GREEN: `TestServiceActorEvidenceCleanupRemovesExactPartialAfterEarlyFileFailures`
  passes at all three injected boundaries and proves each exact owned partial
  is absent afterward.
- GREEN: `TestServiceActorEvidenceCleanupLeavesEarlySubstitutedPartialUntouched`
  proves an early replacement survives and the returned error contains both
  the injected write failure and the cleanup refusal.

### Verification

- `rtk go test ./internal/remoteworker -count=1` — 64 passed.
- `rtk go test -race ./internal/remoteworker -count=1` — 64 passed.
- `rtk go vet ./internal/remoteworker` — passed.
- `rtk git diff --check` — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: immutable actor-evidence cleanup authority must be captured immediately after exclusive no-follow creation, before write/chmod/file-fsync; later cleanup accepts only the same no-follow private regular inode with size bounded from zero through the evidence length.
- Evidence: `internal/remoteworker/actor_evidence.go`, `TestServiceActorEvidenceCleanupRemovesExactPartialAfterEarlyFileFailures`, `TestServiceActorEvidenceCleanupLeavesEarlySubstitutedPartialUntouched`, and the focused, race, vet, and diff-check commands above.
- Stale or ambiguous guidance removed: the prior guide's “expected size” wording implied only a fully written partial and did not cover safely owned zero-length or short early partials.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains unchanged.

## Fix round 3

Status: DONE_WITH_CONCERNS

### Result

- Exact existing actor evidence is no longer accepted as success until the
  parent directory is fsynced. A retry after an unknown post-rename durability
  outcome therefore re-establishes the directory durability barrier.
- Deferred partial cleanup retains the created file's device, inode, mode, and
  size. It observes the still-named entry with `fstatat(AT_SYMLINK_NOFOLLOW)`
  and unlinks only the same private bounded regular file; a substituted name is
  left untouched and the cleanup mismatch is included in the publication
  error.
- Descriptor-relative no-follow traversal, bounded stable observation,
  no-replace publication, unsupported-`renameat2` failure, idempotency, and
  service journal behavior remain unchanged.

### TDD evidence

- RED: removing the equal-existing parent fsync made
  `TestServiceActorEvidenceRetryConfirmsParentDurability` report zero retry
  durability confirmations.
- RED: restoring name-only cleanup made
  `TestServiceActorEvidenceCleanupLeavesSubstitutedPartialUntouched` detect that
  the substitute was removed without a recorded identity-mismatch error.
- GREEN: both regressions pass, and
  `TestServiceActorEvidenceCleanupRemovesExactPartial` proves an unchanged
  exact partial is still removed after an injected pre-rename failure.

### Verification

- `rtk go test ./internal/remoteworker -count=1`
  — 59 passed.
- `rtk go test -race ./internal/remoteworker -count=1`
  — 59 passed.
- `rtk go vet ./internal/remoteworker`
  — passed.
- `rtk git diff --check`
  — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: idempotent actor-evidence replay requires a fresh parent-directory fsync, and deferred partial cleanup may unlink only after a no-follow named observation matches the created file's device, inode, private regular-file mode, and expected size.
- Evidence: `TestServiceActorEvidenceRetryConfirmsParentDurability`, `TestServiceActorEvidenceCleanupLeavesSubstitutedPartialUntouched`, `TestServiceActorEvidenceCleanupRemovesExactPartial`, and the focused, race, vet, and diff-check commands above.
- Stale or ambiguous guidance removed: the prior guide described stable existing-file observation but did not state the retry durability barrier or identity-bound partial cleanup rule.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains unchanged.

## Fix round 5

Status: DONE

### Result

- Deferred actor-partial cleanup no longer performs check-then-unlink on the
  contested name. After the initial no-follow shape check, it atomically moves
  that name to a random private quarantine with
  `renameat2(RENAME_NOREPLACE)` and fsyncs the parent directory.
- Cleanup validates the quarantined device, inode, private regular-file mode,
  and bounded size against the identity captured immediately after exclusive
  creation. Only that exact quarantined inode is unlinked, followed by a parent
  fsync whose failure remains an unknown-durability publication error.
- A substituted inode is restored to the original name only with
  `RENAME_NOREPLACE`, then parent-fsynced. A concurrent occupant is never
  overwritten; failed restoration leaves the unrelated inode in quarantine as
  evidence. Unsupported atomic rename fails closed without changing the
  contested name.
- Primary publication failures remain joined with cleanup diagnostics.
  No-follow parent traversal, immediate identity capture, bounded shape,
  stable observation, no-replace publication, and idempotent retry behavior
  remain intact.

### TDD evidence

- RED: the new race-boundary tests failed to compile because cleanup exposed no
  seam after its initial check or after atomic quarantine; production still
  called `unlinkat` directly on the checked contested name.
- GREEN:
  `TestServiceActorEvidenceCleanupRestoresSubstitutionAfterInitialCheck`
  substitutes the name after the initial check and proves the unrelated inode
  is restored while both primary and cleanup errors survive.
- GREEN: `TestServiceActorEvidenceCleanupNeverOverwritesConcurrentName` proves
  a new occupant created after quarantine is not overwritten and the displaced
  unrelated inode remains preserved in private quarantine.
- GREEN:
  `TestServiceActorEvidenceCleanupDeletionFsyncFailureIsRetryable` and
  `TestServiceActorEvidenceCleanupRestoreFsyncFailureIsRetryable` prove both
  directory durability failures propagate and a later publication retry can
  complete.
- GREEN:
  `TestServiceActorEvidenceCleanupFailsClosedWithoutAtomicQuarantine` proves an
  unavailable atomic primitive leaves the owned partial untouched.

### Verification

- `rtk go test ./internal/remoteworker -count=1` — 69 passed.
- `rtk go test -race ./internal/remoteworker -count=1` — 69 passed.
- `rtk go vet ./internal/remoteworker` — passed.
- `rtk git diff --check` — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: actor-evidence partial cleanup must atomically quarantine the contested name, fsync every quarantine/delete/restore directory mutation, validate the quarantined inode before deletion, and restore mismatches only with no-replace semantics while preserving evidence when a concurrent name blocks restoration.
- Evidence: `internal/remoteworker/actor_evidence.go`, `TestServiceActorEvidenceCleanupRestoresSubstitutionAfterInitialCheck`, `TestServiceActorEvidenceCleanupNeverOverwritesConcurrentName`, `TestServiceActorEvidenceCleanupDeletionFsyncFailureIsRetryable`, `TestServiceActorEvidenceCleanupRestoreFsyncFailureIsRetryable`, `TestServiceActorEvidenceCleanupFailsClosedWithoutAtomicQuarantine`, and the focused, race, vet, and diff-check commands above.
- Stale or ambiguous guidance removed: the prior cleanup rule authorized a pathname check followed by unlink and said substitutions remained untouched without specifying atomic displacement, restoration, no-overwrite behavior, or directory durability barriers.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains unchanged; no real DevPod/Hauler lifecycle gate ran in this fix round.
