# CLI Composition

## Current boundary

The executable is intentionally a root-only Cobra command. There is no production command composition for the target `open`, `sync`, `close`, `attach`, `status`, `list`, `doctor`, or `recover` experience yet. Do not use package-level application tests as evidence that those commands exist.

When adding commands, compose existing application use cases and adapters instead of moving lifecycle logic into Cobra handlers. Preserve exact argument arrays through typed ports; do not rebuild shell command strings.

Production composition is not only command registration. Before lifecycle handlers can be truthful, composition must supply ownership-safe close effects, a live session observer, a serving refresher after checkpoint publication, and typed propagation of DevPod and IDE options through `app.OpenRequest`. Target entry must preserve the canonical `target.Resolver` → effective DevPod workspace root → `workspace.MapTarget` chain. Until those seams exist, a Cobra command that calls only package fakes or journal state is not a usable command.

Unknown commands and arbitrary arguments must return a nonzero error. The current root-only command accepts `camp open` and an unknown token as positional arguments and exits successfully; tests for the real tree must lock down arity, stderr, and stable exit codes.

## Proof commands

```bash
go build ./cmd/camp
go run ./cmd/camp --help
```

A command is usable only when its handler is wired to production dependencies and a focused test proves its output/error contract. A help entry alone is not lifecycle proof.

## Evidence

- `cmd/camp/main.go`
- `internal/app/open.go`, `close.go`, `checkpoint.go`, and `operations.go`
- `internal/target/` and `internal/workspace/`
- `internal/adapters/devpod/client_test.go`
- `internal/adapters/hauler/client_test.go`
