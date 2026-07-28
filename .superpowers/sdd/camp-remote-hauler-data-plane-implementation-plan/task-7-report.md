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

## Authorized fix round 6

Status: DONE

### Result

- Deferred actor-partial cleanup now exposes the exact boundary after
  quarantine validation and before the former destructive action. A
  substitution at that boundary is never deleted.
- Cleanup creates an exclusive private empty placeholder, captures its
  descriptor identity, fsyncs its creation, and atomically exchanges it with
  the validated quarantine using `RENAME_EXCHANGE`. It then verifies that the
  displaced inode is the originally created private bounded regular partial
  and that the quarantine contains the exact placeholder.
- An exact captured partial and the exact placeholder are removed with a parent
  fsync after each directory mutation. A captured mismatch is restored to the
  original name only with `RENAME_NOREPLACE`; a concurrent name is never
  overwritten, and any failed restoration preserves displaced evidence.
- Missing exchange support fails closed: the original partial is restored, the
  private placeholder remains as evidence, and no unowned inode is deleted.
  Primary publication and cleanup failures remain joined.
- Descriptor-relative no-follow traversal, initial no-replace quarantine,
  immediate creation identity, bounded private-file validation, no-replace
  publication, retry durability, and service journaling are unchanged.

### TDD evidence

- RED:
  `TestServiceActorEvidenceCleanupNeverDeletesReplacementAfterQuarantineValidation`
  failed because the new post-validation hook was not reached by production;
  the test could not find its replacement after the old cleanup completed.
- GREEN: the same test substitutes the random quarantine immediately after
  validation and proves the replacement is restored intact rather than
  deleted.
- GREEN: `TestServiceActorEvidenceCleanupRemovesExactPartial` proves exact-owned
  cleanup; the existing substitution, concurrent-name, deletion-fsync, and
  restore-fsync tests prove restoration/preservation and unknown-outcome
  propagation through the displacement path.
- GREEN:
  `TestServiceActorEvidenceCleanupFailsClosedWithoutAtomicDisplacement` proves
  an unavailable exchange primitive restores the owned partial, preserves the
  private displacement evidence, and returns the primary plus cleanup error.

### Verification

- `rtk go test ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app -count=1`
  — 465 passed.
- `rtk go test -race ./internal/remoteworker -count=1` — 71 passed.
- `rtk go vet ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app` — passed.
- `rtk go build ./cmd/camp` — passed.
- `rtk git diff --check` — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: post-validation actor-partial cleanup requires a fsynced exclusive placeholder, atomic exchange displacement, verification of both the displaced original identity and placeholder identity, a durability barrier after every directory mutation, no-replace mismatch restoration, and fail-closed preservation when exchange is unsupported or restoration is blocked.
- Evidence: `internal/remoteworker/actor_evidence.go`, `TestServiceActorEvidenceCleanupNeverDeletesReplacementAfterQuarantineValidation`, `TestServiceActorEvidenceCleanupRemovesExactPartial`, `TestServiceActorEvidenceCleanupNeverOverwritesConcurrentName`, `TestServiceActorEvidenceCleanupDeletionFsyncFailureIsRetryable`, `TestServiceActorEvidenceCleanupRestoreFsyncFailureIsRetryable`, `TestServiceActorEvidenceCleanupFailsClosedWithoutAtomicDisplacement`, and the focused, race, vet, build, and diff-check commands above.
- Stale or ambiguous guidance removed: the prior guide stopped at validating a random quarantine name before unlink and did not require atomic post-validation displacement, placeholder identity verification, exchange support, or a durability barrier for each new mutation.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains unchanged; no real DevPod/Hauler lifecycle gate ran in this authorized fix round.

## Authorized unnamed-staging redesign

Status: DONE

### Result

- Actor-evidence publication no longer creates a named partial, quarantine,
  capture, displacement placeholder, or cleanup path. The entire named cleanup
  design and every cleanup hook/test were removed.
- Publication opens `openat(parent, ".", O_TMPFILE|O_RDWR|O_CLOEXEC, 0600)`.
  Unsupported unnamed staging fails closed without a named fallback. Chmod,
  exact bounded write, file fsync, and `fstat` prove a regular mode-0600 inode
  with the exact body size and link count zero before publication.
- The exact open inode is linked no-replace first with
  `linkat(fd, "", parent, final, AT_EMPTY_PATH)`. When that route is unavailable,
  the existing unprivileged `/proc/self/fd/<fd>` plus `AT_SYMLINK_FOLLOW` route
  links the same descriptor. If neither exact-FD route works, publication fails
  closed and closing the descriptor reclaims it automatically.
- A fresh final must match the staging descriptor's device and inode, have link
  count one and exact regular/private shape and bytes, then survive a parent
  fsync and a second stable descriptor-relative observation with the same
  identity. No failure path deletes or rolls back the canonical final.
- `EEXIST` accepts only exact, stable bytes followed by parent fsync and a
  second stable observation. Differing, symlinked, malformed, or replaced
  finals are preserved and rejected.
- Parent-fsync failure after a successful link is an unknown outcome. The final
  remains in place; retry re-fsyncs and accepts only an exact stable final.
  Substitution after that unknown outcome, or after linking and before identity
  validation, is preserved and rejected.
- Service receipts, actor schema validation, supervisor relationships, service
  journals, pending-start adoption, and remote Hauler lifecycle behavior were
  not changed.

### TDD evidence

- RED: the new tests failed to compile because actor publication exposed no
  unnamed-open, direct exact-FD link, procfs exact-FD link, pre-link, or
  post-link seams; production still encoded named partial cleanup.
- GREEN:
  `TestServiceActorEvidenceUnnamedStagingFailuresLeaveNoDirectoryEntries`
  proves write, chmod, file-fsync, and pre-link failures leave zero entries.
- GREEN:
  `TestServiceActorEvidenceFailsClosedWithoutUnnamedOrExactFDPublication`
  proves unsupported `O_TMPFILE` and direct-link `EPERM` plus unavailable
  procfs fail closed with no final or staging entry.
- GREEN:
  `TestServiceActorEvidenceEEXISTRaceAcceptsOnlyExactStableFinal`,
  `TestServiceActorEvidenceLinkFsyncUnknownOutcomeRetriesOneCanonicalFinal`,
  `TestServiceActorEvidencePreservesSubstitutionAfterUnknownOutcome`, and
  `TestServiceActorEvidencePreservesSubstitutionAfterLinkBeforeIdentityCheck`
  prove idempotency, retry durability, and preservation of contested finals.
- GREEN: `TestServiceActorEvidencePublishesExactUnnamedInode` proves link count
  zero before publication, the final and descriptor share device/inode with
  link count one afterward, and mode, size, and bytes are exact.
- GREEN: `TestServiceActorObserverRejectsNamedFileReplacementDuringRead`
  continues to prove the bounded stable observer rejects a replacement race
  without deleting the replacement.

### Verification

- `rtk go test ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app -count=1`
  — passed.
- `rtk go test -race ./internal/remoteworker -count=1` — passed.
- `rtk go vet ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app` — passed.
- `rtk go build ./cmd/camp` — passed.
- `rtk git diff --check` — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: actor evidence stages only as an unlinked
  `O_TMPFILE`, publishes the exact descriptor no-replace through
  `AT_EMPTY_PATH` or `/proc/self/fd`, validates the linked identity before and
  after the parent durability barrier, treats post-link fsync failure as an
  unknown outcome, and never deletes or rolls back the canonical final.
- Evidence: `internal/remoteworker/actor_evidence.go`,
  `TestServiceActorEvidenceUnnamedStagingFailuresLeaveNoDirectoryEntries`,
  `TestServiceActorEvidenceFailsClosedWithoutUnnamedOrExactFDPublication`,
  `TestServiceActorEvidenceEEXISTRaceAcceptsOnlyExactStableFinal`,
  `TestServiceActorEvidenceLinkFsyncUnknownOutcomeRetriesOneCanonicalFinal`,
  `TestServiceActorEvidencePreservesSubstitutionAfterUnknownOutcome`,
  `TestServiceActorEvidencePreservesSubstitutionAfterLinkBeforeIdentityCheck`,
  `TestServiceActorEvidencePublishesExactUnnamedInode`, the stable-observer
  replacement test, and the verification commands above.
- Stale or ambiguous guidance removed: all partial/quarantine/placeholder
  cleanup guidance was removed because no validate-path-then-delete design can
  safely authorize deletion against adversarial same-UID replacement.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains
  unchanged; no real DevPod/Hauler lifecycle gate ran in this redesign.

## Unnamed staging follow-up 1

Status: DONE

### Result

- Stable actor-evidence observation now returns the observed bytes together
  with the no-follow named identity: device, inode, size, mode, and link count.
  It also requires mode and link count to remain unchanged across the open-file
  and still-named observations.
- Existing-final confirmation retains the first stable identity instead of
  discarding it. Before the durability barrier it requires exact bounded bytes,
  a regular mode-0600 file, exact size, and link count one.
- After parent fsync, confirmation performs a second stable observation and
  requires the same first device and inode plus exact bytes and single-link
  shape. An exact-byte replacement before the fsync or after the fsync but
  before the second observation is preserved and rejected.
- Hardlinked existing evidence is preserved and rejected. Exact single-link
  replay remains idempotent.
- Both initial existing-final replay and the `EEXIST` publication race pass the
  retained first identity through the same confirmation helper. Fresh unnamed
  staging and exact-FD publication behavior are unchanged.

### TDD evidence

- RED:
  `TestServiceActorEvidenceExistingConfirmationRejectsExactByteInodeReplacementAcrossFsync`
  accepted exact-byte replacement both between the first observation and
  parent fsync and after fsync before the second observation.
- RED:
  `TestServiceActorEvidenceExistingConfirmationRejectsHardlinkedFinal`
  accepted an existing final with link count two.
- GREEN: both replacement boundaries now return `ErrServiceEvidence` while the
  replacement inode remains canonical, and the hardlinked final is rejected
  with both links preserved.
- GREEN:
  `TestServiceActorEvidenceExistingConfirmationAcceptsExactSingleLinkReplay`
  proves unchanged exact single-link evidence remains idempotent.
- GREEN: the existing `EEXIST` exact/different race coverage remains green
  through the shared identity-linked confirmation path.

### Verification

- `rtk go test ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app -count=1`
  — passed.
- `rtk go test -race ./internal/remoteworker -count=1` — passed.
- `rtk go vet ./internal/remoteworker ./internal/adapters/supervisor
  ./internal/adapters/hauler ./internal/capsule ./internal/app` — passed.
- `rtk go build ./cmd/camp` — passed.
- `rtk git diff --check` — passed.

### Documentation improvement

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/devpod-hauler.md`
- Durable learning captured: existing actor evidence is authorized only when a
  retained first stable device/inode and single-link private-file shape survive
  the parent-fsync barrier and match a second stable exact-byte observation;
  equal bytes alone are insufficient durability authority.
- Evidence: `internal/remoteworker/actor_evidence.go`,
  `TestServiceActorEvidenceExistingConfirmationRejectsExactByteInodeReplacementAcrossFsync`,
  `TestServiceActorEvidenceExistingConfirmationRejectsHardlinkedFinal`,
  `TestServiceActorEvidenceExistingConfirmationAcceptsExactSingleLinkReplay`,
  the existing `EEXIST` race test, and the verification commands above.
- Stale or ambiguous guidance removed: the guide no longer implies two
  independent byte-equal observations authorize idempotent success; it now
  requires identity continuity and link count one across the durability
  barrier.
- Remaining uncertainty: fresh pinned-provider Task 12 acceptance remains
  unchanged; no real DevPod/Hauler lifecycle gate ran in this follow-up.
