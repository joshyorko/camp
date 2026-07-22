# Functional Doctor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `camp doctor` functionally prove issue #11 capabilities with bounded, redacted, identity-safe probes.

**Architecture:** Add focused probe files under `internal/doctor` around narrow injected interfaces, then compose real adapters in `internal/cli`. Each mutating probe owns a unique resource lifecycle and reports cleanup failure as blocked.

**Tech Stack:** Go 1.24, standard library, Camp ports/adapters, Go `testing`.

## Global Constraints

- No production behavior is written before its focused test fails for the expected missing behavior.
- Each probe is independently time-bounded and cleanup is separately bounded.
- Never delete or stop a resource unless its recorded identity still matches.
- Never expose credentials or raw causes in report evidence.
- A skipped real-tool test is not functional acceptance evidence.

---

### Task 1: Host and managed-tool probes

**Files:**
- Create: `internal/doctor/host_probes.go`
- Create: `internal/doctor/host_probes_test.go`
- Modify: `internal/doctor/probes.go`
- Modify: `internal/doctor/probes_test.go`
- Modify: `internal/cli/production.go`

**Interfaces:**
- Produces: independent `Probe` implementations for file descriptors, TUN, user namespaces, LSM, container boundary, and lock-backed tool identity.

- [ ] Write focused tests asserting stable healthy/blocked evidence and managed digest/version matching.
- [ ] Run `rtk go test ./internal/doctor -run 'Test(Proc|Tun|UserNamespace|LSM|Container|ManagedTool)' -count=1` and confirm failures describe missing behavior.
- [ ] Implement minimal probe behavior and production composition.
- [ ] Re-run the focused command and `rtk go test ./internal/doctor ./internal/cli -count=1` until green.
- [ ] Commit only the task files with `feat: prove doctor host capabilities`.

### Task 2: Functional pasta confinement

**Files:**
- Create: `internal/doctor/pasta_probe.go`
- Create: `internal/doctor/pasta_probe_test.go`
- Modify: `internal/cli/production.go`

**Interfaces:**
- Produces: `PastaRuntime` with start, inspect, reach, stop, and teardown verification operations; `PastaProbe` implements `Probe`.

- [ ] Write tests for namespace/listener success, identity mismatch, timeout, and cleanup failure.
- [ ] Run `rtk go test ./internal/doctor -run TestPasta -count=1` and confirm expected failures.
- [ ] Implement the minimal identity-safe orchestration and Linux real-process adapter.
- [ ] Re-run focused and package tests until green.
- [ ] Commit with `feat: prove pasta runtime confinement`.

### Task 3: Backend transaction probe

**Files:**
- Create: `internal/doctor/backend_probe.go`
- Create: `internal/doctor/backend_probe_test.go`
- Modify: `internal/doctor/probes.go`
- Modify: `internal/cli/production.go`

**Interfaces:**
- Produces: `BackendTransactions` with create, conditional replace, read, and conditional delete operations; `BackendIOProbe` implements `Probe`.

- [ ] Write tests for exact readback, stale-write conflict, identity mismatch, cleanup failure, and absent configuration.
- [ ] Run `rtk go test ./internal/doctor -run TestBackend -count=1` and confirm the new functional assertions fail.
- [ ] Implement the unique-prefix transaction and compose the existing file/S3 object-store adapter.
- [ ] Re-run focused and package tests until green.
- [ ] Commit with `feat: prove doctor backend transactions`.

### Task 4: Configured reachability and truthful reports

**Files:**
- Create: `internal/doctor/reachability.go`
- Create: `internal/doctor/reachability_test.go`
- Modify: `internal/doctor/report_test.go`
- Modify: `internal/cli/doctor_test.go`
- Modify: `internal/cli/production.go`

**Interfaces:**
- Produces: configured probes for provider, forwarding, workspace, and service contracts, each returning `skipped-not-configured` when prerequisites are absent.

- [ ] Write human/JSON goldens and focused tests for configured success, absent configuration, reachability failure, timeout, and cleanup failure.
- [ ] Run `rtk go test ./internal/doctor ./internal/cli -run 'Test.*(Configured|Reachability|Doctor|Render)' -count=1` and confirm expected failures.
- [ ] Implement minimal configured probe composition and stable evidence.
- [ ] Re-run focused and package tests until green.
- [ ] Commit with `feat: add configured doctor reachability`.

### Task 5: Canonical guidance and final verification

**Files:**
- Modify: `docs/skills/cli-composition.md` or `docs/skills/managed-tools.md`

**Interfaces:**
- Documents only behavior proved by the preceding code and commands.

- [ ] Replace stale syntax-only doctor claims with exact functional evidence and remaining real-tool limitations.
- [ ] Run `rtk gofmt -w` on changed Go files.
- [ ] Run `rtk go test ./... -count=1`, `rtk go test -race ./... -count=1`, `rtk go vet ./...`, `rtk go build ./cmd/camp`, and `rtk git diff --check`.
- [ ] Commit with `docs: record functional doctor evidence` and push the green branch.
- [ ] Update draft PR #37 without `Closes #11` unless every acceptance criterion, including non-skipped real pasta and configured probes, is proven.

