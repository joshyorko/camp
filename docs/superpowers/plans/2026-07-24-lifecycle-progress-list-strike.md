# Lifecycle Progress, List, and Strike Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stream truthful lifecycle progress, list stored camps, and safely strike local Camp state.

**Architecture:** Application-layer progress events are emitted after existing durable checkpoint and cleanup boundaries and rendered synchronously by the CLI. Stored-camp inventory is derived from validated latest-pointer objects plus journal history. Strike is a separate guarded application use case backed by a Linux filesystem controller that archives verified local state with same-filesystem renames.

**Tech Stack:** Go 1.26, Cobra, Camp journal and coordination repositories, Linux `openat`/`renameat` safety patterns, existing terminal presentation.

## Global Constraints

- Preserve JSON stdout as one stable schema envelope.
- Never emit fake percentages, scrape subprocess output, or report unfinished stages.
- Preserve configuration, source, and managed tools.
- Refuse active sessions, remote backends, symlinks, unresolved roots, and external file backends.
- `camp strike --purge` requires `--yes`.
- Do not touch `.claude/` or `test-camp-setup/`.
- Improve `docs/skills/terminal-experience.md` and `docs/skills/local-lifecycle-recovery.md` with verified behavior.

---

### Task 1: Typed lifecycle progress

**Files:**
- Create: `internal/app/progress.go`
- Modify: `internal/app/checkpoint.go`
- Modify: `internal/app/sync.go`
- Modify: `internal/app/close.go`
- Test: `internal/app/checkpoint_test.go`
- Test: `internal/app/sync_test.go`
- Test: `internal/app/close_test.go`

**Interfaces:**
- Produces: `type ProgressEvent struct { Stage ProgressStage; Message string; Generation uint64; ImageCount int; Bytes int64 }`
- Produces: `type ProgressReporter interface { Report(context.Context, ProgressEvent) error }`
- Produces: `type ProgressFunc func(context.Context, ProgressEvent) error`
- Changes: `checkpointPublisher.Publish(context.Context, ports.OperationToken, string, ProgressReporter) (CheckpointResult, error)`
- Changes: `Sync.Run(context.Context, string, ProgressReporter)` and `CloseRequest.Progress`.

- [ ] **Step 1: Write failing ordering tests**

Add reporters that append stages and assert checkpoint stages follow their completed real effects, cleanup stages follow each effect, nil reporters remain valid, and reporter errors stop before later effects.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/app -run 'TestCheckpointProgress|TestSyncProgress|TestCloseProgress' -count=1`

Expected: compile failure because the progress types and parameters do not exist.

- [ ] **Step 3: Implement the minimal reporter contract**

```go
type ProgressFunc func(context.Context, ProgressEvent) error

func (f ProgressFunc) Report(ctx context.Context, event ProgressEvent) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}

func reportProgress(ctx context.Context, reporter ProgressReporter, event ProgressEvent) error {
	if reporter == nil {
		return nil
	}
	return reporter.Report(ctx, event)
}
```

Emit only after the corresponding fact/effect completes. Keep resumed checkpoint logic from emitting skipped stages.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/app -run 'TestCheckpointProgress|TestSyncProgress|TestCloseProgress' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -m "feat: emit durable lifecycle progress"
```

### Task 2: Stream progress through the CLI

**Files:**
- Modify: `internal/cli/production.go`
- Modify: `internal/cli/terminal_linux.go`
- Modify: `internal/presentation/terminal.go`
- Create: `internal/presentation/progress.go`
- Create: `internal/presentation/progress_test.go`
- Test: `internal/cli/terminal_test.go`
- Test: `internal/cli/production_test.go`
- Test: `internal/presentation/terminal_test.go`

**Interfaces:**
- Consumes: `app.ProgressReporter` and `app.ProgressEvent`.
- Produces: `newLifecycleProgressReporter(OutputMode, io.Writer, string) app.ProgressReporter`.

- [ ] **Step 1: Write failing streaming tests**

Use a blocking fake publisher to prove the first human progress line is written before `Sync` or `Close` returns. Assert JSON output contains no streamed terminal lines and remains one decodable object.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/cli ./internal/presentation -run 'Test.*Progress.*Streams|Test.*Progress.*JSON' -count=1`

Expected: FAIL because production lifecycle methods provide no reporter.

- [ ] **Step 3: Implement synchronous rendering**

Adapt the MIT-licensed `basecamp/once` `internal/command/cli_progress.go` architecture: one long-lived Bubble Tea model receives typed stage messages through a bounded channel, renders the current stage with indeterminate progress when Camp has no honest percentage, and exits on completion or failure. Map application stages to presentation messages without forwarding raw subprocess logs. Plain human output remains append-only; rich TTY output updates in place. Final result output must not repeat already-streamed publication or cleanup messages.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/cli ./internal/presentation -run 'Test.*Progress.*Streams|Test.*Progress.*JSON' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli internal/presentation
git commit -m "feat: stream sync and close progress"
```

### Task 3: Stored-camp inventory

**Files:**
- Create: `internal/app/camps.go`
- Modify: `internal/coordination/pointers.go`
- Modify: `internal/adapters/filebackend/store.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/production.go`
- Test: `internal/app/camps_test.go`
- Test: `internal/coordination/pointers_test.go`
- Test: `internal/adapters/filebackend/store_test.go`
- Test: `internal/cli/root_test.go`
- Test: `internal/cli/production_test.go`

**Interfaces:**
- Produces: `PointerRepository.List(context.Context) ([]PointerRecord, error)`.
- Produces: `CampReadModel` with capsule, branch, generation, digest, state, session ID, backend, and updated time.
- Produces: `ProductionLifecycle.List(context.Context, OutputMode, io.Writer) error`.

- [ ] **Step 1: Write failing inventory tests**

Prove root-prefix object listing is read-only and safe, pointer listing validates every `latest.json`, journal and pointer rows merge once, doctor artifacts are excluded, and ordering is capsule then branch.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/adapters/filebackend ./internal/coordination ./internal/app ./internal/cli -run 'Test.*(RootList|PointerList|CampList)' -count=1`

Expected: compile failure because list APIs do not exist.

- [ ] **Step 3: Implement inventory**

Permit empty-prefix `List` without permitting empty keys for mutation. Page through all objects, accept only main or branch `latest.json` keys, decode through the pointer repository, and fail on malformed candidates. Merge the newest journal session for each capsule/lineage.

- [ ] **Step 4: Implement deterministic output**

Human columns: `CAMP`, `BRANCH`, `GENERATION`, `STATE`, `LAST SESSION`, `BACKEND`. JSON uses the normal versioned success envelope.

- [ ] **Step 5: Verify GREEN**

Run: `go test ./internal/adapters/filebackend ./internal/coordination ./internal/app ./internal/cli -run 'Test.*(RootList|PointerList|CampList)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/adapters/filebackend internal/coordination internal/app internal/cli
git commit -m "feat: list stored camps"
```

### Task 4: Safe `camp strike`

**Files:**
- Create: `internal/app/strike.go`
- Create: `internal/adapters/strike/controller_linux.go`
- Create: `internal/adapters/strike/controller_linux_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/production.go`
- Test: `internal/app/strike_test.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Produces: `StrikeRequest { Purge bool; Yes bool }`.
- Produces: `StrikeResult { ArchivedPath string; Camps []CampReadModel; Purged bool }`.
- Produces: `StrikeController.Archive(context.Context, StrikePlan) (string, error)` and `Purge(context.Context, StrikePlan) error`.
- Produces: `ProductionLifecycle.Strike(context.Context, StrikeRequest, OutputMode, io.Writer) error`.

- [ ] **Step 1: Write failing application safety tests**

Assert opening/open/recovering sessions reject before controller calls; purge without confirmation rejects; remote and external backends reject; closed-only archive receives the exact verified target set.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/app ./internal/cli -run 'Test.*Strike' -count=1`

Expected: compile failure because strike APIs do not exist.

- [ ] **Step 3: Write failing filesystem tests**

Prove archive uses sibling timestamped directories, preserves `tools`, rejects symlink targets and path substitution, writes a manifest, and purge removes only verified targets.

- [ ] **Step 4: Verify RED**

Run: `go test ./internal/adapters/strike -count=1`

Expected: compile failure because the controller does not exist.

- [ ] **Step 5: Implement guarded archive and purge**

Acquire an exclusive controller guard, revalidate target device/inode/type immediately before each `renameat` or recursive purge, fsync parents, and leave the archive plus manifest on partial failure. Recreate empty controller directories after successful archive.

- [ ] **Step 6: Wire Cobra**

```text
camp strike
camp strike --purge --yes
```

Print the `camp list` inventory before mutation and the archive path plus `next: camp open` afterward.

- [ ] **Step 7: Verify GREEN**

Run: `go test ./internal/adapters/strike ./internal/app ./internal/cli -run 'Test.*Strike' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/adapters/strike internal/app/strike* internal/cli
git commit -m "feat: add safe camp strike"
```

### Task 5: Documentation and production verification

**Files:**
- Modify: `docs/skills/terminal-experience.md`
- Modify: `docs/skills/local-lifecycle-recovery.md`

- [ ] **Step 1: Document verified contracts**

Record the typed progress stages, JSON non-streaming rule, list inventory source, strike refusal rules, archive layout, purge confirmation, and fresh-start command.

- [ ] **Step 2: Run full gates**

```bash
go test ./... -count=1
go vet ./...
go build ./cmd/camp
git diff --check
```

Expected: all commands exit zero.

- [ ] **Step 3: Run isolated real smoke**

Use a fresh XDG root and file backend. Prove `camp list`, streamed `camp close`, `camp strike`, preserved source/tools/config, and a fresh `camp open`. Do not mutate the user's current configured source.

- [ ] **Step 4: Commit and push**

```bash
git add docs/skills
git commit -m "docs: document progress and strike recovery"
git push origin master
```

- [ ] **Step 5: Rebuild installed CLI**

Build `/var/home/kdlocpanda/.local/bin/camp` with the pushed commit, UTC build date, and `dirty=false`, then verify `camp --version`.

### Task 6: ONCE-style animated stars

**Files:**
- Modify: `internal/setupui/starfield.go`
- Modify: `internal/setupui/model.go`
- Modify: `internal/setupui/scene.go`
- Test: `internal/setupui/starfield_test.go`
- Test: `internal/setupui/model_test.go`
- Test: `internal/setupui/scene_test.go`
- Modify: `docs/skills/terminal-experience.md`

**Interfaces:**
- Produces: a model-owned `*Starfield` with `Init() tea.Cmd`, `Update(tea.Msg) tea.Cmd`, `Resize(width, height int)`, `ComputeGrid()`, and `Paint(*Canvas, skyRows, Palette)`.
- Produces: an internal `starfieldTickMsg` scheduled every 33 milliseconds.
- Changes: scene composition consumes the model-owned starfield rather than constructing a static field per frame.

- [ ] **Step 1: Write failing parity tests**

Assert the ONCE constants (`100`, `0.03`, `0.1`, `3.0`, `33ms`), 2x4 Braille subcell projection, near-star brightness, tick depth movement and recycling, resize grid replacement, and deterministic fixed-seed capture.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/setupui -run 'TestStarfield.*(Projection|Tick|Resize|Deterministic|ONCE)' -count=1`

Expected: FAIL because Camp's current starfield is static and hash-scattered.

- [ ] **Step 3: Adapt ONCE's starfield**

Port the MIT-licensed `basecamp/once/internal/ui/starfield.go` state machine, retain Camp palette colors, inject a deterministic PCG seed from `NewModel`, and clip painting to `skyRows`. Start the tick command only from the rich setup model.

- [ ] **Step 4: Wire the long-lived model**

Combine form and starfield init commands, forward window sizes and tick messages, and pass the owned field to scene composition. Static scene-dump tooling advances to a fixed documented frame before rendering.

- [ ] **Step 5: Verify GREEN**

Run: `go test ./internal/setupui -count=1`

Expected: PASS, including existing width, height, layering, and capture invariants.

- [ ] **Step 6: Commit**

```bash
git add internal/setupui docs/skills/terminal-experience.md
git commit -m "feat: animate the Camp starfield"
```
