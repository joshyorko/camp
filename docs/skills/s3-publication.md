# S3 immutable publication

Camp's S3 adapter publishes immutable objects with the existing injected HTTP
`Signer` seam. This slice does not adopt the AWS SDK for Go v2: backend
composition and runtime credential-chain selection remain separate work.
Credentials must stay inside the signer and must not enter `s3store.Config`,
object metadata, object keys, journals, or logs.

`PutImmutable` first uses `HEAD` to detect an existing key. An existing object is
accepted only when its expected size and `x-amz-meta-sha256` agree and a `GET`
readback reproduces the expected SHA-256, size, and opaque revision. Different
bytes return `ports.ErrConflict`; multipart ETags are opaque revisions and are
never interpreted as content hashes.

For a missing key, Camp opens the restartable source once to verify the exact
size and SHA-256 before creating remote multipart state, then opens it again to
upload deterministic, ascending parts bounded to 8 MiB. Completion carries
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
