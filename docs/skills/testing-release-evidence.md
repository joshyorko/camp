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

The repository CI contract is enforced by `go test ./releasepipeline -count=1`.
Pull-request CI has read-only contents permission and no protected environment,
release write token, attestation token, or secret reference. Its unit, race, vet,
vulnerability, integration, containerized MinIO, real pinned-tool download, and
reproducible-package jobs are mandatory. The MinIO and real-tool jobs name and
run their exact acceptance tests so an absent match or an unset opt-in cannot
turn a skip into release evidence.

Every hosted job selects its Go toolchain from the full patch version in
`go.mod`. Keep that patch at or above the newest standard-library fix required
by `govulncheck`; a language-version-only pin can make otherwise current source
ship reachable vulnerabilities from the runner's selected toolchain.

Credentialed provider runs are separate from credential-free CI. A protected
`release-providers` environment and an explicit named profile are required
before a provider can be claimed. Until such a profile and successful hosted
run exist, `provider-evidence.yml` records `result=gated` and the reason; a job
skipped by `if: secrets.* != ''` is never provider evidence. Do not put secret
values in evidence JSON, artifacts, caches, output, plans, or generated config.

Clean-runner CI must not inherit workstation capabilities. Run CLI composition
tests with a PATH containing only the test's declared fakes and the Go/system
tools; `camp init` must not resolve DevPod or Hauler because initialization uses
only the Docker manifest boundary. Linux cancellation tests must spawn a child
process (`sleep 30 & wait`) and prove the whole process group terminates within
the deadline; killing only the shell can leave descendants holding captured
stdout/stderr open until their natural exit.

Replacement-identity tests must account for immediate inode reuse on GitHub's
Ubuntu filesystems. New materialization and hydration records include Linux
`statx` birth time when the filesystem provides it, in addition to device and
inode. Older records and filesystems without birth time retain the legacy
device/inode comparison, while new records reject a replacement even when the
filesystem reuses the same inode. Cleanup of a renamed regular file compares
mode, link count, size, and modification time as well as device/inode; change
time cannot be compared across rename because rename itself changes it.

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

These checks prove reproducible archive and native-package construction. The
non-skipping package fixtures additionally prove clean DEB/RPM/APK and local
Homebrew install, packaged completions, first-use managed-tool bootstrap,
upgrade state preservation, and package-owned uninstall behavior. They do not
prove a GitHub release, a published tap update path, or a real
DevPod/Kubernetes lifecycle.

## Release-candidate evidence

Build the candidate bundle with the same immutable inputs used for generic
archives:

```bash
VERSION=0.0.0-test \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
SOURCE_DATE_EPOCH=1784678400 \
OUTPUT_DIR="$PWD/dist" \
./packaging/build-release-evidence.sh build
```

The bundle contains Linux amd64/arm64 archives, `checksums.txt`, one SPDX 2.3
JSON SBOM per archive, a rendered `camp.rb`, and `evidence.json`. Each SBOM
package checksum must equal the SHA-256 of the final compressed archive, not an
intermediate binary or directory. The evidence manifest records the commit,
version, platform, digest, result, SBOM filename, package result, and every
gated reason.

Verification must happen after artifact download into a fresh job. Run one
native job per supported architecture:

```bash
VERSION=0.0.0-test \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
VERIFY_ARCH=amd64 \
OUTPUT_DIR="$PWD/dist" \
./packaging/build-release-evidence.sh verify
```

Verification checks the downloaded checksum manifest and SBOM digest, extracts
the matching archive, installs its binary into a fresh temporary prefix, and
executes version, help, and bash/zsh/fish completion commands. It emits
`verification-<arch>.json`; both supported architectures must report `passed`.
Checksums stored beside mutable release assets detect accidental or transport
corruption but do not authenticate the publisher. GitHub artifact attestations
bind final archive digests to the workflow identity; protected tag/manual
publication remains downstream of downloaded-artifact verification and
attestation.

A manual release workflow with `publish=false` is the safe hosted validation
lane: it builds, uploads, downloads, checksum-verifies, and natively exercises
both architectures, but skips attestation and publication. Attestation is a
public provenance side effect, so it runs only for a tag or an explicitly
approved manual publication. A dry-run artifact proves candidate mechanics; it
does not prove attestation or a published release.

Before closing issue #13, link the successful mandatory CI run, both native
verification jobs, uploaded GitHub artifact digest, archive checksums, exact
SBOM digest bindings, attestations, protected provider evidence for every
claimed profile, and every explicit gated or unsupported lane. A green local
run or draft PR is not closure evidence and must not trigger a real release.

The real Homebrew lifecycle fixture builds two archive versions, serves a
local git tap to the official `homebrew/brew` container, and requires tap,
install, completion, update, upgrade, and uninstall to succeed. It sets
`HOMEBREW_NO_AUTOREMOVE=1` only for uninstall: Camp does not own the shared
`passt` prerequisite, and Homebrew 5.0.13 on Linux can recurse through injected
global dependencies (`bubblewrap`, GCC, and `passt`) while autoremoving them.
The fixture still requires Camp's keg and linked binary to be removed and
verifies operator configuration survives. Run it with GNU tar first on `PATH`;
BusyBox tar cannot build the reproducible input archives because it lacks
`--sort=name` and the other normalization flags. A clean container must also
mark the bind-mounted local tap as a Git safe directory before advancing its
test ref; the fixture removes only Homebrew's test-owned XDG subtree before
the host deletes its temporary root.

## Evidence

- `AGENTS.md`
- `cmd/camp/main.go`
- `packaging/build-archives.sh`, `archive_smoke_test.go`, `homebrew_metadata_test.go`, and `homebrew_lifecycle_test.go`
- `packaging/fixtures/homebrew-smoke.sh` (observed against Homebrew 5.0.13)
- `packaging/homebrew/metadata.json` and `camp.rb.tmpl`
- `integration/contracts_test.go`
- `internal/capsule/ownership.go` and `internal/capsule/ownership_test.go`
- `internal/app/supervise_test.go`
- `docs/superpowers/plans/2026-07-14-camp.md` (names the currently missing local lifecycle gates)
- `.github/workflows/ci.yml`, `release.yml`, and `provider-evidence.yml`
- `releasepipeline/workflow_contract_test.go` and `evidence_test.go`
- `packaging/build-release-evidence.sh`
