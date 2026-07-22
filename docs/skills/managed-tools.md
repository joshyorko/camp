# Managed distribution tools

Use this note when changing `tools.lock.yaml` or `internal/adapters/tools`.

## Identity and installation contract

- DevPod is a raw executable, so its locked asset SHA-256 is also the authoritative executable identity. Hauler is a tar.gz asset, so Camp must verify the locked archive SHA-256 first, permit only regular non-executable `LICENSE` and `README.md` metadata plus exactly one regular executable named `hauler`, and then record and recheck the derived executable SHA-256. Duplicate, linked, path-normalized, or additional entries are rejected. A `--version` string is never identity evidence.
- A PATH binary is accepted only when its executable digest matches the authoritative digest. For an archive release, Camp derives that digest from the checksum-verified archive before accepting the PATH candidate.
- Managed installs stage downloads beneath the destination filesystem in a private directory, bound compressed and total archive-entry sizes, reject links/traversal/extra entries, fsync the executable and identity record, and publish the completed directory with one atomic rename while holding the tool/repository/version/platform lock. Reuse reads the identity record without following links and requires a bounded, stable, single-link regular file before checking the executable digest. An interrupted or losing installer may reuse only a fully verified final install.
- Locked download URLs must be credential-free HTTPS. Redirects may carry opaque signed query parameters used by GitHub releases, but every hop must remain HTTPS on an explicit approved host and errors must not echo URLs, query strings, headers, or redirect targets.
- `pasta` is an `external-host-capability`, not a managed distribution tool. It must stay out of `tools.lock.yaml`; Camp discovers it on PATH and probes the required option surface but never downloads it or invokes a host package manager.
- `camp doctor` remains a non-mutating PATH observation surface: it canonicalizes the executable, enforces a regular executable size bound, records its SHA-256, and runs a bounded identity command. That is observed identity, not supported-lock identity, so doctor reports DevPod and Hauler as degraded and never downloads or repairs them. Lifecycle composition and `camp setup` use the separate lock-backed managed resolver.

## User-visible setup

Camp compiles the repository-root `tools.lock.yaml` into the binary. Every lifecycle composition resolves the current Linux architecture and calls the identity-safe installer for DevPod and Hauler under `$XDG_DATA_HOME/camp` (or `~/.local/share/camp`) before constructing either adapter. The verified absolute paths are passed directly to DevPod commands, Hauler commands, and supervised Hauler services; first use therefore requires no PATH export or shell-startup edit. This bootstrap does not execute either tool and does not inspect, install, or package-layer `pasta`.

`camp setup` is an optional prewarm and inspection command over that same resolver. An executable already on PATH is reused only when the installer proves its authoritative binary digest. Otherwise setup reports the verified managed path; its shell-local export is optional interoperability guidance for invoking DevPod or Hauler directly, not a Camp prerequisite. `camp setup --json` returns the path, managed/PATH decision, repository, version, commit, platform, locked asset digest, derived binary digest, and optional PATH export in the versioned success envelope.

On every reuse, Camp rechecks the single-link identity marker, locked repository/version/commit/platform/asset tuple, executable digest, and—when applicable—the retained archive and derived executable digest. A missing, corrupt, linked, oversized, or mismatched final identity directory is removed while holding the matching install lock and rebuilt from a fresh private same-filesystem stage. Abandoned `.stage-*` directories are cleaned only under that identity parent. Failures before the atomic rename leave no trusted final; failures after rename leave a final directory that the next caller must fully verify before reuse.

## Pin review

For a DevPod or Hauler update, review the upstream repository, tag, and commit together; update both Linux amd64 and arm64 assets; independently calculate each asset SHA-256; and rerun the focused and race tests plus real binaries on both architectures. Review raw-versus-archive shape before changing the installer. A checksum committed beside a mutable release source proves that retrieved bytes match the reviewed pin; it is not publisher-authenticity evidence by itself. A skipped real-binary or architecture gate is not proof.

The locked Hauler v2.0.2 amd64 and arm64 tarballs both list, in order, `LICENSE` (mode 0644), `README.md` (mode 0644), and `hauler` (mode 0755). Verify a future pin directly rather than assuming this shape:

```text
curl -fsSL -o hauler.tar.gz <locked-url>
sha256sum hauler.tar.gz
tar -tvzf hauler.tar.gz
```

The focused verification commands are:

```text
go test ./internal/adapters/tools -count=1
go test ./internal/cli -run 'TestRun(Managed|Production)ToolSetup' -count=1
go test -race ./internal/adapters/tools -count=1
CAMP_TEST_REAL_TOOLS=1 go test ./internal/adapters/tools -run TestInstallerCleanInstallsRealPinnedTools -count=1 -v
```

The real-tools gate starts from an empty temporary install root, downloads the locked asset for the runner architecture, verifies and installs it through the production installer, and executes `version`. Run it separately on Linux amd64 and arm64; a cross-compiled or merely inspected arm64 asset is not an execution gate.

The option-surface `PastaProbe` is intentionally narrower than the full setup/doctor runtime proof described by ADR 0006. The initial `camp doctor` slice reuses `supervisor.ConfinementResolver`, which executes version/help probes, checks Camp's required option surface, validates executable shape, fingerprints the host boundary, and validates the SELinux `runcon` child context when enforcing. It reports `degraded`, never `healthy`: namespace creation, loopback mapping, listener scope, and cleanup remain unimplemented issue #11 work. File-backend syntax validation is likewise `degraded` until an isolated write/readback/cleanup probe succeeds.
