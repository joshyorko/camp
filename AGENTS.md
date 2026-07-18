# Repository Guidelines

## Project Structure & Module Organization

Camp is a Linux-focused Go CLI. The executable entry point lives in `cmd/camp/`. Core behavior is organized under `internal/`, with infrastructure integrations in `internal/adapters/` and application/domain packages kept separate from external tools. Cross-package contract tests live in `integration/`; most unit tests sit beside their source as `*_test.go`. Architecture decisions and design assets are under `docs/adr/` and `docs/assets/`. Treat `tools.lock.yaml` as the authoritative contract for DevPod, Hauler, and fixture versions.

## Build, Test, and Development Commands

- `go build ./cmd/camp` builds the current CLI.
- `go test ./... -count=1` runs all Go tests without cached results.
- `go vet ./...` checks for suspicious Go constructs.
- `gofmt -w <files>` formats changed Go files before review.
- `git diff --check` detects whitespace errors.

Run commands on Linux, preferably in the repository's project container when one is available. Installed-tool and lifecycle tests require the pinned DevPod and Hauler binaries; loopback-confinement tests also require `pasta`. A skipped real-tool test is not proof that the full lifecycle works.

## Coding Style & Naming Conventions

Follow standard Go formatting and package conventions: tabs via `gofmt`, short lowercase package names, `PascalCase` exported identifiers, and `camelCase` internal identifiers. Keep interfaces narrow and define them near their consumers. Preserve Camp's existing separation between domain/application logic and adapters. Name platform-specific files with Go suffixes such as `_linux.go` and `_other.go`.

## Testing Guidelines

Use Go's `testing` package and table-driven tests where multiple cases share behavior. Name tests `TestXxx` and place unit tests beside the implementation. Add integration coverage in `integration/` for cross-package or real-tool contracts. Safety-sensitive changes should test failure and recovery paths, especially unknown outcomes, ownership checks, immutable publication, and cleanup guards.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit prefixes such as `feat:`, `fix:`, `test:`, and `docs:`. Keep each commit focused and imperative. Pull requests should explain the behavior and safety impact, identify affected ADRs or issues, list exact verification commands, and disclose skipped real-tool gates. Include screenshots only for documentation or visual asset changes.

## Security & Configuration Tips

Never persist credentials or raw bootstrap secrets in journals, fixtures, logs, or capsules. Do not weaken canonical-path, ownership-marker, device, inode, digest, lease, or compare-and-swap checks without an explicit architecture decision and regression tests.
