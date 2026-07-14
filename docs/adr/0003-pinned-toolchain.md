# ADR 0003: Lock the user-selected DevPod fork and Hauler

## Status

Accepted on 2026-07-14.

## Decision

Camp locks:

- `skevetter/devpod` v0.26.1, commit `86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e`.
- `hauler-dev/hauler` v2.0.1, commit `4f47155d6f8ccec22ba6f609f2f1f4919b02fce1`.
- `joshyorko/room-of-requirement` v1.18.0, commit `0aabf18ad291c590498bd8e904a7d09f66378b85` as the compatibility fixture.

The DevPod choice is a direct user override of the build prompt's initial v0.9.11 pin. Matching PATH binaries are preferred. Missing or mismatched tools are installed as checksum-verified Linux amd64/arm64 assets in Camp's XDG data directory without shell-piped installers.

## Consequences

DevPod raw CLI asset checksums are locked directly because the release has no checksum manifest or detached raw-CLI signatures. Tool upgrades require a lock edit, source-contract review, and green contract/integration tests.
