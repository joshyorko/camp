# Managed distribution tools

Use this note when changing `tools.lock.yaml` or `internal/adapters/tools`.

## Identity and installation contract

- DevPod is a raw executable, so its locked asset SHA-256 is also the authoritative executable identity. Hauler is a tar.gz asset, so Camp must verify the locked archive SHA-256 first, validate that the archive contains exactly one regular executable named `hauler`, and then record and recheck the derived executable SHA-256. A `--version` string is never identity evidence.
- A PATH binary is accepted only when its executable digest matches the authoritative digest. For an archive release, Camp derives that digest from the checksum-verified archive before accepting the PATH candidate.
- Managed installs stage downloads beneath the destination filesystem in a private directory, bound compressed and decompressed sizes, reject links/traversal/extra entries, fsync the executable and identity record, and publish the completed directory with one atomic rename while holding the tool/repository/version/platform lock. An interrupted or losing installer may reuse only a final executable whose identity record and current digest both verify.
- Locked download URLs must be credential-free HTTPS. Redirects may carry opaque signed query parameters used by GitHub releases, but every hop must remain HTTPS on an explicit approved host and errors must not echo URLs, query strings, headers, or redirect targets.
- `pasta` is an `external-host-capability`, not a managed distribution tool. It must stay out of `tools.lock.yaml`; Camp discovers it on PATH and probes the required option surface but never downloads it or invokes a host package manager.

## Pin review

For a DevPod or Hauler update, review the upstream repository, tag, and commit together; update both Linux amd64 and arm64 assets; independently calculate each asset SHA-256; and rerun the focused and race tests plus real binaries on both architectures. Review raw-versus-archive shape before changing the installer. A checksum committed beside a mutable release source proves that retrieved bytes match the reviewed pin; it is not publisher-authenticity evidence by itself. A skipped real-binary or architecture gate is not proof.

The focused verification commands are:

```text
go test ./internal/adapters/tools -count=1
go test -race ./internal/adapters/tools -count=1
```

The option-surface `PastaProbe` is intentionally narrower than the full setup/doctor runtime proof described by ADR 0006; namespace creation, loopback mapping, listener scope, and cleanup remain doctor-layer responsibilities.
