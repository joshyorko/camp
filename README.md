<p align="center">
  <img src="docs/assets/camp-hero.png" alt="A canvas tent and its packed capsule in a sunny mountain clearing" width="100%">
</p>

# Camp

**Break camp here. Make camp anywhere.**

Camp is a Linux lifecycle CLI for carrying an entire development world between machines. It opens a complete Second Brain—working tree, development container, and named OCI images—then checkpoints it into a verified, versioned Hauler archive before cleaning up the temporary environment.

Camp is deliberately small. [Hauler](https://github.com/hauler-dev/hauler) owns the capsule format and OCI content. [DevPod](https://github.com/skevetter/devpod) owns providers, containers, SSH, forwarding, and IDE transport. Camp coordinates the safe lifecycle around them:

```text
resolve → hydrate → serve → enter → checkpoint → seal → publish → clean up
```

## Project status

Camp is under active construction and is not ready for daily use yet. The repository contains the durable journal, coordination, archive, hydration, ownership, checkpoint, registry, image, supervision, workspace, and application-layer foundations. The public command tree and complete real local lifecycle are still being wired and verified.

The current executable exposes setup and lifecycle commands, but the complete real lifecycle still has environment-specific gates. Do not treat a help entry or skipped real-tool test as released lifecycle proof.

### Implemented and tested internally

| Area | Current foundation |
| --- | --- |
| Durable state | Atomic mode-`0600` intent/fact journals, recovery records, operation locks, and unknown-outcome reconciliation |
| Coordination | Immutable generations, main/branch pointers, lineage-scoped writer leases, and compare-and-swap publication |
| Capsule safety | Strict source selection, adopted-root preservation, ownership markers, secure archive extraction, and digest-locked devcontainer fallback |
| Lifecycle use cases | Internal open/re-entry, checkpoint, sync, close, supervision, registry, and image orchestration behind typed ports |
| External tools | Exact argument construction and contract tests for the pinned DevPod and Hauler versions |

These are package-level foundations, not proof that the user-facing lifecycle is complete. Today the file backend is the only backend, workspace return is limited to a proven local-provider no-op, IDE entry is rejected, and installed-tool integration tests may skip when their real binaries are unavailable.

## Command and operator documentation

The [operator documentation](docs/README.md) contains the generated command
reference, deterministic syntax transcripts, backend and recovery contracts,
and the current release limitations. It intentionally omits aspirational
terminal output. `camp open memoryd` selects `MemoryD` only as the landing
directory; it does not turn a child repository into a separate capsule.

## Design

### The whole root is the capsule

The authoritative open state is the complete hydrated Second Brain root. Repositories, notes, generated files, language build directories, virtual environments, patches, and nested projects travel together. Camp excludes only its own recursive build and runtime paths.

### Explicit checkpoints, not background sync

`camp sync` creates a checkpoint while the session remains open. `camp close` creates the final checkpoint and then tears down the workspace. A crash leaves durable local recovery state; it never grants permission to repeat an outcome-unknown destructive or publishing operation blindly.

### Safety before cleanup

Camp separates publication success from cleanup success and preserves these invariants:

- never overwrite an unexplained materialization or adopted source root;
- never delete user-adopted content;
- verify immutable bytes and the remote read-back before moving a pointer;
- reconcile unknown outcomes by observation rather than duplicate effects;
- publish through one checkpoint path with lineage-aware compare-and-swap;
- remove only Camp-owned paths after marker, canonical-path, device, and inode validation;
- keep credentials and raw bootstrap secrets out of durable journals and capsules.

## Toolchain

Camp targets Linux and currently locks:

| Component | Locked contract | Responsibility |
| --- | --- | --- |
| DevPod | [`skevetter/devpod` v0.26.1](https://github.com/skevetter/devpod/releases/tag/v0.26.1) | Providers, devcontainers, SSH, forwarding, and IDE transport |
| Hauler | [`hauler-dev/hauler` v2.0.2](https://github.com/hauler-dev/hauler/releases/tag/v2.0.2) | Versioned haul files, OCI content, registry, and file serving |
| Room of Requirement | [`joshyorko/room-of-requirement` v1.18.3](https://github.com/joshyorko/room-of-requirement/releases/tag/v1.18.3) | Default Wolfi development-image compatibility fixture |

Exact commits, Linux asset URLs, architectures, and SHA-256 checksums live in [`tools.lock.yaml`](tools.lock.yaml).

Install or reuse the pinned DevPod and Hauler binaries without running them:

```console
$ camp setup
```

When a locked binary is not already on PATH, setup reports its verified managed
location. It does not edit shell startup files or install `pasta`; `pasta`
remains an external host capability tracked separately in issue #11.

## Development

Requirements:

- Linux
- Go 1.25 or newer
- Git
- Pinned DevPod and Hauler binaries for installed-tool and real integration gates
- A compatible `pasta` binary for the loopback-confinement integration gates

Build the current command:

```bash
go build ./cmd/camp
```

Run the default repository checks:

```bash
go test ./... -count=1
go vet ./...
git diff --check
```

Installed-tool and real lifecycle tests have additional host requirements; installed-tool tests may skip when their binaries are unavailable. The full local vertical is not considered complete when those real gates are skipped.

## Architecture records

- [Hauler and DevPod boundary](docs/adr/0001-hauler-devpod-boundary.md)
- [Generations, leases, and recovery](docs/adr/0002-generations-leases-and-recovery.md)
- [Pinned toolchain](docs/adr/0003-pinned-toolchain.md)
- [Capsule, devcontainer, and registry contracts](docs/adr/0004-capsule-devcontainer-and-registry-contracts.md)
- [Materialization ownership and supervision](docs/adr/0005-materialization-ownership-and-supervision.md)
- [Hauler loopback confinement](docs/adr/0006-hauler-loopback-confinement.md)
- [Implementation plan](docs/superpowers/plans/2026-07-14-camp.md)
- [Architecture and safety review](docs/reviews/2026-07-14-oracle-architecture-review.md)

## Guiding promise

One command should open the whole development world. One command should pack it back up. When Camp leaves a machine, user data remains verified and the temporary environment becomes clean again.
