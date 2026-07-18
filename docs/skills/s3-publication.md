# S3 immutable publication

Camp keeps the S3 adapter's narrow injected HTTP `Signer` seam and uses the AWS
SDK for Go v2 only in `internal/adapters/objectstore`, where the backend factory
loads the standard runtime credential chain and supplies a SigV4
signer. This preserves focused protocol tests without reimplementing credential
discovery. Credentials are retrieved at request time and must not enter
`s3store.Config`, backend descriptors, object metadata, object keys, journals,
or logs.

Resolve a backend through `config.ResolveBackend`. The application composition
seam passes that descriptor through `app.NewOpenWithBackend`, which constructs the
`ports.ObjectStore`, binds the pointer, generation, and lease repositories to
that same store, rebinds the hydration controller's archive reads, and carries
the resolved identity into session recovery through
`config.DurableBackendConfiguration`. The constructor-bound descriptor is
authoritative: a request may repeat it but must fail closed if it supplies a
different identity or if no backend was constructor-bound, because request
metadata cannot select or relabel already-injected repositories. The legacy
file-backend request field is likewise an identity assertion, not a store
selector, and must match the constructor backend. Factory and
application tests prove this composition seam; they are not executable wiring
proof. Until `cmd/camp` calls this seam, describe the feature as backend
composition rather than production CLI wiring.

The `s3://` URL contains only the bucket and optional clean prefix. Endpoint,
region, path-style addressing, and transport policy are separate bootstrap
values. The YAML `s3` block accepts only those non-secret values; AWS credential
source fields are not part of `userConfig`, and credentials come from the
standard runtime chain. IP-address-shaped and other non-DNS bucket names are
rejected. HTTPS is the default policy;
an `http://` endpoint is rejected unless `insecure: true` (or
`CAMP_S3_INSECURE=true`) is explicit, and `insecure: true` is rejected for an
HTTPS endpoint. Bucket names are DNS-compatible and endpoint URLs cannot carry
credentials, paths, queries, or fragments. Dotted bucket names require
path-style addressing with HTTPS because wildcard certificates do not cover a
multi-label virtual-host bucket prefix. IP-literal endpoints also require
path-style addressing because prepending a bucket would no longer address the
configured origin.

Before an S3 store is bound to pointer, generation, lease, or hydration work,
the writer composition must use `objectstore.NewWriter`, which runs
`ProbeWriter` against a random disposable key. The low-level `objectstore.New`
factory does not certify writer safety and must not be bound directly to writer
repositories. The probe must prove create-if-absent, conditional replacement,
stale-write rejection, exact readback, conditional delete, and cleanup. A failed
or ambiguous probe prevents repository binding; merely constructing an HTTP
client is not writer-safety evidence.

Durable recovery records contain only backend kind, sanitized `s3://` identity,
and its configuration fingerprint. Endpoint and addressing policy contribute
to that fingerprint but credential-source configuration and resolved secrets do
not. Path-style requests use `<endpoint>/<bucket>/<prefix>/<key>`; virtual-host
requests use `<bucket>.<endpoint>/<prefix>/<key>` before SigV4 signing.

`PutImmutable` first uses `HEAD` to detect an existing key. An existing object is
accepted only when its expected size and `x-amz-meta-sha256` agree and a `GET`
readback reproduces the expected SHA-256, size, and opaque revision. Different
bytes return `ports.ErrConflict`; multipart ETags are opaque revisions and are
never interpreted as content hashes.

For a missing key, Camp opens the restartable source once to verify the exact
size and SHA-256 before creating remote multipart state, then opens it again to
upload deterministic, ascending parts bounded to 8 MiB. Every opening is
untrusted independently: the bytes consumed by multipart upload are hashed and
must match the expected size and SHA-256 before completion. Completion carries
`If-None-Match: *`, preserving create-only publication.

Every failure before completion attempts `AbortMultipartUpload` using an
independent bounded cleanup context. Abort failures are joined to the primary
error so cleanup loss is not hidden. Cancellation is checked while verifying
and uploading the source and before completion. A 5xx completion result is
ambiguous: Camp observes the key with `HEAD` and verified `GET` readback. A
verified object is accepted; an unverified outcome remains `ports.ErrAmbiguous`
and is not blindly completed again or aborted.

Focused verification lives in
`internal/adapters/s3store/multipart_test.go`. Run it with:

```sh
go test ./internal/adapters/s3store -run '^TestPutImmutable' -count=1 -v
```

The non-skipping real fixture in `integration/minio_s3store_test.go` runs MinIO
`RELEASE.2025-04-22T22-12-26Z` from the pinned image digest
`sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e`.
It proves create, streamed idempotent readback, conflicting-byte rejection,
cancellation with zero remaining multipart uploads, and reconciliation after a
committed completion response is deliberately lost. It also directly races an
uploaded contender against an existing key: MinIO returns HTTP 412 when
`CompleteMultipartUpload` carries `If-None-Match: *`, verifying the conditional
completion requirement rather than assuming it from synthetic HTTP tests.

Run the real contract with Docker available:

```sh
go test ./integration -run '^TestMinIOImmutableLifecycle$' -count=1 -v -timeout=3m
```

## Independent-controller acceptance

S3 concurrency and portability evidence must use separate OS processes with
disjoint `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, and
`XDG_CACHE_HOME`. Synchronize contenders only after each process has read the
same pointer revision and uploaded its independently named immutable archive
and sidecar. Then release both processes to call the production pointer
repository's compare-and-swap operation. Valid evidence has exactly one winner,
a typed `coordination.ErrPointerChanged` loser, and no active multipart
uploads. Register kill-and-wait cleanup immediately after every helper process
starts so a parent-side assertion or timeout cannot strand a controller.

Retained-loser evidence must read the archive to EOF and verify its exact size
and SHA-256, not merely prove that `GET` opens. Branch recovery requires writing
and reconciling metadata under the branch lineage before creating its pointer.
A separate fresh-XDG branch reader must then read the branch pointer, list and
validate its sidecar, download the archive, and verify the same size and digest.
Pointer creation by itself is not branch-reopen evidence.

A third process with a new XDG root must reconstruct the winning main pointer
and history from MinIO and verify the downloaded winner digest. Integration
harnesses must not synthesize recovery commands. An exact recovery-command
assertion becomes valid only when production application conflict handling
returns that command.

`TestS3TwoWriterConflict` is this repository/adapter-level process contract. It
also proves that branch-scoped metadata and a pointer rooted at the recorded
parent let a fresh process reopen the retained loser.
It does not prove a Camp lifecycle reopen: that claim requires a production
backend factory, S3 runtime configuration and credential-chain composition,
and lifecycle CLI/application wiring. Until those boundaries exist, do not
replace them with an integration-only factory or describe repository-level
download evidence as DevPod/Hauler reopen evidence.

Run the process contract with Docker available:

```sh
go test ./integration -run '^TestS3TwoWriterConflict$' -count=1 -v -timeout=3m
```
