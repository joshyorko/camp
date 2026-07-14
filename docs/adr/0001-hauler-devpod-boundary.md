# ADR 0001: Keep Camp as a lifecycle controller

## Status

Accepted on 2026-07-14.

## Decision

Camp is one Go CLI and does not embed or reimplement DevPod or Hauler. Typed adapters construct exact argument vectors and parse only documented machine-readable output. DevPod owns providers, SSH, workspace lifecycle, IDE transport, forwarding, and remote credentials. Hauler owns OCI-layout stores, haul save/load, file extraction, registry serving, and file serving.

The lifecycle controller owns adoption, durable transitions, checkpoint construction, publication, recovery, and safe cleanup. The complete adopted root is the unit of state.

## Consequences

Upgrades are explicit tool-lock changes with contract tests. Camp can provide stable lifecycle behavior while upstream tools change, but it must reject an incompatible binary rather than guess.
