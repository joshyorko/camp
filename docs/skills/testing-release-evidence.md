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

Supervisor heartbeat tests must synchronize on the complete event being
asserted. The fake lease keeper's `renewed` channel fires inside `Renew`, before
the supervisor records the durable fact and releases its operation lock; using
that signal alone to assert `fact` or `release` is timing-dependent under the
race detector. Wait for the required event-log sequence instead.

## Release gate

Do not describe Camp as released or clean-machine-ready until the packaged binary, locked tools, real local lifecycle, portable backend lifecycle, and required Room/Wolfi/Rust/direct-registry acceptance matrix have concrete passing evidence. The executable is buildable and locally packageable, but that is not release evidence.

## Generic archive evidence

Run the repository-owned archive builder from the repository root with an
explicit version, full commit, and reproducible timestamp:

```bash
VERSION=0.0.0-test \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
SOURCE_DATE_EPOCH=1784678400 \
OUTPUT_DIR="$PWD/dist" \
./packaging/build-archives.sh
```

The builder requires GNU `tar`, `gzip`, `date`, `sha256sum`, and the Go
toolchain. It produces normalized Linux amd64/arm64 archives plus
`checksums.txt`. Archive order, numeric ownership, timestamps, gzip headers,
Go paths, and VCS stamping are normalized. `go test ./packaging -count=1`
builds twice into isolated directories, compares every output byte, extracts
the amd64 archive, and runs the packaged binary's `--version`, `--help`, and
bash/zsh/fish completion paths.

The generic archive declares `passt`/`pasta` as an external host prerequisite.
`packaging/homebrew/metadata.json` names the intended
`joshyorko/homebrew-tap/Formula/camp.rb` destination, and
`packaging/homebrew/camp.rb.tmpl` records the Linux architecture, checksum,
dependency, completion, and formula-test shape. Its URL, version, and checksum
tokens stay unresolved until a separate publication lane supplies real release
artifacts.

These checks prove only reproducible local archive construction and package
shape. They do not prove a GitHub release, a usable tap update path, native
DEB/RPM/APK ownership, clean install/upgrade/uninstall, first-use managed-tool
bootstrap, or a real DevPod/Kubernetes lifecycle.

## Evidence

- `AGENTS.md`
- `cmd/camp/main.go`
- `packaging/build-archives.sh`, `archive_smoke_test.go`, and `homebrew_metadata_test.go`
- `packaging/homebrew/metadata.json` and `camp.rb.tmpl`
- `integration/contracts_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
- `docs/superpowers/plans/2026-07-14-camp.md` (names the currently missing local lifecycle gates)
- `.github/` is currently absent, so no repository CI/release workflow is established.
