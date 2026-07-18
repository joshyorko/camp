# Testing and Release Evidence

## Evidence ladder

Run the narrowest affected package first, then the repository gates:

```bash
go test ./... -count=1
go vet ./...
go build ./cmd/camp
git diff --check
```

Report passed, failed, and skipped gates separately. Installed-tool tests may skip without pinned DevPod, Hauler, or `pasta`; those skips leave the real lifecycle unproved. A package test, commit, push, merge, packaged artifact, and deployed release are distinct evidence states.

For filesystem-dependent safety tests, prove determinism with repeated focused execution when practical. The ownership-marker temporary-name substitution test requires injection of the named fallback because a Linux filesystem may support `O_TMPFILE`; the focused test passed 50 repetitions after that injection, and `go test ./internal/capsule -count=1` passed 52 tests.

## Release gate

Do not describe Camp as released or clean-machine-ready until the packaged binary, locked tools, real local lifecycle, portable backend lifecycle, and required Room/Wolfi/Rust/direct-registry acceptance matrix have concrete passing evidence. The current root-only executable is buildable but is not that release.

## Evidence

- `AGENTS.md`
- `cmd/camp/main.go`
- `integration/contracts_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
- `.github/` is currently absent, so no repository CI/release workflow is established.
