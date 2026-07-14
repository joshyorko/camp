# ADR 0004: Preserve root configuration and retain tagged registry content

## Status

Accepted on 2026-07-14.

## Decision

Camp discovers devcontainer configuration only at standard paths in the adopted root unless the user passes an explicit path. An existing invalid root config is an error. Nested project configs are never selected implicitly. When no root config exists, Camp writes a digest-locked Room of Requirement Wolfi overlay under `.camp/runtime/` and passes it explicitly to DevPod.

The inner tar.zst contains the complete root except `.camp/build/` and `.camp/runtime/`. Hauler's writable Distribution backend is separate from its OCI-layout store and has no supported arbitrary import command. Camp therefore catalogs every tagged registry reference, combines it with the workspace image inventory, re-pulls each known reference into a fresh Hauler store, verifies it, and saves that store.

## Consequences

Direct tagged pushes to the live Camp registry survive checkpoints. Untagged manifests, digest-only objects, and arbitrary OCI artifacts are not promised; documentation and status output state this boundary plainly.
