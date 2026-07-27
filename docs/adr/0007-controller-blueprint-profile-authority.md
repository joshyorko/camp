# ADR 0007: Separate controller, blueprint, and profile authority

## Status

Partially implemented on 2026-07-27. The domain and application validation
slice exists; durable profile storage, journal binding writes, CLI composition,
and lifecycle enforcement remain deferred.

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
maps are not accepted. Application reads validate values returned by a future
store, and the current value-only schema is copied at import/list/show
boundaries. There is no durable store in this slice. A future journal writer
must persist the selected binding before open effects and reject a different
profile digest for attach, sync, close, or recover.

The application timeline projection reports absent, zero, malformed, or
unsupported bindings as `unknown-blueprint`; it marks only validated bindings
as known. It does not infer a compatible portable blueprint from legacy journal
state. No production binding reader is composed yet.

## Consequences

Portable artifacts can identify their controller and blueprint without leaking
machine-specific runtime facts. CLI and production composition remain deferred
until profile storage and journal binding persistence have one authoritative
owner; exposing commands before that wiring would falsely imply durable
activation and session freeze semantics.
