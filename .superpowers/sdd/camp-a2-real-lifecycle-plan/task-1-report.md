## Task 1 report

Implemented the shared real-lifecycle scenario harness for the file and MinIO
verticals. It provides private XDG and DevPod roots, owns a durable-resource
ledger, creates an unrelated private-context workspace before Camp activity,
and uses exact workspace IDs for cleanup. Registry and fileserver endpoints are
read from the durable session snapshot after `open`; no Camp port reservation or
port injection remains. The harness proves recorded endpoints are closed after
cleanup and deletes the unrelated workspace only after proving the scenario did
not remove it.

Verification:

- `go test ./integration -count=1`
- `go vet ./integration`
- `git diff --check`

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/testing-release-evidence.md`
- Durable learning captured: Real lifecycle harness endpoint and cleanup claims are evidence-backed only through durable snapshots, exact IDs, and a supplied candidate binary; port injection is not proof of Camp-selected endpoints.
- Evidence: `integration/local_lifecycle_test.go`, `integration/minio_cli_reopen_test.go`, `integration/minio_cli_reopen_helpers_test.go`, and passing integration/vet checks above.
- Stale or ambiguous guidance removed: The obsolete intentionally-missing unrelated-workspace gate statement.
- Remaining uncertainty: `CAMP_TEST_REAL_LIFECYCLE=1` and `CAMP_TEST_REAL_MINIO_REOPEN=1` were not run; their live DevPod/Hauler/Docker result requires the orchestrator-provided `CAMP_TEST_BINARY`.

## Fix round 1

Added one mode-0700 `XDG_RUNTIME_DIR` beneath each scenario controller and made
both file and MinIO environments use its exact path. Tightened the resource
ledger to only independently owned classes: exact DevPod workspace IDs,
full Camp-recorded process identities, cleanup-permitted paths, and listener
endpoints. Cleanup now consumes and verifies every class after both successful
and interrupted controllers, continues verification after a cleanup failure,
and never enumerates or deletes ambient Docker, Dagger, RCC, or DevPod state.
Misleading container and namespace maps were removed because DevPod's exact
workspace ID is the safe container boundary and namespace observations are not
independently owned cleanup targets.

Verification:

- `go test ./integration -run TestLifecycleEnvironmentIncludesDevPodIsolation -count=1` — passed
- `go test ./integration -run TestLifecycleScenarioCleanupConsumesInterruptedLedger -count=1` — passed
- `go test ./integration -run TestLifecycleScenarioCleanupContinuesVerificationAfterDeleteFailure -count=1` — passed
- `go test ./integration -count=1` — passed
- `go vet ./integration` — passed
- `git diff --check` — passed

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/testing-release-evidence.md`
- Durable learning captured: Real lifecycle scenarios require private XDG runtime roots and verify only exact, independently owned workspace, process, path, and listener identities after cleanup.
- Evidence: `integration/local_lifecycle_test.go`, `integration/minio_cli_reopen_test.go`, `integration/minio_cli_reopen_helpers_test.go`, and the passing commands above.
- Stale or ambiguous guidance removed: Removed the implication that container IDs and observed namespaces were separate harness-owned cleanup identities.
- Remaining uncertainty: Real DevPod/Hauler/Docker gates were not executed without the orchestrator-provided `CAMP_TEST_BINARY`; live runtime cleanup remains gated evidence.

## Fix round 2

Close failures are now retained in the aggregate result instead of suppressed.
After any failed close, the harness attempts exact fallback cleanup for every
retained class before absence verification: complete PID/boot/start identities
use `supervisor.ProcessManager.Stop`, created materializations use
`capsule.Ownership.RemoveOwned`, and forwarding evidence plus runtime paths
require matching device/inode identity before exact removal. Workspace deletion
remains exact-ID-only, listeners remain verification-only, and cleanup continues
after individual failures so all owned resources receive an attempt. Malformed
PID-only process records and evidence paths without complete identity fail
closed and are never accepted as verified absence.

Verification:

- `go test ./integration -run TestLifecycleScenarioCleanupConsumesInterruptedLedger -count=1` — passed; real process and ownership-marked materialization plus exact evidence/runtime paths were removed while the forced close error was surfaced
- `go test ./integration -run TestLifecycleScenarioRejectsIncompleteProcessIdentity -count=1` — passed
- `go test ./integration -run TestLifecycleScenarioCleanupContinuesVerificationAfterDeleteFailure -count=1` — passed
- `go test ./integration -count=1` — passed
- `go vet ./integration` — passed
- `git diff --check` — passed

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/testing-release-evidence.md`
- Durable learning captured: A failed Camp close must remain visible while exact fallback cleanup uses complete process identities, ownership-marker materialization records, and device/inode path identities before verifying absence.
- Evidence: `integration/minio_cli_reopen_helpers_test.go` focused failure-path tests and the passing full integration/vet commands above.
- Stale or ambiguous guidance removed: Removed the implicit claim that post-close observation alone constituted failed-controller cleanup proof.
- Remaining uncertainty: Real DevPod/Hauler/Docker lifecycle gates were not run without the orchestrator-provided `CAMP_TEST_BINARY`; live fallback behavior remains gated evidence.

## Fix round 3

Replaced the fallback path remover's split `Lstat` plus path deletion with a
descriptor-relative quarantine primitive matching Camp's production safety
pattern. It opens the exact parent with `O_NOFOLLOW`, renames the entry to a
random private quarantine name, opens and validates the quarantined device,
inode, and type, restores and reports any mismatch, then uses descriptor-relative
unlink and parent fsync. Runtime directories use `AT_REMOVEDIR`; the helper does
not recurse into a replacement. The deterministic substitution test covers both
the forwarding-evidence file and runtime-directory classes and proves the
competing entry plus displaced recorded object are preserved on mismatch.

Verification:

- `go test ./integration -run TestRemoveExactOwnedPathRestoresReplacementOnIdentityMismatch -count=1` — passed for file and runtime-directory substitutions
- `go test ./integration -run TestLifecycleScenarioCleanupConsumesInterruptedLedger -count=1` — passed
- `go test ./integration -count=1` — passed
- `go vet ./integration` — passed
- `git diff --check` — passed

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/testing-release-evidence.md`
- Durable learning captured: Device/inode validation is cleanup proof only when descriptor-relative quarantine atomically binds the checked object to removal, with restoration on mismatch.
- Evidence: `integration/minio_cli_reopen_helpers_test.go` deterministic file/directory substitution test and `internal/adapters/lifecycle/forwarding.go` production quarantine pattern.
- Stale or ambiguous guidance removed: Replaced wording that allowed a separate identity check followed by path-based deletion.
- Remaining uncertainty: Real DevPod/Hauler/Docker gates remain unrun without the orchestrator-provided `CAMP_TEST_BINARY`; runtime directories containing unexpected children fail closed and are restored rather than recursively removed.

## Fix round 4

Kept the initially validated `O_PATH` descriptor open through the removal
decision and moved the current quarantine entry into a fresh mode-0700
descriptor-held removal boundary before a second device, inode, and exact Linux
file-type validation. Only the matching boundary-relative candidate is
unlinked. A post-validation substitution is moved intact into the boundary,
detected, and restored to the original name; the displaced recorded descriptor
is never targeted. Successful restoration fsyncs both affected directories,
and successful removal fsyncs the boundary and parent before and after removing
the empty helper boundary. Forwarding evidence now records and requires
`S_IFREG`; runtime roots record and require `S_IFDIR`.

Verification:

- `go test ./integration -run 'Test(RemoveExactOwnedPathPreservesPostValidationSubstitution|InspectOwnedPathRejectsNonRegularForwardingEvidence|LifecycleScenarioCleanupConsumesInterruptedLedger)' -count=50` — passed 250 test executions
- `go test ./integration -count=1` — passed 36 tests
- `go vet ./...` — passed
- `git diff --check` — passed

Documentation improvement:
- Canonical file changed or proposed: `docs/skills/testing-release-evidence.md`
- Durable learning captured: A descriptor-relative quarantine name remains mutable after validation; safe fallback removal moves the current entry into a private descriptor-held boundary, revalidates exact device, inode, and Linux file type there, and durably restores any mismatch without recursive deletion.
- Evidence: `TestRemoveExactOwnedPathPreservesPostValidationSubstitution` deterministically swaps both a forwarding-evidence regular file and a runtime directory after initial `Fstat`, proving each substitute and displaced recorded object survives and replacement directory children remain untouched; `TestInspectOwnedPathRejectsNonRegularForwardingEvidence` rejects a FIFO; the focused 50-count, full integration, vet, and diff checks above passed.
- Stale or ambiguous guidance removed: Replaced the overstated claim that validating a mutable quarantine name atomically bound it to unlink.
- Remaining uncertainty: Real DevPod/Hauler/Docker lifecycle gates remain unrun without the orchestrator-provided `CAMP_TEST_BINARY`; the safety proof covers regular forwarding evidence and runtime directories under the scenario-owned Linux removal boundary.
