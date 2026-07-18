# CLI Composition

## Current boundary

The executable is intentionally a root-only Cobra command. There is no production command composition for the target `open`, `sync`, `close`, `attach`, `status`, `list`, `doctor`, or `recover` experience yet. Do not use package-level application tests as evidence that those commands exist.

When adding commands, compose existing application use cases and adapters instead of moving lifecycle logic into Cobra handlers. Preserve exact argument arrays through typed ports; do not rebuild shell command strings.

## Proof commands

```bash
go build ./cmd/camp
go run ./cmd/camp --help
```

A command is usable only when its handler is wired to production dependencies and a focused test proves its output/error contract. A help entry alone is not lifecycle proof.

## Evidence

- `cmd/camp/main.go`
- `internal/app/`
- `internal/adapters/devpod/client_test.go`
- `internal/adapters/hauler/client_test.go`
