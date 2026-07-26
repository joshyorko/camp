# Camp Kit Manifest Contract

CampKit schema version 2 is the current manifest-validation contract. The
`internal/campkit` package validates, canonically encodes, and strictly decodes
manifests. `Inspect` and `Verify` operate on `manifest.json`-first deterministic
tar+zstd archives. CLI composition reserves `camp kit export --generation REF
--output FILE` for a lifecycle exporter with strict required flags; the
production generation resolver is not wired yet. Camp still does not import,
load, or accept kits into its lifecycle backend via CLI. `camp kit import FILE
--as CAMP` does perform complete verification before extracting the verified
regular payload closure into a new no-replace local import directory; it does
not create a backend pointer or claim disconnected lifecycle proof. Schema
version 1 is intentionally unsupported because no
public command consumed or emitted it.

## CLI usage

- `camp kit inspect FILE`: prints the decoded manifest summary (`inspect`)
- `camp kit verify FILE`: verifies payload integrity and prints a success summary (`verify`)
- `camp kit import FILE --as CAMP`: verifies the complete archive, then publishes
  its regular payloads into a new XDG-local import directory without replacing
  an existing name.
- `--json` is supported for inspect, verify, and import.
- `FILE` must be a regular file path; `camp` rejects missing files, directories,
  and symlinks before attempting to inspect or verify.

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
but is not trusted; both statuses require evaluator participation at verification
time. Only an evaluator result of verified establishes trusted evidence; rejected
is an explicit non-trusted result even when checksums are valid. Signature
verification and local receipts are outside this contract.

Archive `Inspect` verifies determinism and manifest-decoding only. `Verify`
streams payload bodies and checks size/digest/name ordering, then separates
integrity status from trust evaluation so integrity can be valid while trust remains
unverified.
For integrity checking, most payloads are hashed through a bounded reader so archive
bodies are not fully materialized. `PayloadGenerationMetadata` and
`PayloadTrustEvidence` are still buffered to validate metadata bindings and to
pass trust-evidence bytes to the configured `TrustEvaluator`.

Trust status remains `unverified` unless manifest trust metadata is `verified` or
`rejected`. `Verify` requires an evaluator and bound evidence for either status;
it returns `ErrTrustUnsupported` when either is unavailable and never promotes a
rejected result to trusted. No checksum-only result is publisher authenticity.

Canonical JSON is compact Go struct-field order with no newline. Encoding
deep-copies the manifest, normalizes copied timestamps to UTC `Z`, and sorts
copied platform, payload, and image slices by the schema's bytewise keys.
Decoding enforces a 4 MiB byte limit, 256 payloads, 8,192 images, two
platforms, 4 KiB non-path strings, strict known fields, one JSON value, full
validation, and byte-for-byte equality with canonical reserialization.
Unsupported schema versions return `*UnsupportedSchemaError`; v1 is not
upgraded or reinterpreted.

## Exact-generation reads for reconstruction and recovery

Generation reads are currently split:

- `ReadMetadata(...)` resolves by logical generation identity and validates stored
  metadata invariants.
- `ResolveExactGeneration(...)` reads the metadata payload by exact generation key and
  returns reopenable handles for the exact archive and metadata sidecar keys, plus
  their identity fingerprints.

The exact path verifies both object-level and metadata-level integrity before
returning:

- metadata decoding from the exact sidecar key, with raw-sidecar bytes included in the
  result;
- object metadata checks against `GenerationMetadata.ArchiveSHA256` and byte size;
- optional source identity validation via `ports.ObjectStoreIdentity` when available.

The file backend now surfaces local object-source identity through
`ObjectSourceFingerprint` with `Kind: "file"`, canonical path, and optional
`Device`/`Inode` fields for drift detection. This is best-effort metadata;
verifier semantics are still driven by generation metadata and archive hashing.
`ResolveExactGeneration` validates object and sidecar fingerprints when it
resolves a generation, but its returned sources reopen the backend later.
`ExactGenerationRecord.RevalidateSources` repeats those checks and must be
called by export orchestration before and after copying payloads; initial
resolution alone is not a transfer lock.
`app.ExportCampKit` is the application seam: it requires an authoritative
resolver and revalidator, performs the first check before streaming, and uses
the final check immediately before `ExportFile`'s no-replace publication. It
does not move pointers or acquire leases.

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

Archive entry limits, compressed-byte safety, payload byte comparison, and
deterministic archive writing are implemented in `campkit.Export`. It validates
the manifest first, requires a restartable source for every declared payload,
streams each source through bounded size counting and SHA-256 while writing the
canonical manifest first and payloads in bytewise path order. `ExportFile` adds
same-directory ownership-checked temporary output, fsync, rename, and
parent-directory fsync; it refuses to replace an existing output and removes
only its owned temporary file on failure. Raw `Export` may have written partial
bytes to an arbitrary writer when it fails; only `ExportFile` is an atomic
publication boundary. These functions do not resolve
generations or publish/import Camp state. An application exporter must
revalidate exact-generation fingerprints before and after transfer; initial
source resolution is not a transfer lock. Do not treat
a valid C0 manifest as proof that any of those operations exist or succeeded.
