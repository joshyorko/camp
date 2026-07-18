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
that same store, and carries the resolved identity into session recovery through
`config.DurableBackendConfiguration`. The constructor-bound descriptor is
authoritative: a request may repeat it but must fail closed if it supplies a
different identity, because request metadata cannot relabel the already-composed
store. Factory and application tests prove this composition seam; they are not
executable wiring proof. Until `cmd/camp` calls this seam, describe the feature
as backend composition rather than production CLI wiring.

The `s3://` URL contains only the bucket and optional clean prefix. Endpoint,
region, path-style addressing, and transport policy are separate bootstrap
values. The YAML `s3` block accepts only those non-secret values; AWS credential
source fields are not part of `userConfig`, and credentials come from the
standard runtime chain. IP-address-shaped and other non-DNS bucket names are
rejected. HTTPS is the default policy;
an `http://` endpoint is rejected unless `insecure: true` (or
`CAMP_S3_INSECURE=true`) is explicit, and `insecure: true` is rejected for an
HTTPS endpoint. Bucket names are DNS-compatible and endpoint URLs cannot carry
credentials, paths, queries, or fragments.

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
