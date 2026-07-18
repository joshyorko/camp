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

Filesystem-safety tests must inject the temporary-file strategy when they need the named fallback. Do not assume the filesystem rejects Linux `O_TMPFILE`; the production ownership-marker path deliberately attempts it before the named fallback.

## Mandatory Self-Improvement Contract

Every implementation, review, debugging, reconnaissance, and verification task must leave Camp's repository-local operational guidance measurably better in correctness, completeness, discoverability, determinism, testability, or recovery guidance.

- `docs/skills/` is the canonical home for reusable operational knowledge. Correct or extend an existing guide before creating another one.
- Document only behavior backed by code, tests, commands, observed failures, or authoritative upstream behavior. Never present planned behavior as implemented.
- Replace stale or contradictory guidance when evidence changes. Keep personal preferences out of shared project instructions.
- Do not create per-run diaries or add cosmetic prose. Keep the change as small as the durable learning permits.
- Mutating lanes update the relevant canonical guide in their branch or worktree. Read-only lanes propose an exact delta for the root integration agent.

Every agent dispatch must include:

> Before completing, improve the relevant project documentation or agent skill with durable, verified knowledge learned during this task. Do not add cosmetic prose or a session diary. Report the exact documentation delta and its evidence.

Every child or remote lane must return:

```text
Documentation improvement:
- Canonical file changed or proposed:
- Durable learning captured:
- Evidence:
- Stale or ambiguous guidance removed:
- Remaining uncertainty:
```

The root integration agent may declare the parent task complete only after every lane has returned that receipt, each durable learning has been integrated or explicitly rejected with a reason, and the canonical guides contain no contradictory claims. The final report must list the documentation improvements from the full run.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit prefixes such as `feat:`, `fix:`, `test:`, and `docs:`. Keep each commit focused and imperative. Pull requests should explain the behavior and safety impact, identify affected ADRs or issues, list exact verification commands, and disclose skipped real-tool gates. Include screenshots only for documentation or visual asset changes.

## Security & Configuration Tips

Never persist credentials or raw bootstrap secrets in journals, fixtures, logs, or capsules. Do not weaken canonical-path, ownership-marker, device, inode, digest, lease, or compare-and-swap checks without an explicit architecture decision and regression tests.
