# Crash-cut evidence

Issue #3 crash evidence uses the machine-readable schema at
`docs/evidence/crash-cut-ledger.schema.json` and the current ledger at
`docs/evidence/issue-3-crash-cut-ledger.json`. Each cut records its durable
precondition, injected death, recovery action, resulting journal state,
publication/cleanup result, and evidence artifacts. A cut is runtime-proven
only when its real gate runs against the exact `CAMP_TEST_BINARY`; unit tests
and skipped capability gates do not upgrade its status.

The current ledger intentionally marks the complete process-death matrix
blocked. The existing `integration/forwarder_crash_test.go` proves only the
forwarder-start-before-fact cut. Required real capabilities must fail the gate
explicitly; missing `hauler`, `pasta`, `rsync`, or PTY tooling is not a pass.
