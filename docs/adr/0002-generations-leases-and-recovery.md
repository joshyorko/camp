# ADR 0002: Publish immutable generations with optimistic concurrency

## Status

Accepted on 2026-07-14.

## Decision

Every checkpoint uploads an immutable `<generation>-<sha256>.tar.zst` object. After upload, Camp verifies remote size and digest, then conditionally updates `latest.json` against the generation and digest opened by the session. A renewable writer lease prevents ordinary concurrent writers; branches use independent latest pointers.

Every lifecycle transition is atomically journaled under the XDG data directory. Publication success and teardown success are separate outcomes. Uploaded generations survive pointer conflicts, and `camp recover` can resume local, uploaded-but-unpublished, and published-but-not-cleaned sessions.

## Consequences

No local root, session store, or recoverable journal is removed before verified publication. Cleanup failure never disguises a successful checkpoint. Camp remains a lifecycle tool rather than a merge system or distributed database.
