# Camp Kit Manifest Contract

Camp Kit schema version 1 is the portable identity and trust boundary for
offline Camp artifacts. It describes closure only; it does not resolve,
download, pack, import, export, catalog, or run any artifact.

## Required closure

A valid manifest records all of these identities:

- the Camp binary name, version, and SHA-256;
- the capsule ID, immutable generation number, and archive SHA-256;
- one runtime payload plus every required tool payload by name, version, and
  SHA-256;
- every workspace image and the Room image by digest-pinned OCI reference and
  matching manifest digest;
- the DevPod provider payload by name, version, and SHA-256;
- the complete supported architecture set (`amd64`, `arm64`, or both);
- an explicit trust status and any evidence required by that status;
- a stable kit ID, UTC export time, source generation digest, and optional
  source/parent kit digests for import/export lineage.

All SHA-256 values use 64 lowercase hexadecimal characters. OCI digests and
references use the `sha256:` prefix; file payload identities do not. Capsule
and kit IDs are single safe path segments.

## Canonical JSON

Canonical bytes are compact JSON with schema-defined field order, UTC
RFC 3339 timestamps, sorted tools, sorted workspace images, and sorted
architectures. There is no trailing newline. Canonical decoding rejects
unknown fields, trailing data, non-canonical ordering or whitespace,
unsupported schema versions, missing closure, duplicate identities, mutable
image references, mismatched image digests, unsafe IDs, and unsupported
architectures.

Serialization operates on a copy. It must not reorder or otherwise mutate the
caller's manifest.

## Trust status

`unverified` is the safe default and carries no claimed verification evidence.
`verified` requires the verifier identity, verification time, and an immutable
signature payload identity. `rejected` records a failed trust decision and
cannot be used as verified closure. The manifest records a trust decision; this
contract does not implement signature verification.

## Evidence

- `internal/campkit/manifest_test.go` defines the schema, canonical-byte,
  completeness, safety, lineage, and trust-status acceptance tests.
- Git history preserves the initial failing test checkpoint before the
  production implementation.
