# Camp RCC Full-Program Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the complete Camp product promised by the original Codex build prompt through one RCC-backed development, evidence, release, runtime, TUI, portability, and performance program without dropping or postponing product scope.

**Architecture:** Existing generations, metadata, pointers, leases, journals, backends, and CampKit remain authoritative. Work proceeds in dependency order and may run in parallel when write sets and evidence environments are independent; evidence gates constrain claims and destructive operations, not whether a committed workstream may begin. The exact `build/camp` candidate and its SHA-256 connect every source gate, black-box lifecycle, UI proof, provider proof, package, and release artifact.

**Tech Stack:** Go 1.26.5, Cobra, Bubble Tea, DevPod v0.26.1, Hauler v2.0.2, RCC v18.17.7, Python 3.10.15, Invoke 2.2.0, Robot Framework 7.4.2, Docker/Podman, MinIO/S3, Kubernetes, GitHub Actions.

## Global Constraints

- Linux product support is amd64 and arm64; the RCC factory host is Linux amd64 while release candidates execute natively on both architectures.
- `developer/rcc.lock.yaml`, `go.mod`, and `tools.lock.yaml` are the only authorities for RCC, Go, and runtime-tool identities respectively.
- Every RCC run receives a private `ROBOCORP_HOME`; no shared writable runtime home is cached.
- Preserve `.claude/` untouched and untracked.
- Preserve whole-root filesystem semantics and reject traversal, escaping links, special files, duplicate paths, unsafe permission bits, shared writable trusted/workspace inodes, and identity drift.
- Preserve explicit `$CAMP_REGISTRY` capture and implement automatic named workspace-engine image capture; dangling layers and opaque BuildKit cache are not OCI images.
- No destructive cleanup occurs before immutable generation verification and conditional pointer publication.
- Every behavior change updates the narrowest guide under `docs/skills/` in the same commit.
- Every gate reports `passed`, `failed`, `missing`, `skipped`, or `gated`; only `passed` supports a product or release claim.
- TUI, runtime, portability, and performance work are active program tracks. Their releases remain bound to the same lifecycle and evidence contracts.

## Baseline Triage Against Current `master`

| Program area | Current evidence | Classification | Required correction |
|---|---|---|---|
| RCC trust root | `developer/rccw`, lock, toolkit, setup, factory tests | Implemented with drift | Keep RCC v18.17.7; retain Robot Framework 7.4.2 and update the older 6.1.1 plan pin |
| Exact candidate | `tasks.py`, `candidate.json`, RCC CI jobs | Partial | Make `local` repository-only; move `$HOME` installation exclusively to `install` |
| Source gates | RCC `test`, direct CI, release-pipeline contracts | Partial | Add generated-doc, freeze/config, deterministic cross-build, and contribution-contract gates explicitly to `tasks.py` |
| Real Docker lifecycle | Named Go gates exist | Broken/unverified | Fix the `/proc/self/fd/<n>/hauler-manifest.yaml` exec boundary and complete open/attach/sync/close/reopen |
| OCI lifecycle | Explicit registry capture implemented | Divergent from original prompt | Add automatic named engine-image discovery, push, digest proof, restore, and opt-out behavior |
| MinIO/concurrency | MinIO and two-writer tests exist | Partial/unverified | Make exact lifecycle evidence non-skipping under RCC Robot and preserve losing immutable generations |
| CI parity | Direct and RCC source jobs plus parity artifact exist | Partial | Add mandatory exact-candidate Robot evidence and two consecutive complete parity records |
| Release | RCC package plus native amd64/arm64 verification exists | Partial | Bind release only to a candidate already proven by mandatory lifecycle and parity evidence |
| Kubernetes | Protected workflow and tests exist | Gated/unverified | Run and retain a passing exact-candidate protected lifecycle receipt |
| TUI | Unified setup/init Bubble Tea flow and assets exist | Partial | Extend the same visual/event model through open, attach, sync, close, recover, and status |
| Controller/blueprints/timeline/profiles | No complete public contract | Missing | Implement the schemas and commands as active workstreams |
| CampKit | Current v1 inspect/verify/import/export behavior exists | Partial | Complete v2 generation envelope, profile binding, receipts, and collision-safe import |
| Performance | No accepted representative baseline | Missing | Add reproducible profiling, then implement cache/pack/reflink/protocol work behind measured safety gates |
| Dogfood | No ten-cycle evidence set | Missing | Run continuously as features land; publish the final ten-cycle acceptance report before release |

---

### Task 1: Repair the Exact Live Checkpoint Failures

**Files:**
- Modify: `internal/checkpoint/builder.go`
- Modify: `internal/checkpoint/builder_test.go`
- Modify: `internal/adapters/hauler/generation.go`
- Modify: `internal/app/checkpoint.go`
- Test: `internal/checkpoint/builder_test.go`
- Test: `internal/app/checkpoint_test.go`
- Modify: `docs/skills/local-lifecycle-recovery.md`

**Interfaces:**
- Consumes: descriptor-anchored `checkpointDirectory` writes and the existing `GenerationAssembler.Assemble` contract.
- Produces: stable subprocess-visible manifest/build paths that remain identity-checked against the held directory descriptors.

- [ ] Add a failing builder test whose assembler starts a real child process that opens the provided manifest path after exec; require the path to remain readable and remain beneath the canonical held `.camp` directory.
- [ ] Run `rtk go test ./internal/checkpoint -run 'TestBuilder.*Manifest.*Exec' -count=1` and confirm failure contains `/proc/self/fd`.
- [ ] Pass Hauler canonical filesystem paths only after `openat2` validation proves the final directory identities still match the held descriptors; retain descriptor paths for Camp-owned commit, archive, and removal operations.
- [ ] Run the focused checkpoint, Hauler, and crash-recovery tests.
- [ ] Build `build/camp`, initialize a disposable camp, and prove the previously failing remote tar checkpoint reaches verified publication.
- [ ] Commit with `fix: preserve checkpoint paths across hauler exec`.

### Task 2: Reconcile the RCC Factory With the Master Contract

**Files:**
- Modify: `tasks.py`
- Modify: `developer/setup.yaml`
- Modify: `robot_requirements.txt`
- Modify: `developer/test_factory.py`
- Modify: `releasepipeline/rcc_factory_contract_test.go`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: `build_candidate()`, `verify_candidate()`, and `install_candidate()`.
- Produces: repository-only `local`, explicit user-scoped `install`, and one frozen RCC dependency identity.

- [ ] Change the factory contract test to require `local` to stop after candidate smoke verification and require only `install` to call `install_candidate`.
- [ ] Run `rtk go test ./releasepipeline -run TestRCCFactory -count=1` and confirm it fails against the current implicit installation.
- [ ] Split `tasks.py` so `local` builds/smokes `build/camp` and `install` verifies then atomically links that exact candidate.
- [ ] Keep Robot Framework 7.4.2 in both RCC and pip declarations and record that it supersedes the older 6.1.1 plan pin.
- [ ] Prove `developer/rccw` succeeds from an empty private tool root and fails closed for corrupt, truncated, wrong-version, wrong-architecture, missing, and network-interrupted assets without PATH/Homebrew/latest fallback.
- [ ] Add explicit freeze validation for Go, RCC, Robot, DevPod, Hauler, and Room declarations without duplicating `tools.lock.yaml`.
- [ ] Run developer Python tests and RCC factory contract tests.
- [ ] Commit with `fix: align rcc candidate and install tasks`.

### Task 3: Make RCC Test the Sole Source-Gate Orchestrator

**Files:**
- Modify: `tasks.py`
- Modify: `releasepipeline/documentation_contract_test.go`
- Modify: `releasepipeline/rcc_factory_contract_test.go`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: repository test, packaging, docs generation, release-pipeline, and deterministic-build commands.
- Produces: `build/evidence/test-gates.json` with a complete named gate ledger.

- [ ] Add contract tests for named gates: unit, race, vet, vulnerability, generated documentation, RCC freeze validation, packaging, release pipeline, deterministic amd64/arm64 build, contribution receipt, and whitespace.
- [ ] Add each gate explicitly to `test_task`; do not rely on a broad Go test to hide a missing named contract.
- [ ] Record command identity, duration, result, and sanitized failure reason for every gate.
- [ ] Prove one intentionally missing mandatory gate makes the task fail and leaves a `missing` evidence record.
- [ ] Run `./developer/rccw run -r developer/toolkit.yaml --dev -t test`.
- [ ] Commit with `feat: make rcc test authoritative`.

### Task 4: Complete the Real Docker Filesystem Lifecycle

**Files:**
- Modify: `integration/local_lifecycle_test.go`
- Modify: `integration/local_lifecycle_task2_helpers_test.go`
- Modify: `integration/forwarder_crash_test.go`
- Modify: `scripts/verify-real-evidence.sh`
- Modify: `docs/skills/testing-release-evidence.md`
- Modify: `docs/skills/devpod-hauler.md`

**Interfaces:**
- Consumes: the exact `CAMP_TEST_BINARY`, private XDG/DevPod homes, and the production CLI.
- Produces: sanitized lifecycle and cleanup receipts for one exact candidate.

- [ ] Require files, modes, Unicode, spaces, relative symlinks, hardlinks, large files, `.claude`, deletes, traversal rejection, escaping-link rejection, special-file rejection, duplicate-path rejection, and unsafe-mode rejection.
- [ ] Require open, reentry, attach, sync, close, fresh-controller reopen, supervisor recovery, forwarder recovery, stale lease rejection, and exact process-identity rejection.
- [ ] Create one unrelated workspace in the private context and prove exact-ledger cleanup preserves it.
- [ ] Fail rather than skip when `CAMP_TEST_REAL_LIFECYCLE=1`.
- [ ] Run the named lifecycle and crash matrix against `build/camp`.
- [ ] Commit with `test: prove exact docker lifecycle`.

### Task 5: Deliver Both OCI Capture Paths

**Files:**
- Create: `internal/images/discover.go`
- Create: `internal/images/discover_test.go`
- Modify: `internal/images/capture.go`
- Modify: `internal/app/images.go`
- Modify: `internal/cli/root.go`
- Modify: `integration/local_lifecycle_test.go`
- Modify: `docs/skills/devpod-hauler.md`

**Interfaces:**
- Consumes: workspace engine commands through the existing structured runner and direct pushes found in the immutable registry cut.
- Produces: a deterministic inventory merging automatically discovered named images and explicitly pushed registry content by immutable digest.

- [ ] Add failing table tests for Docker then Podman discovery, named tags, multiple tags, digest/platform metadata, dangling exclusion, label opt-out, collision-safe private references, and bounded diagnostic output.
- [ ] Add `ImageDiscoverer.Discover(context.Context, EngineScope) (domain.ImageInventory, error)` and inject it into capture/sync/close.
- [ ] Push discovered named images through `CAMP_REGISTRY`, verify registry-provided digests, merge direct pushes, and regenerate the Hauler manifest without mutable-tag authority.
- [ ] Make `camp images capture` execute the same production capture boundary and return inventory evidence.
- [ ] Prove close removes local tag and ID, fresh-controller reopen pulls the exact digest, checks `RepoDigests`, and runs the digest-pinned fixture.
- [ ] Commit with `feat: capture named workspace images`.

### Task 6: Complete MinIO, Multi-Writer, and Recovery Proof

**Files:**
- Modify: `integration/minio_cli_reopen_test.go`
- Modify: `integration/minio_portability_test.go`
- Modify: `integration/minio_cli_reopen_helpers_test.go`
- Modify: `scripts/verify-real-evidence.sh`
- Modify: `docs/skills/s3-publication.md`

**Interfaces:**
- Consumes: exact candidate, digest-pinned MinIO, isolated credentials, generation and pointer repositories.
- Produces: fresh-controller, two-writer, ambiguous-outcome, and credential-redaction receipts.

- [ ] Require exact remote object size and digest readback, fresh-controller reopen, two controllers on one revision, one CAS winner, one safe loser, retained losing generation, branch publication, and lease isolation.
- [ ] Inject upload, metadata, and pointer response-loss cases and prove observation reconciles without duplicate destructive or publishing effects.
- [ ] Scan journals, command output, Robot logs, and retained artifacts for credentials.
- [ ] Ensure RCC Robot discovers and executes the named MinIO lifecycle with no skip.
- [ ] Commit with `test: prove minio concurrency and recovery`.

### Task 7: Extend the TUI Across the Whole Lifecycle

**Files:**
- Create: `internal/setupui/lifecycle.go`
- Create: `internal/setupui/lifecycle_test.go`
- Modify: `internal/cli/terminal.go`
- Modify: `internal/cli/production.go`
- Modify: `internal/presentation/events.go`
- Modify: `docs/skills/terminal-experience.md`
- Add captures: `docs/assets/lifecycle-scene/`

**Interfaces:**
- Consumes: typed lifecycle progress and failure events; never parses subprocess text.
- Produces: one accessible responsive TUI for setup, init, open, attach, sync, close, recover, and status.

- [ ] Add golden scene tests for 80x24, 120x40, 160x48, plain terminal, reduced color, cancellation, resize, failure, and recovery-command rendering.
- [ ] Define stable visual stages for hydrate, services, DevPod, attach, mirror, image capture, archive, upload, pointer, cleanup, and recovery.
- [ ] Reuse the campsite art direction and verified assets while keeping every animation driven by real events.
- [ ] Add black-box terminal captures to RCC Robot and bind them to the exact candidate.
- [ ] Commit with `feat: unify lifecycle tui`.

### Task 8: Finish RCC CI Parity and Release Adoption

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `releasepipeline/workflow_contract_test.go`
- Modify: `releasepipeline/evidence_test.go`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: RCC local/test/robot evidence and direct-job parity during migration.
- Produces: two consecutive complete parity records and release artifacts built once.

- [ ] Add an RCC Robot job that downloads or rebuilds only the exact candidate identity and uploads Robot XML/logs plus cleanup receipts with `if: always()`.
- [ ] Require direct and RCC lanes until two consecutive complete PR/master runs are recorded.
- [ ] Remove duplicated direct source-gate logic only after the repository contains those two immutable run references.
- [ ] Bind release packaging, native amd64/arm64 verification, SBOMs, checksums, attestations, Homebrew metadata, and publication to the already verified candidate.
- [ ] Commit with `ci: require rcc lifecycle evidence`.

### Task 9: Produce Protected Kubernetes Evidence

**Files:**
- Modify: `integration/kubernetes_lifecycle_test.go`
- Modify: `.github/workflows/provider-evidence.yml`
- Modify: `scripts/kubernetes_evidence.py`
- Modify: `releasepipeline/kubernetes_evidence_test.go`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: exact candidate SHA-256, authorized provider/context/kubeconfig, unique namespace.
- Produces: allowlisted lifecycle, OCI capability, and exact cleanup evidence.

- [ ] Prove open, sync, close, fresh-controller reopen, digest-pinned OCI execution, and namespace/resource-ledger cleanup.
- [ ] Bind every retained record to candidate commit, candidate SHA-256, and relevant-change commit.
- [ ] Run the protected workflow and retain a passing artifact before a Kubernetes support claim.
- [ ] Commit with `test: bind kubernetes lifecycle evidence`.

### Task 10: Implement Controller, Blueprint, Provenance, Timeline, and Profiles

**Files:**
- Create: `internal/domain/controller.go`
- Create: `internal/domain/blueprint.go`
- Create: `internal/domain/provenance.go`
- Create: `internal/app/controller.go`
- Create: `internal/app/blueprint.go`
- Create: `internal/app/timeline.go`
- Create: `internal/app/profile.go`
- Modify: `internal/cli/root.go`
- Create: `docs/adr/0007-controller-blueprint-profile-authority.md`
- Modify: `docs/skills/cli-composition.md`

**Interfaces:**
- Produces: independently versioned `ControllerIdentity`, `CampBlueprint`, `BlueprintRef`, `ExecutionBinding`, and `ExecutionProvenance`; journal-projected timeline; immutable non-secret profiles.

- [ ] Define canonical JSON schemas that exclude credentials, provider secrets, host paths, allocated ports, timestamps, and session IDs from portable blueprint identity.
- [ ] Add inspect, timeline, profile import/list/show/current/activate/deactivate commands with stable JSON.
- [ ] Freeze the selected profile digest in every open session and reject retargeting attach/sync/close/recover.
- [ ] Report old generations as unknown-blueprint without inventing compatibility.
- [ ] Commit with `feat: add controller blueprint timeline and profiles`.

### Task 11: Complete CampKit v2

**Files:**
- Modify: `internal/campkit/`
- Modify: `internal/cli/production.go`
- Modify: `internal/cli/root.go`
- Modify: `docs/skills/camp-kits.md`

**Interfaces:**
- Consumes: an exact existing generation, blueprint/profile references, immutable metadata.
- Produces: a verified offline envelope and collision-safe imported lineage.

- [ ] Add generation-ref export, profile binding, blueprint reference, canonical manifest encoding, verification receipts, and deterministic archive tests.
- [ ] Prove export never checkpoints or moves a pointer.
- [ ] Prove import verifies before staging, creates a new file-backed capsule/lineage, and never overwrites an existing pointer.
- [ ] Commit with `feat: complete campkit v2`.

### Task 12: Implement the Performance and Storage Program

**Files:**
- Create: `internal/performance/`
- Create: `integration/performance_baseline_test.go`
- Create: `docs/adr/0008-performance-storage-protocol.md`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: representative generation sizes, file counts, OCI inventories, file/S3 backends, and p50/p95 phase timings.
- Produces: reproducible baselines plus digest-verified cache, pack, reflink, multipart/range, and protocol evidence.

- [ ] Record phase-level CPU, bytes, object count, p50, and p95 for hydrate, mirror, archive, Hauler sync/save/load, upload, download, and reopen.
- [ ] Implement a reconstructible digest-verified read-through cache with no writable workspace hardlinks.
- [ ] Implement pack/reflink paths behind explicit capability checks and ordinary-copy fallback.
- [ ] Exercise existing S3 range/multipart behavior, then implement any remote protocol improvement with bounded corruption and recovery tests.
- [ ] Require at least 20% representative p95 improvement for an optimization to become the default; retain slower safe fallback paths.
- [ ] Commit with `perf: add verified storage acceleration`.

### Task 13: Run Continuous Dogfood and Final Product Proof

**Files:**
- Create: `docs/evidence/dogfood.schema.json`
- Create: `docs/evidence/dogfood-summary.md`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: exact candidate version, controller identity, generation, image digests, elapsed duration, recovery use, and intervention record.
- Produces: at least ten ordinary cycles, six handoffs, and two fresh-controller or cross-machine reopens.

- [ ] Start dogfood as soon as Task 1 restores checkpoint publication and append evidence for every program candidate.
- [ ] Continue feature implementation while dogfood runs; reset the ten-cycle release window after any incorrect pointer, digest, isolation, cleanup, or credential event.
- [ ] Require at least nine of ten final cycles without manual repair and no more than three ordinary-path commands.
- [ ] Publish a final report separating investigated, implemented, tested, pushed, merged, rebuilt, packaged, released, and dogfooded states.
- [ ] Commit with `docs: publish camp product proof`.

### Task 14: Complete the Public Command, IDE, and Provider Contract

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/production.go`
- Modify: `internal/adapters/devpod/client.go`
- Create: `internal/adapters/devpod/ide.go`
- Create: `internal/adapters/devpod/ide_test.go`
- Modify: `internal/target/`
- Modify: `integration/local_lifecycle_test.go`
- Create: `integration/ide_lifecycle_test.go`
- Modify: `docs/skills/cli-composition.md`

**Interfaces:**
- Consumes: the proven lifecycle session, exact DevPod binary, resolved capsule target, and stable presentation envelope.
- Produces: the complete original-prompt command/flag behavior for open, attach, sync, close, status, list, history, recover, config, serve, images, provider, DevPod, Hauler, completion, and IDE entry.

- [ ] Add exact-Cobra-tree tests for every public command, alias, repeated DevPod/SSH option, `--devpod-arg`, `--`, typed/raw conflict, shell completion, and stable JSON/human failure.
- [ ] Prove `camp open [target]` resolves absolute, relative, unique basename, and zoxide targets only beneath the capsule root and reports ambiguity with candidates.
- [ ] Make `camp open /absolute/root` adopt and initialize an obvious uninitialized root through the same setup/init composition without requiring a separate ritual.
- [ ] Add the VS Code-family nested-folder adapter using the exact percent-encoded `vscode-remote://ssh-remote+<workspace>.devpod/<target>` shape; require `vscode-insiders` to invoke `code-insiders` without a redundant interactive shell.
- [ ] Prove tmux-present and tmux-absent attach, root and nested IDE targets, read-only sessions, branches, provider/context flags, raw safe passthrough, serve status/logs/restart, config provenance/redaction, and session-selection ambiguity.
- [ ] Run `rtk go test ./internal/cli ./internal/adapters/devpod ./internal/target ./internal/app -count=1` and the exact-candidate IDE lifecycle test.
- [ ] Commit with `feat: complete camp command and ide contract`.

### Task 15: Complete Managed Tools, Doctor, Installation, and Package Proof

**Files:**
- Modify: `internal/adapters/tools/`
- Modify: `internal/doctor/`
- Modify: `internal/cli/production_setup.go`
- Modify: `packaging/`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `docs/skills/managed-tools.md`
- Modify: `docs/skills/testing-release-evidence.md`

**Interfaces:**
- Consumes: pinned DevPod/Hauler assets, external host `pasta`, backend probes, release candidate archives.
- Produces: clean first-use bootstrap, functional doctor evidence, reproducible archives/packages/Homebrew lifecycle, SBOMs, checksums, and uninstall-safe operator state.

- [ ] Prove checksum-verified concurrent bootstrap, interrupted-download recovery, existing locked-tool reuse, wrong-identity rejection, and no host package-manager mutation.
- [ ] Make doctor prove Linux/architecture, backend conditional writes/readback, DevPod/Hauler identity, SSH/tar/rsync, engine/provider, endpoint reachability, Room availability, and a functional loopback-confined `pasta` child with exact identity-safe cleanup.
- [ ] Run clean DEB, RPM, APK, generic archive, and Homebrew tap/install/completion/update/upgrade/uninstall fixtures while preserving user configuration and shared `passt`.
- [ ] Produce normalized amd64/arm64 archives, checksums, SPDX SBOMs, Homebrew metadata, native verification records, verified-artifact manifest, attestations, and protected publication from one candidate build.
- [ ] Execute every documented install/setup/doctor command against packaged binaries; a missing host prerequisite must be reported as `gated` or `failed`, never passed.
- [ ] Commit with `feat: complete managed install and doctor proof`.

## Final Verification

```bash
./developer/rccw run -r developer/toolkit.yaml --dev -t local
./developer/rccw run -r developer/toolkit.yaml --dev -t test
./developer/rccw run -r developer/toolkit.yaml -t robot
./developer/rccw run -r developer/toolkit.yaml -t robotKubernetes
git diff --check
```

Acceptance requires a clean exact-candidate evidence chain for RCC, Docker/OCI, MinIO, release artifacts, protected Kubernetes, TUI captures, runtime schemas, CampKit, performance, cleanup, and dogfood. A gated provider environment limits the corresponding support claim but does not remove its implementation workstream from this program.
