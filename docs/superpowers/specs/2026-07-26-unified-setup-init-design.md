# Unified Setup and Init Design

## Outcome

`camp setup` is the obvious first-run command. On a machine with no Camp
configuration and no camp at the selected root, it collects the camp root and
name, machine defaults, prepares the locked tools, and initializes that camp in
one continuous human workflow.

`camp init [root]` remains the direct command for creating additional camps and
for scripts. Machine defaults remain independent from every camp manifest.

## Interaction

The first-run human form asks for:

1. camp root, defaulting to the current directory;
2. camp name, defaulting to the root directory name;
3. backend, defaulting to Camp's XDG file backend;
4. workspace provider, defaulting to `docker`;
5. DevPod context, defaulting to `default`.

The form labels the last value **DevPod context** and describes it as an
advanced named DevPod configuration context. It must not imply that this value
is a project directory.

After confirmation, one setup pipeline:

1. validates the complete request without filesystem mutation;
2. persists only backend/provider/context machine defaults;
3. installs or verifies the locked DevPod and Hauler tools;
4. creates the camp manifest at the selected root;
5. initializes the capsule;
6. finishes with the initialized camp and `camp open` as the next action.

The rich TUI remains one Bubble Tea program across form, tool setup, manifest
creation, capsule initialization, ready, failure, and cancellation. Plain human
mode asks the same questions and reports the same proven milestones without
terminal controls.

## Existing-machine behavior

Running `camp setup` with machine defaults but no camp at the current directory
must not claim that configuration is unpersisted. In an interactive terminal it
offers the camp-specific root and name and continues into initialization.

Running `camp setup` from an initialized camp verifies the locked tools and
renders that camp's ready state without rewriting its manifest. Additional
camps use `camp init [root]` or run `camp setup` from their intended root.

JSON setup remains noninteractive and machine-scoped. It prepares tools and
returns its existing stable setup result; it never guesses a root or camp name.

## CLI language and compatibility

The canonical flag is `--devpod-context`. The old `--workspace-context` spelling
remains as a hidden compatibility alias for one release cycle. Help and errors
say **DevPod context**, and examples show the root as the positional argument:

```text
camp init ~/test-camp-robot --name test_camp
```

Provider and context remain explicit in each camp manifest after defaults are
resolved, so later machine-default changes cannot retarget an existing camp.

## Failure and recovery

Validation completes before any write. After machine defaults are committed,
manifest or capsule initialization failures report the exact selected root and
a copyable `camp init <root> --name <name>` recovery command. Existing manifest,
ownership, and idempotency guards remain authoritative; setup does not replace
or delete Camp state.

Cancellation before submission writes nothing. Cancellation after submission
waits for the active operation to stop and restores the terminal.

## Verification

Tests cover:

- default and explicit first-run root/name values;
- one setup dispatch continuing through initialization;
- existing machine config with no camp still offering initialization;
- initialized-camp setup remaining idempotent;
- `--devpod-context` and the hidden compatibility alias;
- help text that distinguishes root from DevPod context;
- plain, rich, JSON, cancellation, failure, and terminal restoration paths;
- no manifest creation when complete-request validation fails;
- focused package tests, full Go tests, race tests for touched concurrent UI
  packages, vet, build, and whitespace checks.

Real-tool acceptance must initialize a disposable project root and open it with
the selected provider/context. Skipped real-tool tests are not lifecycle proof.

