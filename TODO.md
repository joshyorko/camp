# Camp implementation queue

Authoritative execution ledger for `patchraptor/camp-implementation`. Update this file at every pushed integration checkpoint. Percentages are evidence-weighted estimates, not completion claims.

## Current state

- Overall Tasks 3–7: **25%**.
- Last pushed integration: `40518c7f14f9a553fab91d2e4c9bcffa882a2bf6` (`fix: make materialization markers crash safe`).
- Active checkpoint base: `40518c7f14f9a553fab91d2e4c9bcffa882a2bf6`, upstream `origin/patchraptor/camp-implementation`, ahead/behind `0/0` with a clean worktree immediately after push.
- Integrator/committer: Sol (`/root`).
- Merge authority: merge the completed, verified PR into the verified default branch; do not deploy, publish a release, or mutate external services.
- Active pull request: draft [#2](https://github.com/joshyorko/camp/pull/2), targeting verified default branch `master`; mark ready only after final gates.

## Live lanes

| State | Task / issue | Owner | Exclusive write scope | Checkpoint gate |
|---|---|---|---|---|
| PUSHED `40518c7` | Task 3 crash-safe ownership marker and owned-root cleanup | `/root/task3_ownership_marker`; integrated by Sol | `internal/capsule/ownership.go`, `internal/capsule/ownership_test.go` | Focused/race/package and Sol full gates passed |
| PUSHED `40518c7` | Task 3 crash-safe archive provenance and hydration marker | `/root/task3_hydration_marker`; integrated by Sol | `internal/adapters/archive/{tarzstd.go,tarzstd_test.go}`, `internal/adapters/hydration/{controller.go,controller_test.go}` | Focused/race/package and Sol full gates passed |
| PUSHED `744d2bf` | Task 3 hydration replay plus Task 4/5 adapter checkpoint | three prior Luna lanes; integrated by Sol | App hydration recovery, DevPod/IDE, SSH transfer adapters | Focused/race/full gates passed at pushed tip |
| PUSHED `dfb2473` | Task 7 Camp documentation artwork | `/root/docs_artwork`; integrated by Sol | Ten archive-provided images, matching Markdown, artwork ledger | Safe ZIP/PNG/link and full repository gates passed |
| READY FOR CHECKPOINT | Task 3 unknown Hauler outcomes | `/root/task3_hydration_unknown_red`; production fix integrated by Sol | Hydration unknown-outcome regression plus serialized controller | Exact extraction observes ambiguous Load without a second Load; Extract preservation green |
| READY FOR CHECKPOINT | Task 4 attach use case | Sol | New `internal/app/{attach.go,attach_test.go}` | Ownership revalidation, target mapping, exactly one terminal/VS Code entry path |
| READY FOR CHECKPOINT | Task 5 remote workspace transport | `/root/task5_remote_workspace`; integrated by Sol | New `internal/workspace/{remote.go,remote_test.go}` | Rsync/fresh tar fallback/partial discard transport contract |
| READY FOR CHECKPOINT | Task 6 conditional S3/MinIO core | `/root/task6_s3store`; integrated by Sol | New `internal/adapters/s3store/**` | Conditional operations, pagination, verified writer capability probe |

## Task 3 — complete local lifecycle and recovery (about 70%)

- [x] WorkspaceUp unknown-outcome status observation and resumed opening.
- [x] Durable terminal-entry intent/fact and callback crash-cut recovery.
- [x] Durable recovery objective and hydration plan (`96ef443`).
- [x] Workspace-readiness reconciliation (`e7e7294`).
- [x] Push crash-safe marker/archive recovery checkpoint (`40518c7`).
- [x] Reconcile pending hydration transitions from the original durable token/stage/final plan without duplicate intent/effect/fact (current checkpoint).
- [x] Observe unknown Hauler load/extract outcomes without duplicate destructive work (current checkpoint).
- [ ] Complete service readiness, supervisor recovery, and durable opening/recovering objectives.
- [ ] Prove the real local open → edit/image → sync → close → reopen vertical with crash cuts.

## Task 4 — command, IDE, and presentation surface (about 5%)

- [ ] Freeze one canonical session selector/target mapper; do not duplicate `internal/app/session.go` or `internal/workspace/local.go`.
- [x] Implement typed DevPod flags, SSH forwarding, raw-argument conflict rejection, and VS Code/Insiders/T3 IDE adapter contracts (current checkpoint; app/CLI wiring remains).
- [ ] Implement attach/list/status/history/recover/serve/images/provider/config use cases and stable human/JSON presenters.
- [x] Implement the ownership-safe terminal and VS Code-family `attach` application use case (current checkpoint; CLI/T3 remain).
- [ ] Wire the complete Cobra tree, aliases, help goldens, and bash/zsh/fish completions.
- [ ] Issue #1: add `t3-code` and `--sites` validation without changing terminal or VS Code-family behavior.

## Task 5 — remote transport and portable controller (0%)

- [x] Implement rsync mirror, structured tar fallback, exact exclusions, and DevPod-generated SSH target/forward primitives (current checkpoint; workspace integration remains).
- [ ] Expand the serialized mirror request/application boundary; `internal/app/checkpoint.go` currently accepts only `MirrorLocalNoop`.
- [ ] Prove remote open/sync/close/recover uses the same publisher, CAS, lease, and cleanup protocol as local.
- [ ] Issue #1: run T3 inside the DevPod workspace, manage its process/readiness/logs, loopback-only forward, Desktop handoff, teardown, and controller relocation.

## Task 6 — S3/MinIO and multi-controller durability (0%)

- [ ] Implement S3 object-store conditional operations, immutable multipart upload, abort, pagination, checksum/size verification, and safe capability probe.
- [ ] Add configuration/factory wiring without persisting credentials; retain mounted `file://` behavior.
- [x] Add a credential-free conditional S3 HTTP core with opaque revisions, pagination, and a fail-closed writer probe (current checkpoint; factory wiring and multipart remain).
- [ ] Prove two-writer conflict behavior, retained losing generation, branch recovery, and MinIO integration.
- [ ] Issue #1: prove MinIO/S3 is the portable truth and a fresh controller can reopen the last verified checkpoint.

## Task 7 — operations, packaging, documentation, and release evidence (about 5%)

- [ ] Managed tool installer with exact pins/checksums/atomic concurrent installation; keep `pasta` external.
- [ ] Doctor capability model and functional probes, including provider, backend, forwarding, `/proc/self/fd`, T3, and Codex requirements.
- [ ] Packaging, completions, Homebrew metadata, CI/release configuration, SBOM/checksum artifacts, and credential-gated real matrices.
- [ ] Issue #1 pins: tap `de1f2cf4554437a2d52d426b0b80b78f32bc5b8e`, T3 `main.20260716072441.5e8b2c800ecd`, Codex `release.20260608151242.525e9535fcdf`, Sites client `5e8b2c800ecd98e23adbdbfc8970f4687ac8254b`.
- [ ] Issue #1 private Sites pairing/session lifecycle and optional Harvester profile; no secrets in haul, backend, Sites config, cloud-init, or OpenTofu state.
- [ ] Final docs must remain truthful and derive examples from the real command tree. The validated ten-image documentation artwork set is linked in the current docs commit.

## Current blockers

- No concrete checkpoint blocker. The ownership fallback retains a theoretical active same-UID name-substitution window because Linux has no unlink-by-fd primitive; deterministic identity/replay tests pass and unexplained state fails closed.
- Bind-mount fixtures skip in this container with `EPERM`; `openat2` `RESOLVE_NO_XDEV` enforcement and deterministic substitution tests pass.
- No merge is allowed until Tasks 3–7 and issue #1 acceptance criteria have fresh evidence.

## Child disposition

- `019f6b47-dc00-7a90-aa74-75cbf12d4e38`: **superseded/stopped** duplicate shared-checkout owner; its unique TODO and hydration RED evidence were adopted, and its marker work is contained in `40518c7`/`744d2bf`.
- `/root/task3_ownership_marker`: **integrated** in `40518c7`.
- `/root/task3_hydration_marker`: **integrated** in `40518c7`.
- `/root/task3_app_hydration_red`: **integrated** in `744d2bf`.
- `/root/task4_devpod_ide`: **integrated** in `744d2bf`.
- `/root/task5_sshtransfer`: **integrated** in `744d2bf`.
- `/root/docs_artwork`: **integrated** in `dfb2473`; all ten ZIP images remain linked.
- `/root/task3_hydration_unknown_red`: **integrated in current checkpoint**; lane stopped after handoff.
- `/root/task5_remote_workspace`: **integrated in current checkpoint**; lane stopped after handoff.
- `/root/task6_s3store`: **integrated in current checkpoint**; lane stopped after handoff.

## Verification ledger

- `f8f15e9`: `go build -o /tmp/camp-readme-checkpoint ./cmd/camp`; focused tests; `go test ./... -count=1`; `go vet ./...`; `git diff --check` — passed before push.
- `40518c7`: focused capsule/archive/hydration tests, `go test ./... -count=1`, `go vet ./...`, and `git diff --check` passed under Sol immediately before commit; live branch SHA verified equal after push.

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

## Documentation artwork ledger

The supplied archive was validated as exactly ten safe repository-relative PNG entries. Every image is 1774×887, has valid PNG chunk CRCs, and decompresses successfully. No archive entry was rejected.

| Archive entry | Destination asset | Linked document | Validation |
|---|---|---|---|
| `docs/assets/adr-0001-lifecycle-controller.png` | `docs/assets/adr-0001-lifecycle-controller.png` | `docs/adr/0001-hauler-devpod-boundary.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/adr-0002-generations-leases-recovery.png` | `docs/assets/adr-0002-generations-leases-recovery.png` | `docs/adr/0002-generations-leases-and-recovery.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/adr-0003-pinned-toolchain.png` | `docs/assets/adr-0003-pinned-toolchain.png` | `docs/adr/0003-pinned-toolchain.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/adr-0004-config-registry-contracts.png` | `docs/assets/adr-0004-config-registry-contracts.png` | `docs/adr/0004-capsule-devcontainer-and-registry-contracts.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/adr-0005-ownership-supervision.png` | `docs/assets/adr-0005-ownership-supervision.png` | `docs/adr/0005-materialization-ownership-and-supervision.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/adr-0006-loopback-confinement.png` | `docs/assets/adr-0006-loopback-confinement.png` | `docs/adr/0006-hauler-loopback-confinement.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/oracle-architecture-review.png` | `docs/assets/oracle-architecture-review.png` | `docs/reviews/2026-07-14-oracle-architecture-review.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/camp-implementation-plan.png` | `docs/assets/camp-implementation-plan.png` | `docs/superpowers/plans/2026-07-14-camp.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/task-3b-atomic-materialization.png` | `docs/assets/task-3b-atomic-materialization.png` | `docs/superpowers/tasks/2026-07-14-camp-task-3b.md` | Valid PNG; 1774×887; linked once |
| `docs/assets/camp-codex-build-prompt.png` | `docs/assets/camp-codex-build-prompt.png` | `prompts/camp-codex-build-prompt.md` | Valid PNG; 1774×887; linked once |
