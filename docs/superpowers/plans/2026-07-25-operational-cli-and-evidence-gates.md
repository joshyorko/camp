# Operational CLI and Evidence Gates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose Camp's existing operational application use cases through truthful production commands, generate their reference artifacts, and make the real lifecycle/backend evidence gates exact and discoverable.

**Architecture:** Keep command parsing in `internal/cli`, application selection and safety in `internal/app`, and production adapter composition in `internal/cli/production.go`. Delivery 1 owns manifest, migration, and selector semantics; this delivery forwards only the existing `app.SessionSelector` fields and isolates all Cobra/root overlap in one commit. Evidence gates remain opt-in when they require externally provisioned tools, but named acceptance entrypoints must exist and must fail rather than skip once explicitly selected.

**Tech Stack:** Go 1.24, Cobra, existing Camp application/adapters, Go integration tests, generated Markdown and bash/zsh/fish completions.

## Global Constraints

- Do not implement or assume Delivery 1 manifest, migration, or selector behavior.
- Do not touch `.claude/` or `test-camp-setup/`.
- Do not implement Devsy issue #52 or T3/Sites issue #1.
- Do not add a command unless a real application use case and production composition exist.
- Keep `internal/cli/root.go` and related Cobra goldens in a separate commit for later integration.
- Treat skipped real-tool tests as missing evidence, never success.
- Improve `docs/skills/` with durable, verified operational knowledge.

---

### Task 1: Cobra operational command contracts

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: existing `app.SessionSelector` fields through CLI request DTOs only.
- Produces: `status`, `images list|capture|restore`, `serve status|logs|restart`, and read-only `provider list` command requests.
- Explicit blocker: configuration mutation and provider mutation remain absent unless a production-safe application port exists.

- [ ] **Step 1: Write failing command-boundary tests**

Add table-driven tests that execute each new subtree against a recording lifecycle, assert exact request DTOs, command arity, `--json` propagation, and deterministic root/subcommand help. Assert unsupported mutation names return usage errors before any lifecycle call.

- [ ] **Step 2: Run tests to verify RED**

Run: `rtk go test ./internal/cli -run 'TestOperational|TestRootHelp' -count=1`

Expected: FAIL because the operational interfaces and commands are absent.

- [ ] **Step 3: Implement minimal command DTOs and conditional interfaces**

Add narrow optional lifecycle interfaces so commands are registered only when the injected lifecycle implements the real method. Convert flags into existing selector-compatible fields without adding resolution or defaulting semantics.

- [ ] **Step 4: Run tests to verify GREEN**

Run: `rtk go test ./internal/cli -run 'TestOperational|TestRootHelp' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the integration-sensitive CLI boundary**

Commit: `feat(cli): expose operational command contracts`

### Task 2: Production operational composition and presentation

**Files:**
- Modify: `internal/cli/production.go`
- Create or modify: focused `internal/cli/production_*_test.go`
- Modify only if required by an existing port: `internal/adapters/devpod/`

**Interfaces:**
- Consumes: `app.OperationalQueries.Status`, `app.ImageOperations`, `app.Serve`, and `app.Providers.List`.
- Produces: stable human tables/text and versioned JSON success envelopes through `writeSuccess`.
- Explicit blockers: no `config` subtree unless an application-owned read/update/lock contract exists; no provider update because `app.ErrProviderMutationUnsupported` is the current contract.

- [ ] **Step 1: Write failing production tests**

Use injected composition seams or focused presenter helpers to prove status, image inventory/capture/restore, serve status/log/restart, and redacted provider-list output. Each helper test must name the production method or presenter change that would make it fail.

- [ ] **Step 2: Run tests to verify RED**

Run: `rtk go test ./internal/cli -run 'TestProduction(Status|Images|Serve|Providers)' -count=1`

Expected: FAIL because production lifecycle methods are absent.

- [ ] **Step 3: Compose existing application use cases**

Construct the existing journal, operation lock, recovery guard, service controller/log reader, image capturer/restorer, operational observer/history, and DevPod provider reader from production configuration. Do not duplicate application safety checks in Cobra handlers.

- [ ] **Step 4: Run focused and package tests to verify GREEN**

Run: `rtk go test ./internal/cli ./internal/app ./internal/images -count=1`

Expected: PASS.

- [ ] **Step 5: Commit production wiring**

Commit: `feat(cli): compose operational application commands`

### Task 3: Generated reference artifacts and transcript truth

**Files:**
- Modify: `internal/docsgen/generate.go`
- Modify: `internal/docsgen/generate_test.go`
- Regenerate: `docs/generated/commands.md`
- Regenerate: `docs/generated/transcripts.md`
- Regenerate: `docs/generated/completions/camp.bash`
- Regenerate: `docs/generated/completions/camp.zsh`
- Regenerate: `docs/generated/completions/camp.fish`

**Interfaces:**
- Consumes: the live production-shaped command tree with a transcript lifecycle implementing only real command contracts.
- Produces: byte-reproducible command help, completions, and deterministic human/JSON transcripts.

- [ ] **Step 1: Write failing generator coverage**

Assert every visible operational leaf appears in the command reference, each transcript invokes a registered real contract, and two generations are byte-identical.

- [ ] **Step 2: Run tests to verify RED**

Run: `rtk go test ./internal/docsgen ./docs -count=1`

Expected: FAIL until transcript fixtures and generated files cover the new tree.

- [ ] **Step 3: Extend the transcript lifecycle and generator**

Add deterministic outputs for the newly wired leaves, regenerate with `rtk go run ./cmd/camp-docs`, and do not hand-edit generated files.

- [ ] **Step 4: Verify generated outputs**

Run: `rtk go test ./internal/docsgen ./docs -count=1`

Expected: PASS with no generated diff after a second generation.

- [ ] **Step 5: Commit generated contracts**

Commit: `docs: generate operational command references`

### Task 4: Exact lifecycle, crash, MinIO, and file-backend gates

**Files:**
- Modify or create: `integration/local_lifecycle_test.go`
- Modify or create: `integration/forwarder_crash_test.go`
- Modify or create: `integration/minio_cli_reopen_test.go`
- Modify or create: `integration/minio_portability_test.go`
- Create: `scripts/verify-real-evidence.sh`
- Modify: `.github/workflows/test.yml` only where a credential-free gate can run with repository-owned setup.

**Interfaces:**
- Produces named discoverable gates: `TestLocalLifecycleVertical`, `TestLocalLifecycleCrashMatrix`, `TestMinIOLifecycleVertical`, `TestS3TwoWriterConflict`, and `TestMountedFileBackendParity`.
- A selected gate exits nonzero for missing prerequisites; an unselected opt-in test may skip and must be reported as missing evidence.

- [ ] **Step 1: Write failing discovery test or run named gate discovery**

Run: `rtk go test -list 'TestLocalLifecycleVertical|TestLocalLifecycleCrashMatrix|TestMinIOLifecycleVertical|TestS3TwoWriterConflict|TestMountedFileBackendParity' ./integration`

Expected: identify every missing exact issue-owned test name.

- [ ] **Step 2: Add the smallest truthful aliases/harnesses**

Reuse existing real process and backend helpers. Do not replace process-death or external-tool evidence with fake adapters. The evidence script first discovers each name, then runs it with the required opt-in environment and preserves PASS/FAIL/SKIP classification.

- [ ] **Step 3: Run credential-free gates available on this host**

Run the file-backend and MinIO gates when their container prerequisites are available. Run real DevPod/Hauler gates only with pinned identities; otherwise record the exact missing executable/capability.

- [ ] **Step 4: Commit evidence entrypoints**

Commit: `test(integration): make real evidence gates discoverable`

### Task 5: Canonical testing and release guidance

**Files:**
- Modify: `docs/release.md`
- Modify: `docs/setup-and-doctor.md`
- Modify: `docs/recovery.md`
- Modify: `docs/skills/cli-composition.md`
- Modify: `docs/skills/local-lifecycle-recovery.md`

**Interfaces:**
- Produces exact commands, prerequisite boundaries, skip classification, and current blockers backed by source/tests from Tasks 1-4.

- [ ] **Step 1: Add docs tests that reject stale command/evidence claims**

Extend existing docs tests to require the named commands and gates and to reject claims that skipped tests prove readiness.

- [ ] **Step 2: Run docs tests to verify RED**

Run: `rtk go test ./docs -count=1`

Expected: FAIL until canonical guides contain the new verified contracts.

- [ ] **Step 3: Update canonical guidance**

Document current shipped commands, exact real-evidence commands, production blockers for config/provider mutation, and the distinction among generated, unit-tested, real-tool, built, installed, and released evidence.

- [ ] **Step 4: Run docs tests to verify GREEN**

Run: `rtk go test ./docs -count=1`

Expected: PASS.

- [ ] **Step 5: Commit canonical guidance**

Commit: `docs: define operational evidence and release gates`

### Task 6: Final verification and handoff

**Files:**
- Verify all changed files; do not modify unrelated user-owned paths.

- [ ] **Step 1: Run full test and static gates**

Run:

```text
rtk go test ./... -count=1
rtk go test -race ./internal/cli ./internal/app ./internal/images ./internal/doctor ./integration
rtk go vet ./...
rtk go build ./cmd/camp
rtk git diff --check
```

- [ ] **Step 2: Run available real-tool gates**

Discover every named gate before execution. Record missing pinned DevPod, Hauler, `pasta`, container engine, or MinIO capability as missing evidence, not success.

- [ ] **Step 3: Review scope and commit graph**

Verify `.claude/` and `test-camp-setup/` are untouched; verify the Cobra/root commit is independent; list commits in integration order after Delivery 1.
