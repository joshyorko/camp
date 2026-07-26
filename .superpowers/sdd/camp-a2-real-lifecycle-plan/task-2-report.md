# Task 2 report: file-backend lifecycle and digest-qualified OCI proof

## Status

Implemented and focused-test verified; real file lifecycle acceptance remains
failed. Do not promote A2 from roadmap-gated on this report.

The RCC `local` task built the single candidate at `build/camp` from reviewed
HEAD `fe8231a0eaee988687be29f51e4e2c547d59f760` plus this task's dirty
worktree. Candidate SHA-256:
`761d15b5f3350eca1ebed41078caf5a19391dc0259b0d884f47c4775c160776f`.
The candidate reports `0.0.0-fe8231a0eaee.dirty`; no test built another Camp
binary.

## Implemented behavior

- The file gate drives live repeated `camp open` reentry and `camp attach`
  through a bounded PTY process group, proves the landing target, and asserts
  that no additional DevPod workspace was created.
- The fixture now covers ordinary files, executable and non-executable modes,
  Unicode and spaces, an internal relative symlink, an internal payload
  hardlink, a 3 MiB-plus file with an exact SHA-256, user-owned `.claude`
  content, and attach-created content.
- Sync, close, and final fresh-controller close receipts require published
  generations, successful cleanup, and exact `camp recover <session>` recovery
  commands.
- File-backend evidence binds `latest.json`, immutable generation metadata,
  archive bytes, sidecar bytes, parentage, and verified flags. It proves
  generation 1 remains byte-identical after generation 2 advances the pointer.
- The OCI fixture is explicitly built and tagged under `CAMP_REGISTRY`. The
  registry digest is accepted only when it matches the complete platform
  manifest response body and all config/layer descriptors are complete.
- The gate records the local image ID, removes both mutable tag and image ID
  before sync, and persists that ID as payload evidence. After reopen it removes
  any restored copy, pulls `camp/acceptance@sha256:...`, verifies exact
  `RepoDigests`, and requires the digest-pinned container to emit
  `camp-a2-oci-ok`.
- Failure evidence captures bounded forwarder/service logs and durable snapshots
  before the exact Task 1 cleanup path runs. The unrelated fixture is removable
  even if DevPod changes its ownership; verifier-only paths and listeners remain
  fail-closed after failed close.

## Verification

- TDD RED: focused helper test initially failed to compile because
  `parsePlatformManifestDigest`, `runBoundedPTYCommand`,
  `readFilePublicationEvidence`, and `readFileGenerationEvidence` did not
  exist.
- Focused GREEN before the final receipt:
  `go test ./integration -run
  'TestReadFilePublicationEvidenceBindsPointerSidecarAndArchiveBytes|TestParsePlatformManifestDigestRequiresCompleteDigestBoundManifest|TestRunBoundedPTYCommandForwardsInputAndStopsAtDeadline|TestNamedImageReopenProofIsDigestQualifiedValidShell|TestLifecycleScenarioCleanupConsumesInterruptedLedger'
  -count=1`.
- `go test ./integration -count=1` passed 43 tests before the final live-gate
  corrections.
- `go vet ./integration` and `git diff --check` passed during implementation;
  final focused verification is recorded in the commit handoff.
- RCC candidate construction:
  `rcc run -r developer/toolkit.yaml --dev -t local` passed and installed the
  normal development link.
- Real file gate:
  `CAMP_TEST_REAL_LIFECYCLE=1 CAMP_TEST_BINARY=build/camp go test -v
  ./integration -run '^TestLocalLifecycleVertical$' -count=1` did not pass.
  Two runs failed during fresh-controller reopen because the fileserver
  workspace forwarder did not become ready; the preserved DevPod forwarder logs
  show the previous controller's tunnels exiting with status 137 during normal
  workspace deletion and the new fileserver readiness probe exhausting its
  bound. One intervening run completed fresh reopen and all filesystem checks,
  then exposed that restored content need not recreate the mutable tag; that
  finding produced the pre-checkpoint eviction correction above.
- The MinIO gate was not run, as required.

Every failed live run used the exact scenario ledger cleanup. The first two
runs left only their exact root-owned unrelated fixture tree; each was
identity-scoped, ownership-repaired through a one-off container mount, and
removed without touching ambient resources. The final permission correction
allowed the third run to remove its entire temporary root automatically.
Targeted process inspection found no scenario workspace, process, or forwarder
identity remaining after the final run.

## Concerns and remaining work

- The fresh-controller fileserver forwarder readiness failure is reproducible
  but intermittent. The complete A2 file gate remains failed until the merged
  implementation is exercised in the planned comprehensive real-gate run and
  this runtime failure is fixed or proven external.
- The corrected pre-checkpoint image eviction plus post-reopen digest pull
  sequence is focused-test verified but did not reach its final live assertions
  after the last correction because the final run stopped at the forwarder
  blocker.
- No MinIO behavior is claimed by this task.

## Documentation improvement

- Canonical file changed or proposed:
  `docs/skills/testing-release-evidence.md`;
  `docs/skills/devpod-hauler.md`.
- Durable learning captured: bounded PTY reentry/attach, byte-bound immutable
  file publication evidence, complete platform-manifest digest validation,
  pre-checkpoint tag/image-ID eviction, exact digest pull/`RepoDigests`/run
  proof, container-owned unrelated-fixture cleanup, and preservation of
  forwarder failure evidence.
- Evidence: focused helper tests; RCC candidate manifest; live sync generation
  1 and close generation 2; a successful fresh reopen through all filesystem
  assertions; registry manifest digest
  `sha256:b87a26eec1b3e574f60e26013e0170b8cc5e0acabc4fb0f746d17d58ba000f45`;
  preserved forwarder logs and durable snapshots from the failed live runs.
- Stale or ambiguous guidance removed: acceptance no longer assumes a mutable
  engine tag survives image capture and fresh-controller reopen.
- Remaining uncertainty: no complete real file lifecycle pass after the final
  correction; intermittent fresh-reopen fileserver forwarder readiness remains;
  MinIO was not run.
