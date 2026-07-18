# Managed distribution tools

Use this note when changing `tools.lock.yaml` or `internal/adapters/tools`.

## Identity and installation contract

- DevPod is a raw executable, so its locked asset SHA-256 is also the authoritative executable identity. Hauler is a tar.gz asset, so Camp must verify the locked archive SHA-256 first, permit only regular non-executable `LICENSE` and `README.md` metadata plus exactly one regular executable named `hauler`, and then record and recheck the derived executable SHA-256. Duplicate, linked, path-normalized, or additional entries are rejected. A `--version` string is never identity evidence.
- A PATH binary is accepted only when its executable digest matches the authoritative digest. For an archive release, Camp derives that digest from the checksum-verified archive before accepting the PATH candidate.
- Managed installs stage downloads beneath the destination filesystem in a private directory, bound compressed and total archive-entry sizes, reject links/traversal/extra entries, fsync the executable and identity record, and publish the completed directory with one atomic rename while holding the tool/repository/version/platform lock. An interrupted or losing installer may reuse only a final executable whose identity record and current digest both verify.
- Locked download URLs must be credential-free HTTPS. Redirects may carry opaque signed query parameters used by GitHub releases, but every hop must remain HTTPS on an explicit approved host and errors must not echo URLs, query strings, headers, or redirect targets.
- `pasta` is an `external-host-capability`, not a managed distribution tool. It must stay out of `tools.lock.yaml`; Camp discovers it on PATH and probes the required option surface but never downloads it or invokes a host package manager.

## Pin review

For a DevPod or Hauler update, review the upstream repository, tag, and commit together; update both Linux amd64 and arm64 assets; independently calculate each asset SHA-256; and rerun the focused and race tests plus real binaries on both architectures. Review raw-versus-archive shape before changing the installer. A checksum committed beside a mutable release source proves that retrieved bytes match the reviewed pin; it is not publisher-authenticity evidence by itself. A skipped real-binary or architecture gate is not proof.

The locked Hauler v2.0.1 amd64 and arm64 tarballs both list, in order, `LICENSE` (mode 0644), `README.md` (mode 0644), and `hauler` (mode 0755). Verify a future pin directly rather than assuming this shape:

```text
curl -fsSL -o hauler.tar.gz <locked-url>
sha256sum hauler.tar.gz
tar -tvzf hauler.tar.gz
```

The focused verification commands are:

```text
go test ./internal/adapters/tools -count=1
go test -race ./internal/adapters/tools -count=1
CAMP_TEST_REAL_TOOLS=1 go test ./internal/adapters/tools -run TestInstallerCleanInstallsRealPinnedTools -count=1 -v
```

The real-tools gate starts from an empty temporary install root, downloads the locked asset for the runner architecture, verifies and installs it through the production installer, and executes `version`. Run it separately on Linux amd64 and arm64; a cross-compiled or merely inspected arm64 asset is not an execution gate.

The option-surface `PastaProbe` is intentionally narrower than the full setup/doctor runtime proof described by ADR 0006; namespace creation, loopback mapping, listener scope, and cleanup remain doctor-layer responsibilities.
