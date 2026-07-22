# ADR 0003: Lock the user-selected DevPod fork and Hauler

![Pinned expedition tools sit in fitted positions on a calibrated outdoor workbench](../assets/adr-0003-pinned-toolchain.png)

## Status

Accepted on 2026-07-14.

## Decision

Camp locks:

- `skevetter/devpod` v0.26.1, commit `86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e`.
- `hauler-dev/hauler` v2.0.2, commit `4ece589a5c763fff15e253735263bd13a889d3cc`.
- `joshyorko/room-of-requirement` v1.18.0, commit `0aabf18ad291c590498bd8e904a7d09f66378b85` as the compatibility fixture.

The DevPod choice is a direct user override of the build prompt's initial v0.9.11 pin. Matching PATH binaries are preferred. Missing or mismatched DevPod and Hauler binaries are installed as checksum-verified Linux amd64/arm64 assets in Camp's XDG data directory without shell-piped installers. The `pasta` confinement helper is the separately validated external host capability defined by ADR 0006; Camp does not install it.

## Consequences

DevPod raw CLI asset checksums are locked directly because the release has no checksum manifest or detached raw-CLI signatures. Tool upgrades require a lock edit, source-contract review, and green contract/integration tests.
