# ADR 0007: Separate controller, blueprint, and profile authority

## Status

Partially implemented on 2026-07-27. Domain validation, durable profile
storage, and the journal execution-binding contract exist; CLI composition and
lifecycle enforcement remain deferred.

## Decision

The implemented domain slice gives `ControllerIdentity`, `CampBlueprint`,
`BlueprintRef`, `ExecutionBinding`, and `ExecutionProvenance` independent
schema versions and strict JSON decoders. A validated blueprint's canonical
JSON digest is its portable identity. Its closed schema contains only
controller identity, portable capsule/lineage identifiers, the supported
workspace-engine identity, and the existing typed DevPod/Hauler versions.
Controller and tool versions use strict v-prefixed SemVer 2.0.0: numeric core
identifiers cannot contain leading zeroes, prerelease identifiers cannot be
empty or use leading zeroes when numeric, and build identifiers must be
non-empty dot-separated values. Validation rejects unsupported schema versions,
unknown JSON fields (including standalone blueprint-reference inputs),
non-portable identifiers, and non-canonical SHA-256 references before an
identity is computed.

An execution binding can reference a validated blueprint digest and an optional
validated profile digest. The current profile schema allows only the explicit
`workspaceEngine` field and only the supported `devpod` value; arbitrary value
maps are not accepted. Application reads validate values returned by the store,
and the current value-only schema is copied at import/list/show boundaries. The
profile adapter persists one closed, versioned JSON document behind an adjacent
exclusive lock. It validates every profile and the active digest on read and
before publication, writes a mode-0600 temporary file, fsyncs it, renames it
over the destination, and fsyncs the parent directory. Imports are
digest-idempotent, listing is deterministic, and activation cannot select a
missing profile.

Journal snapshots may contain one optional execution binding. Binding is
allowed only while the session journal is empty, is idempotent for an exact
match, and rejects a different blueprint or profile digest. The application
`ExecutionGuard` persists that binding before invoking its supplied effects and
provides the exact-match check lifecycle adapters need before attach, sync,
close, or recover. Production lifecycle composition does not call this seam
yet.

The application timeline projection reports absent, zero, malformed, or
unsupported bindings as `unknown-blueprint`; it marks only validated bindings
as known. Legacy snapshots decode without the optional field and remain
unknown. It does not infer a compatible portable blueprint from legacy journal
state. No production timeline reader is composed yet.

## Consequences

Portable artifacts can identify their controller and blueprint without leaking
machine-specific runtime facts. Profile storage and journal binding each have
one durability owner. CLI and production composition remain deferred because
the lifecycle does not yet derive the selected blueprint/profile binding or
enforce it at every entry point; exposing commands before that wiring would
falsely imply complete session freeze semantics.
