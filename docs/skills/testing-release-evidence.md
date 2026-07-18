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

When a stacked PR's base merges, restack the child directly onto the resulting `master` commit and compare `master...HEAD` before pushing. The post-rebase diff must contain only the child scope; record the old and new head SHAs, then force-push with a lease pinned to the observed old remote head so concurrent updates fail instead of being overwritten.

Named acceptance gates require discovery evidence before execution evidence. `go test -run` exits zero even when no matching test exists, so first require the exact test name from `go test -list`, then run with `-v` and retain the matching `=== RUN` and `--- PASS` lines. A package-level `PASS` accompanied by `[no tests to run]` is not acceptance evidence.

```bash
go test ./integration -list '^TestNamedAcceptanceGate$'
go test -v ./integration -run '^TestNamedAcceptanceGate$' -count=1
```

This guard currently matters for `TestLocalLifecycleVertical` and `TestLocalLifecycleCrashMatrix`: neither name is present, while both focused `-run` commands still exit zero with `testing: warning: no tests to run`. Treat both gates as missing, not passed, until discovery lists them and their runs emit the named `RUN`/`PASS` pair.

For filesystem-dependent safety tests, prove determinism with repeated focused execution when practical. The ownership-marker temporary-name substitution test requires injection of the named fallback because a Linux filesystem may support `O_TMPFILE`; the focused test passed 50 repetitions after that injection, and `go test ./internal/capsule -count=1` passed 52 tests.

## Release gate

Do not describe Camp as released or clean-machine-ready until the packaged binary, locked tools, real local lifecycle, portable backend lifecycle, and required Room/Wolfi/Rust/direct-registry acceptance matrix have concrete passing evidence. The current root-only executable is buildable but is not that release.

## Evidence

- `AGENTS.md`
- `cmd/camp/main.go`
- `integration/contracts_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
- `docs/superpowers/plans/2026-07-14-camp.md` (names the currently missing local lifecycle gates)
- `.github/` is currently absent, so no repository CI/release workflow is established.
