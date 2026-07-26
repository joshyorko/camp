# Unified Setup and Init Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make human `camp setup` prepare the machine and initialize the selected camp in one continuous plain or rich workflow.

**Architecture:** Extend the setup request to carry camp root and identity, then make the existing setup orchestration persist machine defaults, resolve locked tools, and delegate camp creation to the existing `ProductionLifecycle.Init` boundary. Keep JSON setup machine-scoped and keep `camp init` independently available for additional camps. Reuse the existing Bubble Tea model and typed pipeline messages; do not put lifecycle effects in `internal/setupui`.

**Tech Stack:** Go, Cobra, Bubble Tea v2, Bubbles v2, Lip Gloss v2, Go `testing`.

## Global Constraints

- Human setup is one workflow from root/name collection through initialized camp readiness.
- Machine configuration stores only backend/provider/context defaults.
- Camp identity and source remain manifest-owned.
- `--devpod-context` is canonical; `--workspace-context` remains a hidden compatibility alias.
- JSON setup stays noninteractive and machine-scoped.
- Existing manifest, atomic-write, ownership, cancellation, and recovery guards remain authoritative.
- A skipped real-tool test is not lifecycle proof.

---

### Task 1: Setup request and CLI language

**Files:**
- Modify: `internal/cli/setup_prompt.go`
- Modify: `internal/cli/setup_prompt_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: `InitRequest`, `setupPromptDefaults`, Cobra flag binding.
- Produces: `promptSetupRequest(...) (InitRequest, error)` with `Root`, `Capsule`, `Backend`, `DevPodProvider`, and `DevPodContext`; canonical `--devpod-context`.

- [ ] **Step 1: Write failing prompt tests**

Add cases proving empty answers resolve the current directory and directory
basename, explicit answers survive, and the labels say `Camp root`, `Camp
name`, and `DevPod context`.

- [ ] **Step 2: Run the prompt tests and witness RED**

Run: `go test ./internal/cli -run 'TestPromptSetupRequest|TestSetupPrompt' -count=1`

Expected: FAIL because setup currently omits root and camp identity.

- [ ] **Step 3: Extend the setup request**

Add `Source` and `Capsule` defaults, read root/name before machine defaults,
canonicalize the root with `filepath.Abs`, and return all five `InitRequest`
fields. Reject empty values before persistence.

- [ ] **Step 4: Add the canonical context flag**

Bind `--devpod-context` to `InitRequest.DevPodContext`. Bind
`--workspace-context` to a separate compatibility value, hide it, reject using
both, and copy the compatibility value only when the canonical flag is absent.

- [ ] **Step 5: Run focused tests and commit**

Run: `go test ./internal/cli -run 'TestPromptSetupRequest|TestSetupPrompt|TestInitHelp|TestInit.*Context' -count=1`

Commit: `feat: clarify camp root and DevPod context`

### Task 2: Plain setup orchestrates initialization

**Files:**
- Modify: `internal/cli/production_setup.go`
- Modify: `internal/cli/production_setup_test.go`
- Modify: `internal/cli/production_test.go`

**Interfaces:**
- Consumes: complete `InitRequest`, `persistSetupDefaults`, `ProductionLifecycle.Init`.
- Produces: human `Setup` that runs tool preparation and camp initialization before rendering readiness; JSON remains unchanged.

- [ ] **Step 1: Write failing orchestration tests**

Prove a human setup request persists machine defaults without source/name,
creates `<root>/.camp/camp.yaml`, initializes capsule metadata, and prints a
`camp open` next action. Prove JSON setup does not initialize the current
directory.

- [ ] **Step 2: Run tests and witness RED**

Run: `go test ./internal/cli -run 'TestProductionSetup.*Init|TestSetup.*JSON' -count=1`

Expected: FAIL because setup currently stops after locked-tool preparation.

- [ ] **Step 3: Implement the single plain pipeline**

For human mode without an initialized camp at the selected root:

```go
request, err := promptSetupRequest(...)
if err != nil { return err }
if _, err := persistSetupDefaults(paths.ConfigPath, request); err != nil { return err }
if err := runProductionToolSetupWithEvents(...); err != nil { return err }
return p.Init(ctx, request, ModeHuman, out)
```

Resolve prompt defaults from existing machine configuration when present.
If the selected root already has the matching manifest, verify tools and render
that camp without rewriting it.

- [ ] **Step 4: Run focused tests and commit**

Run: `go test ./internal/cli -run 'TestProductionSetup|TestSetup' -count=1`

Commit: `feat: continue setup through camp initialization`

### Task 3: Rich setup uses the same unified lifecycle

**Files:**
- Modify: `internal/cli/setup_rich.go`
- Modify: `internal/cli/init_rich.go`
- Modify: `internal/cli/init_rich_test.go`
- Modify: `internal/setupui/model.go`
- Modify: `internal/setupui/model_test.go`
- Modify: `internal/setupui/form_test.go`

**Interfaces:**
- Consumes: `setupui.Pipeline`, complete setup form values, existing `ProductionLifecycle.Init`.
- Produces: one Bubble Tea setup pipeline that emits real tool, manifest, capsule, runtime, and storage facts before `AllReadyMsg`.

- [ ] **Step 1: Write failing rich-pipeline tests**

Prove setup fields include root/name, `Start` invokes initialization exactly
once with all resolved values, camp metadata replaces `no camp selected`, and
the ready command is `cd <root> && camp open`.

- [ ] **Step 2: Run tests and witness RED**

Run: `go test ./internal/cli ./internal/setupui -run 'TestRichSetup|TestWorkflowModel|TestConfigForm' -count=1`

Expected: FAIL because the rich setup pipeline ends at machine readiness.

- [ ] **Step 3: Implement typed unified milestones**

Use the existing setup model. Extend its workflow fields to root/name and have
`richSetupPipeline.run` persist defaults, verify tools, call the existing init
operation with activity reporting, emit authoritative manifest/capsule/runtime
metadata, and finish only after initialization succeeds.

- [ ] **Step 4: Run focused and race tests**

Run: `go test ./internal/cli ./internal/setupui -count=1`

Run: `go test -race ./internal/cli ./internal/setupui -count=1`

- [ ] **Step 5: Commit**

Commit: `feat: unify setup and init in the TUI`

### Task 4: Canonical guidance and complete verification

**Files:**
- Modify: `docs/skills/cli-composition.md`
- Modify: `docs/skills/managed-tools.md`
- Modify: `docs/skills/terminal-experience.md`
- Modify generated command documentation if `go run ./cmd/camp-docs` changes it.

**Interfaces:**
- Consumes: verified behavior and test output from Tasks 1–3.
- Produces: noncontradictory operational guidance describing unified human setup and machine-scoped JSON setup.

- [ ] **Step 1: Update the narrow canonical guides**

Remove claims that human setup never initializes a camp. Document the root
positional argument, DevPod-context meaning, compatibility alias, existing-camp
idempotency, and JSON boundary with exact tests as evidence.

- [ ] **Step 2: Regenerate checked-in CLI documentation**

Run: `go run ./cmd/camp-docs`

Review only changed generated files and keep deterministic output.

- [ ] **Step 3: Run complete verification**

Run:

```bash
go test ./... -count=1
go test -race ./internal/cli ./internal/setupui -count=1
go vet ./...
go build ./cmd/camp
git diff --check
```

- [ ] **Step 4: Commit**

Commit: `docs: document unified Camp setup`

