# Session selection and presentation

Use one selector contract for `open`, `reopen`, `attach`, `sync`, `close`, `recover`, and `status`. Apply inputs in this order and stop at the first applicable tier:

1. explicit `--session <id>`;
2. explicit `--camp <id>`;
3. the nearest validated `.camp/camp.yaml` at or above the canonical current directory (or `open` path);
4. the sole unambiguous active session, only for commands that operate on an active session;
5. an actionable no-camp or ambiguity error.

There is no configured or hidden global active capsule. Positional arguments keep command-specific meaning: `open [path]` discovers a camp from that path, `reopen [session]` and `recover [session]` name sessions, and `attach [target]` names only an in-workspace landing target. Active mutation accepts only `opening`, `open`, and `recovering` sessions. Reopen history accepts closed records and deterministically chooses the most recently updated closed session when no session is explicit. Sort ambiguous session IDs before generating candidate commands; not-found and ambiguity errors must reference only shipped commands such as `camp list`, `camp status --session …`, `camp open --camp …`, and `camp init`.

An explicit `open --camp <id>` may use journals/pointers to identify the camp, but it must prove a current local source before opening: the durable source must resolve to the exact root of a regular non-symlink `.camp/camp.yaml`, and that manifest's ID, backend, provider, and context must match the durable session facts. Missing or stale host paths never invalidate `list` or explicit session history/status, but opening by camp name fails with the exact missing source and `camp init <source> --name <id>` recovery instead of guessing.

Build public output from application read models, never by marshaling journal snapshots. Treat service liveness as `unknown` until an inspector reconciles both helper and child process identities. Distinguish `live`, `stopped`, `dead`, and `pid-reused`; a persisted observed state alone is not liveness evidence.

The setup campsite uses the latest matching session's non-empty provider and context as durable display facts. Derive its local or remote DevPod label only after those overrides: `docker`, `podman`, an empty provider, or explicit `LocalProvider` evidence is local; other providers are remote.

Keep publication, cleanup, and recovery independent. A published generation remains `published` when cleanup later fails, and that combination produces `cleanup-only` recovery. Uploaded or verified generations without pointer commitment are `orphaned`; record a proven compare-and-swap loss as `pointer-conflict`.

The application-level `OperationalQueries` type is the reusable read-only seam for `list`, `status`, and `history` composition. `List` observes every journal snapshot before building session read models; `Status` uses history-purpose selection so an explicitly selected closed session remains inspectable. If process observation fails, return the error instead of publishing persisted service state as current evidence. A nil observer is allowed for callers that intentionally accept `unknown` service liveness.

`HistoryFor` first selects a session, then queries generation history with that session's capsule and lineage. It returns `GenerationReadModel` values rather than storage metadata and sorts them by generation descending, creation time descending, and digest ascending. This keeps command output deterministic even when an adapter does not preserve ordering.

Recovery uses `SelectionRecovery`, but selection alone never authorizes effects. `Recover.Run` first observes the selected record, reloads it from the journal, rejects changes to capsule, lineage, mode, materialization root, workspace identity, or lease revision, and dispatches lifecycle versus cleanup reconciliation only after `RecoverySafetyGuard` revalidates ownership, pending-transition identity, and the active writer lease. It observes again immediately before the reconciler and fails closed if evidence changed; after reconciliation it observes once more to build the returned session read model. Cleanup failure takes precedence over lifecycle state so a published checkpoint is not repeated while cleanup-only recovery runs.

JSON output uses a top-level `schemaVersion`, stable error codes, deterministic arrays, redacted strings, no ANSI, and stdout even for failures. Human successes use stdout and human failures use stderr. Update the presentation goldens whenever this contract intentionally changes.

Verification:

```text
rtk go test ./internal/app ./internal/presentation -count=1
rtk go test -race ./internal/app ./internal/presentation
```
