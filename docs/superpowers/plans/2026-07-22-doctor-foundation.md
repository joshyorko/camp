# Doctor Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a smallest usable, read-only `camp doctor` command that reports host tool, confinement, backend configuration, and credential-chain capabilities without leaking secrets.

**Architecture:** A new `internal/doctor` package owns the stable report model, probes, aggregation, and presentation. Production CLI composition supplies bounded Linux probes; it does not install tools or create DevPod, Kubernetes, forwarding, or backend lifecycle resources.

**Tech Stack:** Go, Cobra, existing Camp configuration and confinement adapters, AWS SDK v2 credential chain.

## Global Constraints

- Work only from the isolated `patchraptor/issue-11-doctor` branch based on current `origin/master`.
- Follow strict RED-GREEN-REFACTOR for every behavior.
- Never emit credential values, credential-bearing URLs, or raw probe causes.
- Do not run real DevPod, Kubernetes, Hauler service, forwarding, or workspace lifecycle tests.
- Keep managed installation and lock-backed executable resolution in issue #10.
- Keep issue #11 open because backend CAS, forwarding, workspace, service, T3, and Codex probes remain out of this slice.

---

### Task 1: Stable report model and rendering

**Files:**
- Create: `internal/doctor/report.go`
- Create: `internal/doctor/report_test.go`

**Interfaces:**
- Produces: `Report`, `Result`, stable status constants, `RenderHuman(io.Writer, Report)`, and `RenderJSON(io.Writer, Report)`.

- [ ] Write table-driven tests for deterministic result ordering, versioned JSON, human status/code/remediation, and redaction.
- [ ] Run `go test ./internal/doctor -run 'TestRender' -count=1` and verify the tests fail because the package API is absent.
- [ ] Implement only the typed model, deterministic rendering, and redaction needed by the tests.
- [ ] Rerun the focused test and verify it passes.

### Task 2: Read-only capability probes

**Files:**
- Create: `internal/doctor/probes.go`
- Create: `internal/doctor/probes_test.go`

**Interfaces:**
- Produces: `Probe` and constructors for observed tool identity, pasta confinement, backend configuration, and S3 credential-chain availability.
- Consumes: injected path lookup, command runner, file hashing, configuration resolver, confinement resolver, and credential checker seams.

- [ ] Write failing tests for executable absence, bounded command failure, digest evidence, functional pasta capability, file backend configuration, S3 credential availability, not-configured behavior, and secret-free causes.
- [ ] Run `go test ./internal/doctor -run 'Test.*Probe' -count=1` and verify expected assertion failures.
- [ ] Implement the minimal read-only probes with per-probe context deadlines and stable result codes.
- [ ] Rerun the focused tests and verify they pass.

### Task 3: CLI and production composition

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`
- Modify: `internal/cli/production.go`
- Create: `internal/cli/doctor_test.go`

**Interfaces:**
- Produces: `camp doctor [--json]` with no positional arguments.
- Consumes: `doctor.Runner` and the report renderers.

- [ ] Write failing CLI tests for command registration, strict arguments, human output, JSON output, and nonzero blocked results.
- [ ] Run `go test ./internal/cli -run 'TestDoctor' -count=1` and verify expected failures.
- [ ] Add the command and production dependency composition without changing other lifecycle handlers.
- [ ] Rerun the focused tests and verify they pass.

### Task 4: Durable operational guidance and publication

**Files:**
- Modify: `docs/skills/cli-composition.md`
- Modify: `docs/skills/managed-tools.md`

**Interfaces:**
- Produces: canonical guidance describing exactly what doctor proves and what remains unproved.

- [ ] Update the canonical guides with verified status semantics, probe boundaries, redaction rules, and exclusions.
- [ ] Run focused tests and `git diff --check`, then commit the first meaningful RED/implementation slice.
- [ ] Push `patchraptor/issue-11-doctor` and immediately open a draft PR targeting `master`, explicitly preserving issue #11 as open.
- [ ] Run `go test ./... -count=1`, `go test -race ./internal/doctor ./internal/cli`, `go vet ./...`, `go build ./cmd/camp`, and `git diff --check`.
- [ ] Update the draft PR with exact verification and explicit skipped real-tool lifecycle gates.
