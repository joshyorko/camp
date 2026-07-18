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
