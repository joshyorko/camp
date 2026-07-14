# ADR 0002: Publish immutable generations with optimistic concurrency

## Status

Accepted on 2026-07-14.

## Decision

Every checkpoint uploads an immutable `<generation>-<sha256>.tar.zst` object. Camp first proves the saved haul is loadable locally, then verifies remote size and SHA-256 by trustworthy object metadata or streamed readback. It conditionally updates `latest.json` using the opaque backend revision and generation/digest opened by the current checkpoint baseline.

Writer leases are lineage-scoped: main uses `<capsule>/leases/writer.json`, while a branch uses `<capsule>/branches/<branch>/leases/writer.json`. Branch creation is conditional and records its source generation as parent. After each successful `sync`, the journal advances the current baseline and expected pointer revision while retaining the original opened generation for audit.

Every lifecycle transition is atomically journaled under the XDG data directory. Publication success and teardown success are separate outcomes. Uploaded generations survive pointer conflicts, and `camp recover` can resume local, uploaded-but-unpublished, and published-but-not-cleaned sessions.

## Consequences

No Camp-owned local root, session store, or recoverable journal is removed before verified publication. Cleanup failure never disguises a successful checkpoint. Ambiguous upload, pointer, DevPod, and service outcomes enter reconciliation states instead of being retried blindly. Camp remains a lifecycle tool rather than a merge system or distributed database.
