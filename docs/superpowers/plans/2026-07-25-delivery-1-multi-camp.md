# Delivery 1 Multi-Camp Identity and Terminal Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make machine setup independent of camp identity, give every camp a safe versioned manifest, and route all lifecycle commands through deterministic camp/session selection with a truthful shared setup/init terminal workflow.

**Architecture:** Add a focused `internal/campconfig` package for manifest discovery, validation, atomic creation, and legacy singleton migration. Project the selected manifest into production runtime composition, and pass a single `Selection` value from Cobra through open/reopen/attach/sync/close/recover/status. Reuse the existing Bubble Tea event loop for setup and init by making its stage labels and form fields workflow-specific while keeping effects outside the model.

**Tech Stack:** Go 1.24, Cobra, Bubble Tea v2/Bubbles, `gopkg.in/yaml.v3`, Camp journals/backends, generated docs and transcript fixtures.

## Global Constraints

- Delivery 1 only: do not implement Devsy, release packaging, T3/Sites, or delivery-2 command subtrees.
- Do not touch user-owned `.claude/` or `test-camp-setup/`.
- Machine config contains defaults only; new camp manifests contain explicit resolved camp identity, source, backend, provider, and context.
- Manifest schema version is exactly `1`; the rich terminal minimum is exactly `69×20`.
- Preserve existing journal/backend schemas, safety locks, leases, ownership markers, immutable generations, pointer CAS, plain output, and one-envelope JSON output.
- Every production behavior is introduced test-first and each red failure is observed before implementation.

---

### Task 1: Versioned Camp Manifest

**Files:**
- Create: `internal/campconfig/manifest.go`
- Create: `internal/campconfig/manifest_test.go`

**Interfaces:**
- Produces: `Manifest`, `Workspace`, `Discover(path string)`, `Read(path string)`, and `Create(root string, manifest Manifest)`.
- Enforces: canonical directory roots, nearest-parent discovery, schema/version/ID validation, `source: .`, known fields, duplicate-key rejection, credential rejection, regular `.camp` directory and regular non-symlink manifest, atomic mode-0600 durable writes, and idempotent same-value creation.

- [ ] **Step 1: Write failing table tests**

```go
func TestCreateReadAndDiscoverManifest(t *testing.T) {
    root := t.TempDir()
    want := Manifest{SchemaVersion: 1, ID: "alpha", Source: ".", Backend: "file:///tmp/backend", Workspace: Workspace{Provider: "docker", Context: "default"}}
    if _, err := Create(root, want); err != nil { t.Fatal(err) }
    got, err := Discover(filepath.Join(root, "nested"))
    if err != nil || got.Manifest != want || got.Root != root { t.Fatalf("got %#v, %v", got, err) }
}
```

Add literal fixtures for duplicate keys, unknown fields, schema `2`, credentials in backend, `.camp` symlink, `camp.yaml` symlink, conflicting identity/settings, and idempotent identical creation.

- [ ] **Step 2: Verify red**

Run: `rtk go test ./internal/campconfig -count=1`
Expected: FAIL because `Manifest`, `Create`, and `Discover` do not exist.

- [ ] **Step 3: Implement the minimal safe manifest package**

Use `yaml.Node` to reject duplicate mapping keys before strict `KnownFields` decoding. Resolve `source` relative to the real manifest directory and require the canonical result to equal the canonical camp root. Create with a named temporary file in the real `.camp` directory, `chmod(0600)`, `Sync`, `Rename`, and parent-directory `Sync`.

- [ ] **Step 4: Verify green**

Run: `rtk go test ./internal/campconfig -count=1`
Expected: PASS.

### Task 2: Machine Defaults and Deterministic Singleton Migration

**Files:**
- Modify: `internal/config/store.go`
- Modify: `internal/config/store_test.go`
- Modify: `internal/config/bootstrap.go`
- Modify: `internal/config/bootstrap_test.go`
- Create: `internal/campconfig/migrate.go`
- Create: `internal/campconfig/migrate_test.go`

**Interfaces:**
- Produces: machine-only `config.Persistent`; legacy-only `config.LegacyIdentity`; `Migrate(configPath string, defaults config.Persistent)`.
- Migration writes `<source>/.camp/camp.yaml`, preserves `config.yaml.bak`, rewrites machine defaults without `defaultCapsule` or `source`, and returns an exact `camp init --migrate` command for noninteractive/JSON callers.

- [ ] **Step 1: Write failing config and migration tests**

Test new writes omit `defaultCapsule`/`source`; legacy reads expose them only through the migration interface. Test success, conflicting manifest, interruption before config replacement, retry/idempotency, and JSON/noninteractive recovery text with literal YAML fixtures.

- [ ] **Step 2: Verify red**

Run: `rtk go test ./internal/config ./internal/campconfig -count=1`
Expected: FAIL on the old persisted singleton fields and missing migration API.

- [ ] **Step 3: Implement machine defaults and migration**

Keep legacy fields readable but remove them from new machine-config writes. Validate the legacy source and capsule metadata, create the backup before mutation, create/validate the manifest, then atomically rewrite config. Never overwrite a conflicting manifest or backup.

- [ ] **Step 4: Verify green**

Run: `rtk go test ./internal/config ./internal/campconfig -count=1`
Expected: PASS.

### Task 3: Shared Selector, Real Reopen, and Status

**Files:**
- Modify: `internal/app/session.go`
- Modify: `internal/app/session_test.go`
- Create: `internal/app/status.go`
- Create: `internal/app/status_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`
- Modify: `internal/cli/production.go`
- Modify: `internal/cli/production_test.go`

**Interfaces:**
- Produces: CLI `Selection{Camp, Session string}` on open/reopen/attach/sync/close/recover/status.
- Selection order: explicit session, explicit camp, nearest manifest from canonical cwd/path, sole active session only for active-session commands, then typed actionable not-found/ambiguity error.
- `reopen` selects the newest closed generation for the camp and calls the existing open use case using the journal’s canonical source/backend/provider/context.
- `status` returns the selected snapshot and truthful recovery command.

- [ ] **Step 1: Write failing selector and Cobra contract tests**

Add table cases for every lifecycle command proving `--session` beats `--camp`, directory camp beats sole-session fallback, positional attach target remains independent, reopen positional means session only, and ambiguity next commands reference only shipped `camp status`, `camp list`, `camp init`, and selectors.

- [ ] **Step 2: Verify red**

Run: `rtk go test ./internal/app ./internal/cli -run 'Test.*(Select|Reopen|Status|LifecycleFlags)' -count=1`
Expected: FAIL because the shared request/flags and status command do not exist and reopen still aliases open.

- [ ] **Step 3: Implement shared selection and production routing**

Add `--camp` and `--session` consistently to lifecycle commands without overloading positionals. Discover manifests read-only before composing camp-specific backend/runtime state. Reopen from the newest eligible closed journal, preserving the stored canonical source and runtime facts.

- [ ] **Step 4: Verify green**

Run: `rtk go test ./internal/app ./internal/cli -count=1`
Expected: PASS.

### Task 4: `camp init`, Setup Defaults, and Legacy Recovery

**Files:**
- Modify: `internal/cli/production.go`
- Modify: `internal/cli/production_setup.go`
- Modify: `internal/cli/setup_prompt.go`
- Modify: `internal/cli/setup_rich.go`
- Modify: corresponding `*_test.go` files

**Interfaces:**
- `camp setup` writes/updates machine defaults and never selects a camp.
- `camp init [root] --name <id>` resolves omitted settings from defaults, creates `.camp/camp.yaml`, invokes capsule initialization with that ID, and prints `next: cd <root> && camp open`.
- `camp init --migrate` performs the deterministic legacy migration; JSON/noninteractive discovery returns the exact command instead of prompting.

- [ ] **Step 1: Write failing setup/init behavior tests**

Assert setup output/config contain no active source/capsule, init defaults come from machine config, repeat init is idempotent only for identical settings, conflicts preserve files, and plain/JSON outputs retain their established framing.

- [ ] **Step 2: Verify red**

Run: `rtk go test ./internal/cli -run 'Test.*(Setup|Init|Migrate)' -count=1`
Expected: FAIL because setup still collects singleton values and init persists them globally.

- [ ] **Step 3: Implement minimal setup/init workflow changes**

Replace `--capsule`/`--source` persistence with `--name` and manifest creation. Keep setup limited to backend/provider/context/ports/S3 defaults. Wire migration before camp resolution and use exact actionable errors for noninteractive/JSON.

- [ ] **Step 4: Verify green**

Run: `rtk go test ./internal/cli -count=1`
Expected: PASS.

### Task 5: Shared Rich Setup/Init Event Loop

**Files:**
- Modify: `internal/setupui/form.go`
- Modify: `internal/setupui/model.go`
- Modify: `internal/setupui/model_test.go`
- Modify: `internal/cli/setup_rich.go`
- Modify: `internal/cli/setup_rich_test.go`

**Interfaces:**
- Produces workflow-specific typed stages and real activity text while retaining one long-lived Bubble Tea model.
- Navigation: tab/shift-tab focus, enter validation, escape back/cancel, `?` overlay, ctrl-c cancellation, EOF handling, entered-value retention, failing-field focus.
- Rich gate and renderer guard both accept exactly `69×20`.

- [ ] **Step 1: Write failing model tests**

Drive real `tea.KeyPressMsg`, stage messages, EOF, and resize messages. Assert stage completion changes only after the matching effect event; `esc` returns to the previous editable step; `?` toggles help; `69×20` renders the scene; `68×20` and `69×19` render the guard.

- [ ] **Step 2: Verify red**

Run: `rtk go test ./internal/setupui ./internal/cli -run 'Test.*(Activity|Back|Help|Cancel|EOF|69x20|Rich)' -count=1`
Expected: FAIL on missing help/back/activity/EOF behavior.

- [ ] **Step 3: Implement the minimal shared model extensions**

Keep effects in CLI pipelines. Add typed activity/failure messages, workflow form descriptors, help overlay state, back navigation, EOF cancellation, and focused field errors. Do not advance milestones on timer ticks.

- [ ] **Step 4: Verify green and race**

Run: `rtk go test ./internal/setupui ./internal/cli -count=1`
Run: `rtk go test -race ./internal/setupui ./internal/cli -count=1`
Expected: PASS.

### Task 6: Generated Surfaces, Documentation, and Two-Camp Proof

**Files:**
- Modify: `internal/docsgen/*`
- Modify: `docs/generated/commands.md`
- Modify: `docs/generated/presentation.md`
- Modify: `docs/generated/transcripts.md`
- Modify: generated completion fixtures
- Modify: `docs/skills/session-selection-and-presentation.md`
- Modify: `docs/skills/terminal-experience.md`
- Modify: `docs/skills/local-lifecycle-recovery.md`
- Create or modify: `integration/*multi_camp*_test.go`

**Interfaces:**
- Generated surfaces document `status`, `--camp`, `--session`, machine-only setup, manifest init, reopen history, migration recovery, rich activity/help/back, and the 69×20 boundary.
- Isolated smoke proves two roots coexist, open independently, list together, and attach/sync/close resolve the intended session.

- [ ] **Step 1: Write failing docs-generation and isolated smoke tests**

Assert generated output is derived from the real Cobra tree and real transcript runner. Build two temporary camp roots and independent file backends with pinned fake tool adapters; assert journal capsule/source routing rather than fake call counts.

- [ ] **Step 2: Verify red**

Run: `rtk go test ./internal/docsgen ./integration -run 'Test.*(Generated|MultiCamp)' -count=1`
Expected: FAIL because the generated surfaces and smoke do not yet include delivery 1.

- [ ] **Step 3: Regenerate and update canonical operational guidance**

Run the repository’s docs generator found in `cmd/camp-docs`; update only claims backed by passing code/tests. Remove every statement describing global config as the active capsule source.

- [ ] **Step 4: Run full gates**

Run:

```text
rtk go test ./... -count=1
rtk go test -race ./... -count=1
rtk go vet ./...
rtk go build ./cmd/camp
rtk git diff --check
```

Record installed-tool/lifecycle skips separately; a skip is not proof.

- [ ] **Step 5: Commit**

Stage only delivery-1 files and commit with focused Conventional Commit messages. Finish with a clean branch and exact integration instructions.
