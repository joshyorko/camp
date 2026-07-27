# ADR 0007: Separate controller, blueprint, and profile authority

## Status

Accepted on 2026-07-27.

## Decision

`ControllerIdentity`, `CampBlueprint`, `BlueprintRef`, `ExecutionBinding`, and
`ExecutionProvenance` have independent schema versions. A blueprint is a
canonical JSON document whose digest is its portable identity. It contains only
controller identity, capsule/lineage, workspace-engine identity, and pinned
tool versions. Credentials, provider secrets, host paths, allocated ports,
timestamps, and session IDs are deliberately outside that document.

An execution binding references a blueprint digest and, when selected, a
profile digest. Profiles are canonical, non-secret value documents: import
copies their values before persistence and activation selects an already stored
digest. The application does not offer profile mutation. A future journal
writer must persist the selected binding before open effects and must reject a
different profile digest for attach, sync, close, or recover.

Timeline is a journal projection, not a compatibility translator. A historical
session without a recorded binding is reported as `unknown-blueprint`; Camp
does not infer a compatible portable blueprint from legacy journal state.

## Consequences

Portable artifacts can identify their controller and blueprint without leaking
machine-specific runtime facts. CLI and production composition remain deferred
until profile storage and journal binding persistence have one authoritative
owner; exposing commands before that wiring would falsely imply durable
activation and session freeze semantics.
