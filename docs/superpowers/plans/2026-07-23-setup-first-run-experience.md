# Camp Setup First-Run Experience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `camp setup` configure, initialize, prepare, and render a truthful animated campsite as one obvious first-run command.

**Architecture:** Extend the setup CLI boundary with stdin, keep prompting in a focused pure helper, and reuse `ProductionLifecycle.Init` for validation and atomic persistence. Preserve detailed JSON while making human setup output concise.

**Tech Stack:** Go, Cobra, existing Camp configuration/init/presentation packages.

## Global Constraints

- Human setup must not print managed paths, checksums, or `PATH` exports.
- JSON setup output remains stable and detailed.
- Configuration values are non-secret and persist through the existing atomic config store.
- Animation uses only authoritative completed facts and the existing terminal capability gate.

---

### Task 1: First-run prompt and CLI boundary

**Files:**
- Create: `internal/cli/setup_prompt.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/production_setup.go`
- Test: `internal/cli/setup_prompt_test.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: Cobra `command.InOrStdin()`, `InitRequest`, and resolved XDG defaults.
- Produces: `promptSetupRequest(io.Reader, io.Writer, setupPromptDefaults) (InitRequest, error)` and a setup interface that receives stdin.

- [ ] **Step 1: Write failing tests**

Add tests proving empty answers select source/backend/provider/context defaults, capsule derives from the selected source, explicit answers win, EOF fails, and the Cobra setup boundary passes stdin.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/cli -run 'TestPromptSetup|TestLifecycleCommandsDelegate' -count=1`

Expected: FAIL because the prompt helper and stdin-aware setup interface do not exist.

- [ ] **Step 3: Implement the minimal prompt and setup orchestration**

Read one trimmed line per prompt, select the documented default when empty, reject missing required values, call the existing configured init path with output discarded, then continue tool setup and campsite rendering.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/cli -run 'TestPromptSetup|TestLifecycleCommandsDelegate' -count=1`

Expected: PASS.

### Task 2: Concise human output

**Files:**
- Modify: `internal/cli/production_setup.go`
- Test: `internal/cli/production_setup_test.go`

**Interfaces:**
- Consumes: existing `setupResult` and completed-tool callbacks.
- Produces: concise human output; unchanged detailed JSON result.

- [ ] **Step 1: Write a failing regression test**

Assert human output contains verified tool events but excludes `ready at`, `sha256`, `export PATH`, and managed executable paths. Retain JSON assertions for paths, digests, and `pathExport`.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/cli -run 'TestRunManagedToolSetup' -count=1`

Expected: FAIL because current human output prints paths, checksums, and the export.

- [ ] **Step 3: Implement minimal output separation**

Keep constructing the complete result for JSON. In human mode, return after verified completion events without rendering machine details.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/cli -run 'TestRunManagedToolSetup' -count=1`

Expected: PASS.

### Task 3: Canonical guidance, complete verification, and publication

**Files:**
- Modify: `docs/skills/terminal-experience.md`
- Modify generated command documentation if its golden changes.

**Interfaces:**
- Consumes: verified first-run behavior.
- Produces: durable operator guidance and release evidence.

- [ ] **Step 1: Update canonical guidance**

Document that first-run human setup prompts before initialization, JSON never prompts, Camp resolves its managed tools internally, and normal human output must not request a PATH export.

- [ ] **Step 2: Run complete gates**

Run focused tests, `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, `go build ./cmd/camp`, and `git diff --check`.

- [ ] **Step 3: Publish and merge**

Commit the verified slice, push `patchraptor/setup-first-run-ux`, open a ready PR closing issue #45, wait for required CI, and merge to `master`.

- [ ] **Step 4: Rebuild exact master**

Fetch the merged commit, build with the repository's version metadata into `~/.local/bin/camp`, verify `camp --version` matches `origin/master`, and run the first-run setup smoke in an isolated XDG/controller fixture before handing the real command to the user.

