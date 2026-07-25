# Multi-Camp Identity and Terminal Workflow Design

## Outcome

Make Camp feel obvious with more than one camp:

- `camp setup` prepares the machine once.
- `camp init` creates an independently addressable camp.
- Bare lifecycle commands select the camp containing the current directory.
- Explicit `--camp` and `--session` selectors work from anywhere.
- `reopen` reopens history instead of aliasing `open`.
- The rich setup/init experience remains responsive, truthful, and usable at every supported terminal size.

This is the first post-#54 correction batch. It does not implement Devsy, release packaging, T3/Sites, or the remaining real crash-cut evidence.

## Product model

### Machine configuration

`$XDG_CONFIG_HOME/camp/config.yaml` contains only machine-wide defaults:

- default backend;
- default workspace runtime provider and context;
- default registry and fileserver ports;
- non-secret S3 compatibility settings.

It does not contain an active source or active capsule. Running `camp setup` may create or update these defaults and install or verify managed tools, but it never selects a camp.

### Camp manifest

Each camp root owns `.camp/camp.yaml`. The manifest is stable capsule content and therefore travels through checkpoints; only `.camp/build` and `.camp/runtime` remain excluded.

Schema version 1 contains:

```yaml
schemaVersion: 1
id: second-brain
source: .
backend: file:///var/home/example/.local/share/camp/backend
workspace:
  provider: room-of-requirement
  context: default
```

Rules:

- `id` is the stable camp identity and uses the existing capsule identifier validation.
- `source` is `.` in newly created manifests. Camp resolves it relative to the manifest directory and persists the canonical root in journals.
- Backend and workspace settings are explicit in the manifest after initialization so later changes to machine defaults do not silently change an existing camp.
- The manifest is a regular file below a real `.camp` directory. Final-component symlinks, path escape, duplicate YAML keys, unknown fields, credentials, and unsupported schema versions fail closed.
- Secrets never enter the manifest.

### Registry

Camp does not require a second mutable global registry to find directory-bound camps. `camp list` continues to derive durable camps from validated backend pointers and journals. It may add a `SOURCE` column when a current local manifest can be proven, but a stale host path never invalidates durable backend history.

`--camp <id>` resolves against validated pointers and journals. If opening by name requires a local source and no current manifest/source can be proven, Camp reports the exact missing source instead of guessing.

## Selection contract

Every lifecycle command uses one Camp-owned selector:

```text
explicit --session
→ explicit --camp
→ nearest .camp/camp.yaml from canonical cwd
→ sole unambiguous active session, for active-session commands only
→ actionable ambiguity/no-camp error
```

There is no hidden global active capsule.

Command behavior:

- `camp open [path]`: path selects the nearest manifest at or above that path. With no path, use the current directory. If no manifest exists, tell the user to run `camp init`.
- `camp reopen [session]`: explicit session wins; otherwise select the current camp’s newest closed generation. It never treats the argument as a path or landing target.
- `camp attach [target]`: camp/session selection is independent of the optional in-workspace landing target.
- `camp sync`, `camp close`: select the current camp or explicit session. Ambiguity is an error before effects.
- `camp recover [session]`: retains explicit session recovery and may use current-camp selection only when unambiguous.
- `camp status`: reports the selected camp/session and makes existing recovery guidance truthful.
- `camp list`: remains the cross-camp inventory.
- `camp strike`: remains controller-wide reset, not a per-camp deletion command.

Selectors are expressed consistently as `--camp <id>` and `--session <id>`. Positional arguments retain command-specific meaning and are never overloaded between camp identity, session identity, host path, and workspace target.

## Initialization and migration

### New initialization

`camp init [root] --name <id>`:

1. Canonicalizes and validates the root.
2. Refuses to replace an existing manifest unless it is the same identity and settings.
3. Resolves omitted backend/provider/context from machine defaults.
4. Writes `.camp/camp.yaml` atomically with private, durable permissions.
5. Runs the existing capsule initializer using the manifest identity.
6. Prints `next: cd <root> && camp open`.

Inside an uninitialized directory, interactive `camp init` asks only for the missing camp-specific values.

### Singleton migration

When the old global config contains `defaultCapsule` and `source` but the source has no `.camp/camp.yaml`, the first camp-resolving command offers or performs one deterministic migration:

1. Validate the canonical source and existing capsule metadata.
2. Write a versioned manifest using the old capsule, backend, provider, and context.
3. Rewrite global config to machine defaults without source/default capsule.
4. Preserve the old config as an atomic sibling backup until the new config and manifest are durable.

Migration is idempotent. Conflicting existing manifests fail without rewriting either file. JSON and noninteractive modes never prompt; they return an exact `camp init --migrate` recovery command.

## Terminal workflow refresh

Camp keeps its campsite art direction. ONCE is the interaction-architecture reference, not a visual template.

### Shared event loop

Setup and init use one long-lived Bubble Tea model with typed messages:

- machine setup stages;
- camp initialization stages;
- real in-flight activity;
- completion;
- recoverable failure;
- cancellation and EOF.

The model never performs lifecycle work itself.

### Truthful activity

Long-running steps display an indeterminate activity indicator plus the current real stage, such as:

```text
Installing Hauler…
Checking workspace runtime…
Writing camp manifest…
```

Animation proves only that the UI event loop is alive. Milestones advance only after their corresponding effect completes. Plain output remains append-only, and JSON remains one final envelope.

### Navigation and help

- `tab` and `shift+tab` move between fields.
- `enter` validates and continues.
- `esc` returns to the previous editable step; from the first step it cancels.
- `?` toggles a compact help overlay.
- `ctrl+c` cancels from every state and restores the terminal.
- Errors retain entered values and focus the failing field when applicable.

Mouse support is not required for this batch.

### Responsive contract

The rich-mode selection threshold and renderer guard use the same `69×20` minimum. Widths 69–79 no longer fall back to plain output when every other rich-mode capability is satisfied. Resize, truecolor, 256-color/plain fallback, `NO_COLOR`, CI, non-TTY, JSON, cancellation, and terminal restoration remain covered.

## Compatibility and safety

- Existing journal and backend schemas remain readable.
- New journals continue to persist the canonical source, resolved backend identity, camp ID, provider, and context needed for fresh-controller recovery.
- No selection change weakens operation locks, writer leases, ownership markers, process identity, immutable generations, or pointer compare-and-swap.
- Directory discovery is read-only and stops at the filesystem root.
- A camp manifest cannot override credential-bearing environment values into durable state.
- Existing scripts using `reopen` as an alias for `open` intentionally break; the old behavior is incorrect.

## Verification

Required automated evidence:

- manifest parsing, atomic creation, symlink/path/credential rejection, and schema migration;
- legacy singleton migration success, conflict, interruption, idempotency, and JSON recovery;
- selector precedence for every public lifecycle command;
- real `reopen` session/generation selection;
- ambiguity errors referencing only shipped commands;
- generated help, completions, and transcripts for `status`, selectors, setup, and init;
- setup/init model activity, back/help, cancellation, EOF, resize, and `69×20` rich-mode coverage;
- plain and JSON byte-contract preservation;
- full `go test ./... -count=1`, race tests for touched concurrent UI/application packages, `go vet ./...`, production build, and `git diff --check`;
- isolated two-camp smoke proving two roots coexist, open independently, list together, and route attach/sync/close to the intended sessions.

Real DevPod/Hauler lifecycle evidence remains required before claiming the multi-camp runtime proven outside isolated/configuration tests.

## Documentation

Update:

- `docs/skills/session-selection-and-presentation.md` with the selector precedence and command contracts;
- `docs/skills/terminal-experience.md` with setup/init activity, navigation, and the unified `69×20` boundary;
- `docs/skills/local-lifecycle-recovery.md` with singleton migration and multi-camp recovery behavior;
- generated command help, completions, and transcripts.

Remove every statement that describes the global config as the active capsule source.
