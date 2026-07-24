# Lifecycle Progress and Strike Design

## Outcome

Camp will expose real progress while `sync` and `close` perform checkpoint and cleanup work, provide `camp list` as the authoritative stored-camp inventory, and provide `camp strike` as the safe, Camp-native way to return the local controller to a fresh-start state.

## Selected approach

Lifecycle progress will come from typed application events emitted at existing durable boundaries. The CLI will subscribe before invoking the use case and render each event immediately in human mode. It will not invent percentages, poll arbitrary timers, scrape subprocess logs, or report a stage before its authoritative effect or journal fact has completed. JSON mode will remain one stable result document without terminal output interleaved on stdout.

This is preferred over tailing `journal.jsonl`, which introduces polling and races, and over forwarding raw DevPod or Hauler output, which exposes unstable implementation details and can leak unsafe content.

## Progress contract

The checkpoint path will report concise stages for:

1. workspace snapshot preparation;
2. OCI image inventory and capture;
3. immutable registry seal;
4. capsule archive and Hauler generation assembly;
5. local generation verification;
6. generation upload and remote verification;
7. pointer publication;
8. serving refresh.

Close will additionally report each cleanup boundary: workspace, forwarders, services, supervisor, lease, and owned materialization. The final generation and completion messages remain result-derived. Recovery and resumed operations emit only stages actually reconciled during that invocation.

Progress delivery is best-effort presentation. A terminal write failure cancels the operation through its context and returns an error; a slow renderer must not create concurrent lifecycle mutation. Event messages contain bounded, sanitized metadata such as generation number, image count, and archive size, never secrets or raw subprocess output.

## `camp strike`

`camp strike` means “strike camp and return to the trailhead.” It:

- refuses to run while any journal has an opening, open, or recovering session;
- acquires a controller-wide strike lock before rechecking session state;
- preserves Camp configuration, the configured source directory, and managed DevPod/Hauler tools;
- moves controller-owned sessions, mirrors, materializations, locks, supervisors, quarantine, and the configured default file backend into a timestamped sibling archive;
- uses same-filesystem renames and reports the exact archive path;
- recreates the empty controller directories required for the next invocation;
- leaves the next command as `camp open`, which adopts the configured source because no committed pointer remains.

The default operation is recoverable. `camp strike --purge --yes` permanently removes the same verified targets instead of archiving them. `--purge` without `--yes` fails before mutation. Camp never follows symlinks, deletes an unresolved path, deletes its configuration or source, or purges a remote/S3 backend. A configured file backend outside Camp's controller data root causes strike to fail with explicit guidance rather than partially resetting durable state.

## `camp list`

`camp list` is the read-only inventory of Camp capsules known to the controller and configured backend. Human output is a compact table; `camp list --json` emits the stable schema envelope. Each row contains capsule, lineage, latest committed generation and digest, session state, last session ID, backend, and updated time. Results are sorted by capsule and lineage.

The command composes journal history with committed backend pointers and generation metadata rather than inferring camps from directory names. Internal doctor probes and incomplete coordination objects are excluded unless they correspond to a valid capsule pointer. Backend read or validation failures remain explicit; the command does not silently return a partial inventory.

## Recovery

The initial implementation records an archive manifest containing schema version, creation time, original paths, archived paths, and the effective configuration identity. It does not add a restore command yet: restoring controller state safely requires its own collision, active-session, and backend-consistency contract. Until that contract exists, recovery is an explicit operator action documented against the manifest. The normal fresh-start path is:

```text
camp strike
camp open
```

## Verification

Tests will prove:

- progress events are ordered by real checkpoint and cleanup effects;
- no progress is emitted for stages skipped during recovery;
- human output streams before completion while JSON remains valid;
- strike refuses active sessions and unsafe or external paths without mutation;
- recoverable strike preserves tools, configuration, and source while archiving every owned state target;
- purge requires `--yes` and removes only the verified target set;
- list returns every valid committed or journaled capsule once, excludes probe artifacts, and has deterministic human and JSON output;
- interruption cannot make Camp delete a replacement path;
- a post-strike `camp open` starts from the configured source with no committed generation.

The final gate is the full Go test suite, `go vet ./...`, production build, `git diff --check`, and a real local file-backend smoke run showing streamed close progress followed by strike and a fresh open.
