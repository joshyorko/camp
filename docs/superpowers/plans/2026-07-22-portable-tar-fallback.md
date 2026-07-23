# Portable Tar Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the forced-rsync-unavailable mirror fallback work with the BusyBox tar installed in the supported runtime.

**Architecture:** Keep the existing direct producer-to-consumer pipe and fresh-staging safety model. Restrict the local extraction argv to options shared by GNU and BusyBox tar, relying on the archive headers and default extraction behavior to preserve supported filesystem semantics.

**Tech Stack:** Go, `os/exec`, BusyBox tar, Go unit and integration tests.

## Global Constraints

- Preserve the no-shell pipe and fresh-attempt staging boundary.
- Do not rerun the real DevPod/MinIO lifecycle gate unless this change invalidates that evidence.
- Verify RED before production code and GREEN afterward.

---

### Task 1: Portable tar extraction argv

**Files:**
- Modify: `internal/adapters/sshtransfer/tarpipe.go`
- Test: `internal/adapters/sshtransfer/tarpipe_test.go`
- Test: `integration/remote_mirror_test.go`
- Modify: `docs/skills/local-lifecycle-recovery.md`

**Interfaces:**
- Consumes: `BuildTarPipe(TarPipeSpec) (TarPipe, error)`
- Produces: a `TarPipe.Consumer.Argv` accepted by BusyBox and GNU tar.

- [ ] **Step 1: Write the failing test**

Update `TestTarPipeFallbackIsStructured` to require exactly `--extract`, `--file=-`, and `--directory=<fresh-root>` for the consumer, with no GNU-only flags.

- [ ] **Step 2: Run the unit test to verify it fails**

Run: `go test ./internal/adapters/sshtransfer -run '^TestTarPipeFallbackIsStructured$' -count=1 -v`

Expected: FAIL because the current argv also contains `--same-permissions` and `--delay-directory-restore`.

- [ ] **Step 3: Write minimal implementation**

Remove only the two GNU-specific consumer arguments from `BuildTarPipe`.

- [ ] **Step 4: Run focused tests to verify they pass**

Run: `go test ./internal/adapters/sshtransfer -count=1`

Run: `go test ./integration -run '^TestRemoteCheckpointLifecycleTransfersRealFilesystemSemantics/forced_unavailable_rsync' -count=1 -v`

Expected: PASS, including bytes, permissions, hard links, symlinks, exclusions, and discarded failed-rsync staging.

- [ ] **Step 5: Document and verify**

Update `docs/skills/local-lifecycle-recovery.md` to state that the local tar consumer uses the portable option subset proved against BusyBox while retaining the existing fresh-staging and filesystem-semantics contract.

Run: `gofmt -w internal/adapters/sshtransfer/tarpipe.go internal/adapters/sshtransfer/tarpipe_test.go`

Run: `go test ./... -count=1`

Run: `go vet ./...`

Run: `git diff --check`

Expected: all commands exit zero.

- [ ] **Step 6: Commit**

```bash
git add internal/adapters/sshtransfer/tarpipe.go internal/adapters/sshtransfer/tarpipe_test.go docs/skills/local-lifecycle-recovery.md docs/superpowers/plans/2026-07-22-portable-tar-fallback.md
git commit -m "fix: make tar mirror fallback portable"
```
