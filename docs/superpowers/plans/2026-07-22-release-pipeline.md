# Verifiable Release Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a least-privilege CI and gated release pipeline whose downloaded artifacts, checksums, SBOMs, smoke results, and publication evidence can be independently verified.

**Architecture:** Repository tests enforce workflow policy and release-evidence schemas. Credential-free CI and release-candidate construction remain mandatory, while protected provider and publication jobs are explicit downstream gates.

**Tech Stack:** Go 1.25, Bash, GitHub Actions, Docker, MinIO, SPDX JSON, GitHub artifact attestations

## Global Constraints

- Do not publish a release or use secrets in this task.
- Pull-request jobs have no write permissions or protected environment.
- Pin every third-party Action to an immutable commit SHA.
- Validate workflow syntax without mutating the Bluefin host.
- Keep issue #13 open until successful hosted-run evidence satisfies every acceptance criterion.

---

### Task 1: Workflow policy contract

**Files:** Create `releasepipeline/workflow_contract_test.go`; create `.github/workflows/ci.yml`.

- [ ] Write tests that require immutable Action pins, read-only permissions, concurrency, retention, mandatory unit/race/vet/vulnerability/integration/MinIO/package jobs, and forbid secret-presence conditionals.
- [ ] Run `go test ./releasepipeline -count=1` and confirm failure because workflows are absent.
- [ ] Add the minimal CI workflow that satisfies the contract.
- [ ] Run the focused test and commit the green slice.

### Task 2: Release evidence contract

**Files:** Create `releasepipeline/evidence_test.go`; create `packaging/build-release-evidence.sh`; create `.github/workflows/release.yml`.

- [ ] Write tests that build archives and require verified checksums, exact artifact digests in SPDX SBOMs, commit/platform/result/gate records, and smoke execution from extracted downloads.
- [ ] Run the focused test and confirm failure because evidence generation is absent.
- [ ] Implement deterministic evidence generation and the build/upload/download/verify workflow.
- [ ] Run the focused test and commit the green slice.

### Task 3: Protected publication and provider evidence

**Files:** Modify `.github/workflows/release.yml`; create `.github/workflows/provider-evidence.yml`; modify `releasepipeline/workflow_contract_test.go`.

- [ ] Extend tests to require protected environments, minimal scoped write permissions, explicit provider profiles, recorded gates, tag/manual boundaries, and downstream publication after downloaded-artifact verification.
- [ ] Run the focused test and confirm the new assertions fail.
- [ ] Add gated provider and publication jobs without using credentials or triggering side effects.
- [ ] Run the focused test and commit the green slice.

### Task 4: Canonical documentation and full verification

**Files:** Modify `docs/skills/testing-release-evidence.md`.

- [ ] Replace the stale no-workflow statement with exact local and hosted evidence rules, integrity/authenticity distinction, provider gating, and closure checklist.
- [ ] Run `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, `go build ./cmd/camp`, containerized workflow linting, and `git diff --check`.
- [ ] Push the verified commits and create/update a draft PR. Add `Closes #13` only if hosted run evidence proves the complete acceptance criteria.
