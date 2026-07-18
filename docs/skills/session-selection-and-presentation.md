# Session selection and presentation

Use one selector contract for commands that mutate an active Camp session. Apply inputs in this order and stop at the first applicable tier:

1. explicit session ID;
2. explicit capsule and/or branch;
3. current directory's canonical materialization root;
4. configured capsule and/or branch defaults;
5. all eligible sessions.

Active mutation accepts only `opening`, `open`, and `recovering` sessions. History and recovery use separate selection purposes so closed records remain addressable without making them eligible for mutation. Sort ambiguous session IDs before generating candidate commands; not-found and ambiguity errors must include exact next commands.

Build public output from application read models, never by marshaling journal snapshots. Treat service liveness as `unknown` until an inspector reconciles both helper and child process identities. Distinguish `live`, `stopped`, `dead`, and `pid-reused`; a persisted observed state alone is not liveness evidence.

Keep publication, cleanup, and recovery independent. A published generation remains `published` when cleanup later fails, and that combination produces `cleanup-only` recovery. Uploaded or verified generations without pointer commitment are `orphaned`; record a proven compare-and-swap loss as `pointer-conflict`.

The application-level `OperationalQueries` type is the reusable read-only seam for `list`, `status`, and `history` composition. `List` observes every journal snapshot before building session read models; `Status` uses history-purpose selection so an explicitly selected closed session remains inspectable. If process observation fails, return the error instead of publishing persisted service state as current evidence. A nil observer is allowed for callers that intentionally accept `unknown` service liveness.

`HistoryFor` first selects a session, then queries generation history with that session's capsule and lineage. It returns `GenerationReadModel` values rather than storage metadata and sorts them by generation descending, creation time descending, and digest ascending. This keeps command output deterministic even when an adapter does not preserve ordering.

JSON output uses a top-level `schemaVersion`, stable error codes, deterministic arrays, redacted strings, no ANSI, and stdout even for failures. Human successes use stdout and human failures use stderr. Update the presentation goldens whenever this contract intentionally changes.

Verification:

```text
rtk go test ./internal/app ./internal/presentation -count=1
rtk go test -race ./internal/app ./internal/presentation
```
