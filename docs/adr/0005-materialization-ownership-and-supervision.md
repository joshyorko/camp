# ADR 0005: Separate adoption, ownership, and persistent supervision

![Three camp stages distinguish safe adoption, exclusive ownership, and persistent supervision](../assets/adr-0005-ownership-supervision.png)

## Status

Accepted on 2026-07-14.

## Decision

Adopting a capsule does not grant permission to delete its root. Every session records a materialization identity: canonical path, original path, ownership marker, creation/adoption mode, expected device/inode identity where available, and whether cleanup is permitted. Only a Camp-created XDG work root with a matching marker can be removed automatically. An explicitly adopted user directory remains user-owned.

A hidden subcommand in the same `camp` binary runs the persistent session supervisor. It owns remote lease heartbeats, Hauler service units, DevPod reverse tunnels, readiness probes, logs, PID start identities, and reconciliation metadata. Every Hauler service unit contains the `pasta` helper plus its exact Hauler child and uses durable request/ack records, launch tokens, private pid/log paths, orphan discovery, and child-first cleanup as defined by ADR 0006. A separate local operation lock serializes `sync`, `close`, and recovery. The supervisor never watches or synchronizes workspace files.

## Consequences

An IDE or terminal can detach while the writer lease and services remain healthy. PID reuse and crashed commands are reconciled from identity and durable state. Verified publication alone can never authorize deletion of an arbitrary adopted directory.
