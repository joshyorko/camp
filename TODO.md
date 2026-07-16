# Camp implementation queue

Authoritative execution ledger for `patchraptor/camp-implementation`. Update this file at every pushed integration checkpoint. Percentages are evidence-weighted estimates, not completion claims.

## Current state

- Overall Tasks 3–7: **16%**.
- Last pushed integration: `f8f15e929f7a845f33b20b4a09edaa515dfbaa20` (`docs: add project README and Camp hero`).
- Active checkpoint base: `f8f15e929f7a845f33b20b4a09edaa515dfbaa20`, upstream `origin/patchraptor/camp-implementation`, ahead/behind `0/0` before the current uncommitted checkpoint.
- Integrator/committer: Sol (`/root`).
- Merge authority: merge the completed, verified PR into the verified default branch; do not deploy, publish a release, or mutate external services.

## Live lanes

| State | Task / issue | Owner | Exclusive write scope | Checkpoint gate |
|---|---|---|---|---|
| CHECKPOINT READY | Task 3 crash-safe ownership marker and owned-root cleanup | `/root/task3_ownership_marker` | `internal/capsule/ownership.go`, `internal/capsule/ownership_test.go` | Focused/race/package tests, cross-mount rejection, deterministic quarantine and marker crash replay passed; Sol full gate passed |
| CHECKPOINT READY | Task 3 crash-safe archive provenance and hydration marker | `/root/task3_hydration_marker` | `internal/adapters/archive/{tarzstd.go,tarzstd_test.go}`, `internal/adapters/hydration/{controller.go,controller_test.go}` | Focused/race/package tests, no-cross-mount cleanup, fsync ordering, exact destination identity passed; Sol full gate passed |
| QUEUED | Task 3 application hydration-intent replay | Sol owns `internal/app/open.go`; next Luna receives a new test file only | `internal/app/open.go` serialized; dedicated new test file | RED: `TestOpenRemoteResumesPendingHydrationStageWithOriginalPlanWithoutDuplicateIntent` |

## Task 3 — complete local lifecycle and recovery (about 70%)

- [x] WorkspaceUp unknown-outcome status observation and resumed opening.
- [x] Durable terminal-entry intent/fact and callback crash-cut recovery.
- [x] Durable recovery objective and hydration plan (`96ef443`).
- [x] Workspace-readiness reconciliation (`e7e7294`).
- [x] Integrate current marker/archive recovery checkpoint with final focused reviews and fresh repository gates; commit/push pending below.
- [ ] Reconcile pending hydration transitions from the original durable token/stage/final plan without duplicate intent/effect/fact.
- [ ] Observe unknown Hauler load/extract outcomes without duplicate destructive work.
- [ ] Complete service readiness, supervisor recovery, and durable opening/recovering objectives.
- [ ] Prove the real local open → edit/image → sync → close → reopen vertical with crash cuts.

## Task 4 — command, IDE, and presentation surface (about 5%)

- [ ] Freeze one canonical session selector/target mapper; do not duplicate `internal/app/session.go` or `internal/workspace/local.go`.
- [ ] Implement typed DevPod flags, SSH forwarding, raw-argument conflict rejection, and VS Code/Insiders/T3 IDE contracts.
- [ ] Implement attach/list/status/history/recover/serve/images/provider/config use cases and stable human/JSON presenters.
- [ ] Wire the complete Cobra tree, aliases, help goldens, and bash/zsh/fish completions.
- [ ] Issue #1: add `t3-code` and `--sites` validation without changing terminal or VS Code-family behavior.

## Task 5 — remote transport and portable controller (0%)

- [ ] Implement rsync mirror, structured tar fallback, exact exclusions, and DevPod-generated SSH target/forward primitives.
- [ ] Expand the serialized mirror request/application boundary; `internal/app/checkpoint.go` currently accepts only `MirrorLocalNoop`.
- [ ] Prove remote open/sync/close/recover uses the same publisher, CAS, lease, and cleanup protocol as local.
- [ ] Issue #1: run T3 inside the DevPod workspace, manage its process/readiness/logs, loopback-only forward, Desktop handoff, teardown, and controller relocation.

## Task 6 — S3/MinIO and multi-controller durability (0%)

- [ ] Implement S3 object-store conditional operations, immutable multipart upload, abort, pagination, checksum/size verification, and safe capability probe.
- [ ] Add configuration/factory wiring without persisting credentials; retain mounted `file://` behavior.
- [ ] Prove two-writer conflict behavior, retained losing generation, branch recovery, and MinIO integration.
- [ ] Issue #1: prove MinIO/S3 is the portable truth and a fresh controller can reopen the last verified checkpoint.

## Task 7 — operations, packaging, documentation, and release evidence (about 5%)

- [ ] Managed tool installer with exact pins/checksums/atomic concurrent installation; keep `pasta` external.
- [ ] Doctor capability model and functional probes, including provider, backend, forwarding, `/proc/self/fd`, T3, and Codex requirements.
- [ ] Packaging, completions, Homebrew metadata, CI/release configuration, SBOM/checksum artifacts, and credential-gated real matrices.
- [ ] Issue #1 pins: tap `de1f2cf4554437a2d52d426b0b80b78f32bc5b8e`, T3 `main.20260716072441.5e8b2c800ecd`, Codex `release.20260608151242.525e9535fcdf`, Sites client `5e8b2c800ecd98e23adbdbfc8970f4687ac8254b`.
- [ ] Issue #1 private Sites pairing/session lifecycle and optional Harvester profile; no secrets in haul, backend, Sites config, cloud-init, or OpenTofu state.
- [ ] Final docs must remain truthful and derive examples from the real command tree. Documentation artwork is parked outside the worktree until implementation checkpoints are current.

## Current blockers

- No concrete checkpoint blocker. The ownership fallback retains a theoretical active same-UID name-substitution window because Linux has no unlink-by-fd primitive; deterministic identity/replay tests pass and unexplained state fails closed.
- Bind-mount fixtures skip in this container with `EPERM`; `openat2` `RESOLVE_NO_XDEV` enforcement and deterministic substitution tests pass.
- No merge is allowed until Tasks 3–7 and issue #1 acceptance criteria have fresh evidence.

## Verification ledger

- `f8f15e9`: `go build -o /tmp/camp-readme-checkpoint ./cmd/camp`; focused tests; `go test ./... -count=1`; `go vet ./...`; `git diff --check` — passed before push.
- Current checkpoint tip: focused capsule/archive/hydration tests, `go test ./... -count=1`, `go vet ./...`, and `git diff --check` passed under Sol immediately before commit.

## Push and merge checklist

- [ ] Review only intended paths and confirm no abandoned/duplicate worker edits.
- [ ] Run focused checkpoint tests.
- [ ] Run fresh `go test ./... -count=1`, `go vet ./...`, and `git diff --check` on the exact tip.
- [ ] Commit as one coherent checkpoint and push `patchraptor/camp-implementation`.
- [ ] Verify local HEAD, upstream, and live GitHub branch SHA match.
- [ ] Repeat for every remaining Task 3–7 checkpoint.
- [ ] Run the full credential-free matrix and record credential-gated evidence honestly.
- [ ] Open or update the normal PR, mark it ready, and merge only after all authorized implementation and final gates complete.
- [ ] Verify the live default-branch SHA contains every intended commit; reconcile worker branches/worktrees; leave the default checkout clean.
