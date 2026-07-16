# ADR 0004: Preserve root configuration and retain tagged registry content

![A preserved root plan anchors a basecamp beside an orderly registry of retained tagged cases](../assets/adr-0004-config-registry-contracts.png)

## Status

Accepted on 2026-07-14.

## Decision

Camp discovers devcontainer configuration only at standard paths in the adopted root unless the user passes an explicit path. An existing invalid root config is an error. Nested project configs are never selected implicitly. When no root config exists, Camp writes a digest-locked Room of Requirement Wolfi overlay under `.camp/runtime/` and passes it explicitly to DevPod.

The inner tar.zst contains the complete root except `.camp/build/` and `.camp/runtime/`. Hauler's writable Distribution backend is separate from its OCI-layout store and has no supported arbitrary import command. Camp keeps one mutable session registry overlay, establishes a brief write barrier for checkpointing, catalogs every tagged reference, combines it with the workspace image inventory, and re-pulls each known reference into a fresh Hauler store. It resumes the same overlay after the checkpoint so pushes racing after the snapshot remain available for the next generation.

## Consequences

Direct tagged pushes to the live Camp registry survive checkpoints without replacing live mutable state with an immutable store. Untagged manifests, digest-only objects, and arbitrary OCI artifacts are not promised; documentation and status output state this boundary plainly.
