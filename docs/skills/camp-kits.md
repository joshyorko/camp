# Camp Kit Manifest Contract

CampKit schema version 2 is the current manifest-validation contract. The
`internal/campkit` package validates, canonically encodes, and strictly decodes
manifests. Camp does not yet export, archive, inspect, verify, import, load, or
accept kits, and schema version 1 is intentionally unsupported because no
public command consumed or emitted it.

## Fixed wire closure

A valid v2 manifest binds one positive capsule generation and optional earlier
parent to its archive digest, archive payload, raw metadata payload, and exact
lineage source generation. Payload sizes are positive and their overflow-safe
sum fits in `int64`.

The supported platform set is non-empty and contains at most the two exact
platforms `linux/amd64` and `linux/arm64`; variants are not supported. Every
listed platform has exactly one Camp executable, runtime, DevPod provider,
`devpod` tool, `hauler` tool, and Room image. Tool identities include
repository, version, 40-character lowercase commit, asset digest, and
executable digest. DevPod's asset and executable digests are equal; Hauler's
may differ. Exact equality to the current `tools.lock.yaml` is an export
resolver responsibility, not a stable manifest-decoder rule.

Payload paths are printable ASCII paths rooted below `payloads/`, at most 255
bytes, with no absolute, empty, dot, traversal, backslash, drive, URL, query,
fragment, duplicate, or ancestor-colliding form. File digests use 64 lowercase
hex characters. OCI references contain exactly one terminal
`@sha256:<64-lowercase-hex>` digest, no mutable tag, scheme, credentials,
whitespace, query, or fragment, and may not reuse one reference across
platforms as though an index digest were platform-specific.

## Trust and canonical bytes

`unverified` carries no verifier, time, evidence path, or trust-evidence
payload. `verified` and `rejected` both require a digest-shaped verifier,
nonzero verification time no later than export, and exactly one immutable
trust-evidence payload bound by path. A rejected manifest may be checksum-valid
but is not trusted; signature verification and local receipts are outside this
contract.

Canonical JSON is compact Go struct-field order with no newline. Encoding
deep-copies the manifest, normalizes copied timestamps to UTC `Z`, and sorts
copied platform, payload, and image slices by the schema's bytewise keys.
Decoding enforces a 4 MiB byte limit, 256 payloads, 8,192 images, two
platforms, 4 KiB non-path strings, strict known fields, one JSON value, full
validation, and byte-for-byte equality with canonical reserialization.
Unsupported schema versions return `*UnsupportedSchemaError`; v1 is not
upgraded or reinterpreted.

## Verification and boundaries

Run the package contract with:

```bash
go test ./internal/campkit -count=1
go test -race ./internal/campkit -count=1
go vet ./internal/campkit
```

The tests cover canonical permutation and deep non-mutation, UTC normalization,
strict decoding, schema classification, generation and metadata binding,
platform/tool/Room closure, paths and bounds, OCI identity, semantic
duplicates, trust matrices, hostile values, and exact round trips.

Archive entry limits, compressed-byte safety, raw generation metadata parsing,
payload byte comparison, signature verification, authoritative artifact
resolution, deterministic archive writing, publication, import recovery, image
loading, and disconnected acceptance remain later lifecycle work. Do not treat
a valid C0 manifest as proof that any of those operations exist or succeeded.
